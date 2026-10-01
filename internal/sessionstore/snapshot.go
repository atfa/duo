package sessionstore

import (
	"crypto/rand"
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

// SchemaVersion is the persisted snapshot schema. Loading a snapshot written by
// an incompatible schema is refused explicitly instead of resetting to PLAN.
const SchemaVersion = 1

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
// zero value for every v0.4.0 snapshot, so legacy sessions load unchanged.
const (
	DeliveryPending = "pending"
	DeliveryApplied = "applied"
	DeliveryBlocked = "blocked"
)

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
// optional extension to schema version 1: Goal and legacy snapshots simply
// have no verification object. `head` binds a passed verdict to one exact
// Austin artifact, `note` carries an issue description, and `at` records when
// the verdict was formed.
type Verification struct {
	Status string     `json:"status,omitempty"`
	Head   string     `json:"head,omitempty"`
	Note   string     `json:"note,omitempty"`
	At     *time.Time `json:"at,omitempty"`
}

// Snapshot is the durable checkpoint for one Duo session. It combines the
// project domain state with workspace, Pi session identity and integration
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

	// Mode is the session workflow, fixed for the session's lifetime. Driver is
	// recorded for observability only: it is derived from the mode (Fast is
	// always Austin-driven) and no reader branches on it. Both are omitted by
	// legacy v0.4.x snapshots, which load as Goal.
	Mode   project.Mode `json:"mode,omitempty"`
	Driver string       `json:"driver,omitempty"`

	AgentDrivers map[protocol.AgentID]string `json:"agentDrivers,omitempty"`
	AgentModels  map[protocol.AgentID]string `json:"agentModels,omitempty"`

	Ready    map[protocol.AgentID]bool   `json:"ready"`
	Notes    map[protocol.AgentID]string `json:"notes"`
	Evidence map[protocol.AgentID]string `json:"evidence"`

	Verification *Verification `json:"verification,omitempty"`

	Worktrees  map[protocol.AgentID]Worktree `json:"worktrees"`
	PiSessions map[protocol.AgentID]string   `json:"piSessions"`

	Integration Integration `json:"integration"`
	Delivery    Delivery    `json:"delivery"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// EffectiveMode is the single normalization point for the persisted mode.
// Only an explicitly recorded "fast" is Fast; a legacy snapshot with no mode
// field, and any unrecognized value, is Goal. Every reader goes through here so
// the legacy rule cannot drift between resolver, coordinator, harness and CLI.
func (s Snapshot) EffectiveMode() project.Mode {
	if s.Mode == project.ModeFast {
		return project.ModeFast
	}
	return project.ModeGoal
}

// VerificationResult returns the persisted verification, or a zero value when
// the session has none (Goal and legacy snapshots).
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

// NeedsDelivery reports whether this session has an agent-approved artifact that
// has not been handed back to the user's repository yet. It covers the normal
// INTEGRATE dual sign-off, a delivery that was interrupted mid-transaction,
// legacy v0.4.0 snapshots that reached DONE without any delivery record at all,
// and a Fast session whose verification passed but whose delivery was
// interrupted before the durable delivery record was written.
//
// A Fast pass is only deliverable while it is still bound to the current Austin
// HEAD; recovery clears a stale pass before this is consulted, and the delivery
// transaction re-checks the binding, so this predicate never has to re-derive
// Git state.
func (s Snapshot) NeedsDelivery() bool {
	if s.Delivery.Applied() {
		return false
	}
	switch s.EffectiveMode() {
	case project.ModeFast:
		return s.Phase == string(project.PhaseVerify) && s.VerificationResult().Status == project.VerificationPassed
	}
	switch s.Phase {
	case "INTEGRATE":
		return s.Ready[protocol.Austin] && s.Ready[protocol.Tony]
	case "DONE":
		// v0.4.0 marked DONE as soon as both agents signed, without ever
		// touching the user's repository. Integration.Head or a recorded
		// delivery head is the evidence that there is something to apply.
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

// NewUUID returns a random RFC 4122 version 4 UUID, used for stable Pi session
// identities.
func NewUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
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
