package sessionstore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// SchemaVersion is the persisted snapshot schema. Version 2 removes the Pi-only
// resume mirror; incompatible snapshots are refused instead of migrated.
const SchemaVersion = 2

type Worktree struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
}

type Integration struct {
	Started    bool   `json:"started"`
	Conflicted bool   `json:"conflicted"`
	Head       string `json:"head"`
	MergedTony string `json:"mergedTony"`
}

// Delivery statuses. The empty status means "never attempted", which is the
// zero value when delivery has not been attempted.
const (
	DeliveryPending = "pending"
	DeliveryApplied = "applied"
	DeliveryBlocked = "blocked"
)

// StepDelivery records one landed step of a Step-mode session. A Step session
// delivers per step, so the durable per-step record lives on the step itself
// (project.Step.DeliveryHead); this type is the shape the delivery transaction
// returns so the step record and the session record cannot disagree.
type StepDelivery struct {
	Step        int        `json:"step"`
	FinalHead   string     `json:"finalHead"`
	AppliedHead string     `json:"appliedHead"`
	At          *time.Time `json:"at,omitempty"`
}

// Delivery is the durable checkpoint for handing the final Duo artifact back to
// the repository the user launched Duo from. It is a small optional extension
// to schema version 1: old snapshots simply decode to the zero value.
type Delivery struct {
	Status      string `json:"status"`
	FinalHead   string `json:"finalHead"`
	FinalBranch string `json:"finalBranch"`

	TargetRepo   string `json:"targetRepo,omitempty"`
	TargetBranch string `json:"targetBranch,omitempty"`

	AppliedHead string `json:"appliedHead,omitempty"`
	Reason      string `json:"reason,omitempty"`

	AttemptedAt *time.Time `json:"attemptedAt,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

// Applied reports whether the final artifact is already in the user's repo.
func (d Delivery) Applied() bool { return d.Status == DeliveryApplied }

// Verification is the durable Fast-mode verification record. It is a small
// optional checkpoint: Goal snapshots simply have no verification object.
// `head` binds a passed verdict to one exact Austin artifact, `note` carries an
// issue description, and `at` records when
// the verdict was formed.
type Verification struct {
	Status string     `json:"status,omitempty"`
	Head   string     `json:"head,omitempty"`
	Note   string     `json:"note,omitempty"`
	At     *time.Time `json:"at,omitempty"`
}

// Snapshot is the durable checkpoint for one Duo session. It combines the
// project domain state with workspace, opaque driver state and integration
// state. Git remains the ground truth for artifacts and evidence.
type Snapshot struct {
	SchemaVersion int    `json:"schemaVersion"`
	DuoVersion    string `json:"duoVersion"`

	SessionID  string `json:"sessionId"`
	RepoID     string `json:"repoId"`
	Repository string `json:"repository"`
	ScopePath  string `json:"scopePath,omitempty"`
	BaseBranch string `json:"baseBranch"`
	BaseCommit string `json:"baseCommit"`

	Phase       string `json:"phase"`
	Plan        string `json:"plan"`
	PlanVersion int    `json:"planVersion"`
	Started     bool   `json:"started"`

	// Step is the Step-mode queue cursor (1-based; len(Steps)+1 when the queue is
	// finished) and Steps is the queue itself. Both are additive optional state, so
	// the persisted schema version is unchanged and a snapshot written by an older
	// Duo still loads.
	Step  int           `json:"step,omitempty"`
	Steps project.Steps `json:"steps,omitempty"`

	// Mode is the session's persisted workflow. It can change at runtime; a
	// snapshot records the mode in force when it was written. Driver is recorded
	// for observability only: it is derived from the mode (Fast is always
	// Austin-driven) and no reader branches on it. Both are omitted by snapshots
	// that omit it, which normalize to Goal.
	Mode   project.Mode `json:"mode,omitempty"`
	Driver string       `json:"driver,omitempty"`

	AgentDrivers map[protocol.AgentID]string `json:"agentDrivers,omitempty"`
	AgentModels  map[protocol.AgentID]string `json:"agentModels,omitempty"`
	PauseState   string                      `json:"pauseState,omitempty"`
	PausedAgents map[protocol.AgentID]bool   `json:"pausedAgents,omitempty"`

	Ready    map[protocol.AgentID]bool   `json:"ready"`
	Notes    map[protocol.AgentID]string `json:"notes"`
	Evidence map[protocol.AgentID]string `json:"evidence"`

	Verification *Verification `json:"verification,omitempty"`

	Worktrees map[protocol.AgentID]Worktree `json:"worktrees"`

	// DriverStates contains each driver's opaque resume state.
	DriverStates map[protocol.AgentID]DriverState `json:"driverStates,omitempty"`

	Integration Integration `json:"integration"`
	Delivery    Delivery    `json:"delivery"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// EffectiveMode is the single normalization point for the persisted mode. It
// delegates to project.Mode.Normalize so the rule — only an explicitly recorded
// mode is honored, an absent mode and any unrecognized value are Goal — cannot
// drift between resolver, coordinator, harness and CLI.
func (s Snapshot) EffectiveMode() project.Mode {
	return s.Mode.Normalize()
}

// VerificationResult returns the persisted verification, or a zero value when
// the session has none (Goal snapshots).
func (s Snapshot) VerificationResult() project.Verification {
	if s.Verification == nil {
		return project.Verification{}
	}
	return project.Verification{
		Status: project.VerificationResult(s.Verification.Status),
		Head:   s.Verification.Head,
		Note:   s.Verification.Note,
		At:     s.Verification.At,
	}
}

func (s Snapshot) Worktree(agent protocol.AgentID) (Worktree, bool) {
	wt, ok := s.Worktrees[agent]
	return wt, ok
}

// StepUnderCursor returns the step the queue cursor points at, or nil when the
// queue is finished and there is no step left to land.
func (s Snapshot) StepUnderCursor() *project.Step {
	if s.Step < 1 || s.Step > len(s.Steps) {
		return nil
	}
	return &s.Steps[s.Step-1]
}

// StepDeliveryIsCurrent reports whether the applied delivery record describes the
// step under the cursor: that step is open, it was submitted, and the recorded
// final HEAD is exactly that submission. This is the crash window where the
// landing reached the user's repository and the queue never advanced.
//
// In STEP mode the session-level record is the latest landing, so it survives the
// queue advancing. Every consumer that reads "applied" must scope it to one step
// with this predicate, or a step submitted after a landing looks finished.
func (s Snapshot) StepDeliveryIsCurrent() bool {
	current := s.StepUnderCursor()
	if current == nil || current.Done {
		return false
	}
	head := strings.TrimSpace(current.Head)
	return head != "" && strings.EqualFold(head, strings.TrimSpace(s.Delivery.FinalHead))
}

// StepAwaitsDelivery reports whether the queue has an open step that the delivery
// record does not describe: a step submitted after the last landing is a new
// transaction that still needs delivery. Queues in the other modes have no steps,
// so this is false for them.
func (s Snapshot) StepAwaitsDelivery() bool {
	current := s.StepUnderCursor()
	return current != nil && !current.Done && !s.StepDeliveryIsCurrent()
}

// NeedsDelivery reports whether this session has an agent-approved artifact that
// has not been handed back to the user's repository yet. It covers the normal
// INTEGRATE dual sign-off, a delivery interrupted mid-transaction, a DONE
// snapshot without a delivery record, and a Fast session whose verification
// passed but whose delivery record was interrupted.
//
// A Fast pass is only deliverable while it is still bound to the current Austin
// HEAD; recovery clears a stale pass before this is consulted, and the delivery
// transaction re-checks the binding, so this predicate never has to re-derive
// Git state.
func (s Snapshot) NeedsDelivery() bool {
	// In STEP mode an applied record is the latest landing, not the end of the
	// session: a step submitted after it is a new transaction, and reading the
	// record as "nothing left to deliver" leaves the session stuck in INTEGRATE
	// with no way for `duo apply` to find it.
	if s.Delivery.Applied() && !s.StepAwaitsDelivery() {
		return false
	}
	switch s.EffectiveMode() {
	case project.ModeFast:
		return s.Phase == string(project.PhaseVerify) && s.VerificationResult().Status == project.VerificationPassed
	case project.ModeStep:
		// A Step session delivers per step. INTEGRATE means "the reviewed step is
		// ready to land", and once the queue is finished it means "the finished queue
		// is ready to be signed off" — which is delivered by the same transaction.
		if s.Phase != string(project.PhaseIntegrate) {
			return false
		}
		for _, step := range s.Steps {
			if !step.Done {
				return true
			}
		}
		return s.Ready[protocol.Austin] && s.Ready[protocol.Tony]
	}
	switch s.Phase {
	case "INTEGRATE":
		return s.Ready[protocol.Austin] && s.Ready[protocol.Tony]
	case "DONE":
		// Integration.Head or a recorded delivery head proves there is an artifact
		// to apply.
		return strings.TrimSpace(s.Integration.Head) != "" || strings.TrimSpace(s.Delivery.FinalHead) != ""
	default:
		return false
	}
}

// FinalHead resolves the artifact this session considers final, preferring the
// durable delivery record over the integration checkpoint.
func (s Snapshot) FinalHead() string {
	if head := strings.TrimSpace(s.Delivery.FinalHead); head != "" {
		return head
	}
	return strings.TrimSpace(s.Integration.Head)
}

// RepoID derives a stable, collision-resistant store name from the absolute
// repository path, so ~/Projects/foo and /tmp/foo never share a session store.
func RepoID(repository string) string {
	path := strings.TrimSpace(repository)
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	path = filepath.Clean(path)
	sum := sha256.Sum256([]byte(path))
	name := sanitize(filepath.Base(path))
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "repo"
	}
	return fmt.Sprintf("%s-%s", name, hex.EncodeToString(sum[:4]))
}

var invalidName = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// validName reports whether a store path component is safe to join: a single
// plain path element with no separators, no traversal and no hidden dot-only
// name. Session ids and repo ids both flow into filesystem paths.
func validName(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	if strings.ContainsAny(value, `/\`) || strings.ContainsRune(value, 0) {
		return false
	}
	return value == sanitize(value)
}

func sanitize(value string) string {
	value = strings.TrimSpace(value)
	value = invalidName.ReplaceAllString(value, "-")
	return strings.Trim(value, "-._")
}
