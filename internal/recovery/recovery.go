// Package recovery re-establishes a trustworthy Duo state after a crash.
//
// It treats state.json as a checkpoint and Git as ground truth: persisted
// signatures are revalidated against the real worktrees and revoked when their
// recorded evidence no longer matches, and an interrupted integration merge is
// detected instead of being repeated. Recovery never moves a session backwards
// (for example from REVIEW to PLAN); it either keeps the recorded phase or
// advances it to INTEGRATE when Git proves the merge already landed.
package recovery

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

var agents = []protocol.AgentID{protocol.Austin, protocol.Tony}

// ExpectedEvidence returns the evidence string that a phase signature must
// carry. EXECUTE signs the agent's own HEAD, REVIEW signs the peer's HEAD and
// INTEGRATE signs Austin's integrated HEAD. Live signing and crash recovery
// share this rule so they can never drift apart.
func ExpectedEvidence(
	ctx context.Context,
	ws workspace.Manager,
	phase project.Phase,
	signer protocol.AgentID,
	planVersion int,
) (string, error) {
	switch phase {
	case project.PhasePlan:
		return fmt.Sprintf("plan-v%d", planVersion), nil

	case project.PhaseExecute:
		artifact, err := ws.CaptureArtifact(ctx, signer)
		if err != nil {
			return "", err
		}
		return artifact.Commit, nil

	case project.PhaseReview:
		peer := protocol.PeerOf(signer)
		artifact, err := ws.CaptureArtifact(ctx, peer)
		if err != nil {
			return "", fmt.Errorf("cannot sign REVIEW until %s has a clean reviewable worktree: %w", peer, err)
		}
		return artifact.Commit, nil

	case project.PhaseIntegrate:
		artifact, err := ws.CaptureArtifact(ctx, protocol.Austin)
		if err != nil {
			return "", fmt.Errorf("integration branch is not ready for sign-off: %w", err)
		}
		return artifact.Commit, nil

	default:
		return "", fmt.Errorf("cannot sign phase %s", phase)
	}
}

// FastVerificationTarget returns the Austin HEAD that a Fast verification must
// be bound to: the same clean, committed-artifact rule that Goal EXECUTE
// signatures use. The live verification gate and crash recovery both call it,
// so a passed verdict can never describe an artifact that no longer exists.
func FastVerificationTarget(ctx context.Context, ws workspace.Manager) (string, error) {
	artifact, err := ws.CaptureArtifact(ctx, protocol.Austin)
	if err != nil {
		return "", fmt.Errorf("cannot verify until Austin has a clean, committed worktree: %w", err)
	}
	return artifact.Commit, nil
}

// ComposeInput collects everything needed to build one durable snapshot.
type ComposeInput struct {
	DuoVersion   string
	SessionID    string
	RepoID       string
	Repository   string
	BaseBranch   string
	BaseCommit   string
	CreatedAt    time.Time
	Project      project.Snapshot
	Worktrees    workspace.Set
	DriverStates map[protocol.AgentID]sessionstore.DriverState // authoritative plugin-owned state
	AgentDrivers map[protocol.AgentID]string
	AgentModels  map[protocol.AgentID]string
	Integration  workspace.IntegrationResult
	Delivery     sessionstore.Delivery
	// Usage is the session's cost ledger as the coordinator holds it. It is nil when
	// nothing has been measured, which is what keeps a driver that cannot measure cost
	// from persisting a ledger that reads as a cost of zero.
	Usage *sessionstore.UsageLedger
}

// Compose turns live domain, workspace and Pi state into the snapshot that is
// written to disk. It is called after every mutation, so it must stay cheap.
func Compose(in ComposeInput) sessionstore.Snapshot {
	snap := sessionstore.Snapshot{
		SchemaVersion: sessionstore.SchemaVersion,
		DuoVersion:    in.DuoVersion,
		SessionID:     in.SessionID,
		RepoID:        in.RepoID,
		Repository:    in.Repository,
		ScopePath:     in.Worktrees.ScopePath,
		BaseBranch:    in.BaseBranch,
		BaseCommit:    in.BaseCommit,
		Phase:         string(in.Project.Phase),
		Plan:          in.Project.Plan,
		PlanVersion:   in.Project.PlanVersion,
		Step:          in.Project.Step,
		Steps:         in.Project.Steps,
		Started:       in.Project.Started,
		Mode:          in.Project.EffectiveMode(),
		Driver:        driverOf(in.Project.EffectiveMode()),
		Ready:         make(map[protocol.AgentID]bool, len(agents)),
		Notes:         make(map[protocol.AgentID]string, len(agents)),
		Evidence:      make(map[protocol.AgentID]string, len(agents)),
		Worktrees:     make(map[protocol.AgentID]sessionstore.Worktree, len(agents)),
		Integration: sessionstore.Integration{
			Started:    in.Integration.Head != "" || in.Integration.Conflicted,
			Conflicted: in.Integration.Conflicted,
			Head:       in.Integration.Head,
			MergedTony: in.Integration.MergedTony,
		},
		Delivery:  in.Delivery,
		Usage:     in.Usage,
		CreatedAt: in.CreatedAt,
	}

	if len(in.DriverStates) > 0 {
		for k, v := range in.DriverStates {
			if len(v.State) == 0 {
				continue
			}
			if snap.DriverStates == nil {
				snap.DriverStates = make(map[protocol.AgentID]sessionstore.DriverState, len(in.DriverStates))
			}
			snap.DriverStates[k] = v
		}
	}
	if len(in.AgentDrivers) > 0 {
		snap.AgentDrivers = make(map[protocol.AgentID]string, len(in.AgentDrivers))
		for k, v := range in.AgentDrivers {
			if v != "" {
				snap.AgentDrivers[k] = v
			}
		}
	}
	if len(in.AgentModels) > 0 {
		snap.AgentModels = make(map[protocol.AgentID]string, len(in.AgentModels))
		for k, v := range in.AgentModels {
			if v != "" {
				snap.AgentModels[k] = v
			}
		}
	}

	if v := in.Project.Verification; v.Status != project.VerificationNone || v.Head != "" || v.Note != "" {
		snap.Verification = &sessionstore.Verification{
			Status: string(v.Status),
			Head:   v.Head,
			Note:   v.Note,
			At:     v.At,
		}
	}

	for _, agent := range agents {
		snap.Ready[agent] = in.Project.Ready[agent]
		snap.Notes[agent] = in.Project.Notes[agent]
		snap.Evidence[agent] = in.Project.Evidence[agent]
		if wt, ok := in.Worktrees.For(agent); ok {
			snap.Worktrees[agent] = sessionstore.Worktree{Path: wt.Path, Branch: wt.Branch}
		}
	}
	return snap
}

// driverOf records the mode's single driver for observability. It is derived
// from the mode, never read back as state: Fast is always Austin-driven, Step is
// Austin-driven per step (Tony's per-step verb is a verdict, not a phase move),
// and Goal has no single driver because the mover changes per phase.
func driverOf(mode project.Mode) string {
	switch mode {
	case project.ModeFast, project.ModeStep:
		return string(protocol.Austin)
	}
	return ""
}

// ProjectSnapshot extracts the domain-only part of a persisted snapshot.
func ProjectSnapshot(snap sessionstore.Snapshot) project.Snapshot {
	out := project.Snapshot{
		Mode:         snap.EffectiveMode(),
		Phase:        project.Phase(snap.Phase),
		Plan:         snap.Plan,
		PlanVersion:  snap.PlanVersion,
		Step:         snap.Step,
		Steps:        snap.Steps,
		Started:      snap.Started,
		Verification: snap.VerificationResult(),
		Ready:        make(map[protocol.AgentID]bool, len(agents)),
		Notes:        make(map[protocol.AgentID]string, len(agents)),
		Evidence:     make(map[protocol.AgentID]string, len(agents)),
	}
	for _, agent := range agents {
		out.Ready[agent] = snap.Ready[agent]
		out.Notes[agent] = snap.Notes[agent]
		out.Evidence[agent] = snap.Evidence[agent]
	}
	return out
}

// Validate proves that the persisted session still describes this repository
// and that both worktrees exist on their recorded branches. It runs before any
// agent is started, so a broken session fails loudly instead of half-starting.
func Validate(ctx context.Context, ws *workspace.GitManager, snap sessionstore.Snapshot) error {
	root, err := workspace.FindRoot(ctx, snap.Repository)
	if err != nil {
		return err
	}
	if !workspace.SamePath(root, snap.Repository) {
		return fmt.Errorf("session was created for %s but the repository here is %s", snap.Repository, root)
	}
	for _, agent := range agents {
		if _, ok := snap.Worktree(agent); !ok {
			return fmt.Errorf("session state has no %s worktree recorded", agent)
		}
		if err := ws.Validate(ctx, agent); err != nil {
			return err
		}
	}
	return nil
}

// Report summarises what recovery changed relative to the persisted snapshot.
type Report struct {
	Phase                project.Phase
	Revoked              []protocol.AgentID
	IntegrationRecovered bool
	Notes                []string
}

func (r Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "recovered phase %s", r.Phase)
	if r.IntegrationRecovered {
		b.WriteString("; integration state rebuilt from Git")
	}
	if len(r.Revoked) > 0 {
		names := make([]string, 0, len(r.Revoked))
		for _, agent := range r.Revoked {
			names = append(names, string(agent))
		}
		fmt.Fprintf(&b, "; revoked stale signatures: %s", strings.Join(names, ", "))
	}
	if len(r.Notes) > 0 {
		b.WriteString("; " + strings.Join(r.Notes, "; "))
	}
	return b.String()
}

// Result is a reconciled snapshot plus what changed.
type Result struct {
	Snapshot sessionstore.Snapshot
	Report   Report
}

// Apply installs the reconciled snapshot into the live project state.
func (r Result) Apply(state *project.State) error {
	return state.Restore(ProjectSnapshot(r.Snapshot))
}

// Reconcile re-derives trustworthy state from Git. It never mutates live state
// and never writes to disk; the caller decides whether to persist and apply the
// result, so a failed recovery leaves the previous checkpoint untouched.
func Reconcile(ctx context.Context, ws *workspace.GitManager, snap sessionstore.Snapshot) (Result, error) {
	out := snap
	report := Report{Phase: project.Phase(snap.Phase)}

	// A hand-edited or truncated state.json can decode without these objects, and
	// the rules below assign into them.
	if out.Ready == nil {
		out.Ready = make(map[protocol.AgentID]bool, len(agents))
	}
	if out.Notes == nil {
		out.Notes = make(map[protocol.AgentID]string, len(agents))
	}
	if out.Evidence == nil {
		out.Evidence = make(map[protocol.AgentID]string, len(agents))
	}

	mergeState, err := ws.MergeState(ctx, protocol.Austin)
	if err != nil {
		return Result{}, err
	}

	// The rules below rebuild the integration merge, which is the Fast and Goal
	// gate between REVIEW and INTEGRATE. STEP mode has no such merge step: Austin
	// merges Tony's per-step work inside EXECUTE, and a step is gated by Tony's
	// per-step verdict, so promoting a STEP session to INTEGRATE on merge evidence
	// would land a step that was never reviewed.
	stepMode := snap.EffectiveMode() == project.ModeStep

	switch {
	case mergeState.InProgress && stepMode:
		// Austin was merging Tony's work for the step he is building. The merge is
		// his in-flight work, not a finished integration, so the recorded phase
		// stands and the agent is told to finish it.
		report.Notes = append(report.Notes, "an interrupted merge was found in Austin's worktree; finish or abort it before submitting the step")

	case mergeState.InProgress:
		head, err := ws.Head(ctx, protocol.Austin)
		if err != nil {
			return Result{}, err
		}
		report.IntegrationRecovered = true
		report.Notes = append(report.Notes, "an interrupted integration merge was found in Austin's worktree; Duo will not merge again")
		report.Phase = project.PhaseIntegrate
		out.Phase = string(project.PhaseIntegrate)
		out.Integration = sessionstore.Integration{
			Started:    true,
			Conflicted: mergeState.Conflicted,
			Head:       head,
			MergedTony: mergeState.MergeHead,
		}

	case !stepMode && (snap.Phase == string(project.PhaseReview) || snap.Phase == string(project.PhaseIntegrate)):
		// A crash may have happened after `git merge` committed Tony's work but
		// before the result was persisted. Tony's HEAD being an ancestor of
		// Austin's HEAD is the proof that the merge already landed.
		tonyHead, err := ws.Head(ctx, protocol.Tony)
		if err != nil {
			return Result{}, err
		}
		austinHead, err := ws.Head(ctx, protocol.Austin)
		if err != nil {
			return Result{}, err
		}
		merged, err := ws.IsAncestor(ctx, protocol.Austin, tonyHead, austinHead)
		if err != nil {
			return Result{}, err
		}
		// Both sides must have moved off the base commit. Capturing an artifact
		// only requires a clean worktree, not a new commit, so Tony legitimately
		// sits at base; base is then an ancestor of Austin and `merged` is
		// trivially true, which would record a merge that never happened.
		if merged && tonyHead != "" && tonyHead != snap.BaseCommit && austinHead != snap.BaseCommit {
			report.IntegrationRecovered = true
			report.Notes = append(report.Notes, "Tony's work is already merged into Austin; integration recorded as complete")
			report.Phase = project.PhaseIntegrate
			out.Phase = string(project.PhaseIntegrate)
			out.Integration = sessionstore.Integration{
				Started:    true,
				Head:       austinHead,
				MergedTony: tonyHead,
			}
		}
	}

	// Fast sessions have no signatures to revoke; their single gate is a
	// verification bound to one Austin HEAD. Re-derive that binding from Git. A
	// stale pass invalidates the completion request and returns to RUNNING,
	// mirroring the live `ready=false` transition. This is a deterministic
	// Fast-mode rule, not a generic phase rewind: RUNNING is only reachable from
	// VERIFY, and only when the verified artifact is gone.
	if out.EffectiveMode() == project.ModeFast && out.Phase == string(project.PhaseVerify) {
		if v := out.Verification; v != nil && v.Status == string(project.VerificationPassed) {
			target, err := FastVerificationTarget(ctx, ws)
			if err != nil || target != strings.TrimSpace(v.Head) {
				out.Verification = nil
				out.Phase = string(project.PhaseRunning)
				out.Notes[protocol.Tony] = ""
				out.Notes[protocol.Austin] = "the verified artifact changed; verification revoked on resume, request verification again when ready"
				report.Phase = project.PhaseRunning
				report.Revoked = append(report.Revoked, protocol.Austin)
				report.Notes = append(report.Notes, "Fast verification no longer matches Austin's HEAD; returned to RUNNING")
			}
		}
	}

	// STEP sessions reach INTEGRATE only through Tony's per-step verdict, which is
	// bound to one exact Austin HEAD. A checkpoint that claims INTEGRATE without a
	// verdict binding Austin's current artifact is a phase record that outlived the
	// gate that created it, and INTEGRATE is the phase that lands a step, so the
	// step would land on the user's branch unreviewed. The finished queue has no
	// current step and its gate is the two signatures, so it stands.
	if out.EffectiveMode() == project.ModeStep && out.Phase == string(project.PhaseIntegrate) {
		if cur := stepUnderCursor(out.Steps, out.Step); cur != nil && !cur.Done {
			head, err := FastVerificationTarget(ctx, ws)
			if err != nil || cur.Verification.Status != project.VerificationPassed || !cur.Verification.Targets(head) {
				steps := make(project.Steps, len(out.Steps))
				copy(steps, out.Steps)
				steps[out.Step-1].Verification = project.Verification{}
				out.Steps = steps
				out.Phase = string(project.PhaseReview)
				report.Phase = project.PhaseReview
				report.Notes = append(report.Notes,
					fmt.Sprintf("step %d has no verdict binding Austin's current HEAD; it is under review again", out.Step))
			}
		}
	}

	// Revoke any signature whose evidence no longer matches Git. A dirty
	// worktree makes CaptureArtifact fail, which revokes exactly the signatures
	// that require a clean artifact and leaves the session usable.
	for _, signer := range agents {
		if !out.Ready[signer] {
			continue
		}
		expected, err := ExpectedEvidence(ctx, ws, project.Phase(out.Phase), signer, out.PlanVersion)
		switch {
		case err != nil:
			out.Ready[signer] = false
			out.Evidence[signer] = ""
			out.Notes[signer] = "signed target is no longer clean/reviewable; readiness revoked on resume"
			report.Revoked = append(report.Revoked, signer)
		case strings.TrimSpace(expected) == "" || expected != out.Evidence[signer]:
			out.Ready[signer] = false
			out.Evidence[signer] = ""
			out.Notes[signer] = "signed target changed; readiness revoked on resume"
			report.Revoked = append(report.Revoked, signer)
		}
	}

	// Step mode: every landed step goes to the base branch, so a crash between a
	// step landing and the verifier sync leaves Tony's worktree on the previous
	// step — he would review and build a tree the user never received. The
	// fast-forward is a no-op when Tony is already current, so replaying it here is
	// safe whether or not the original attempt completed. The target is the landed
	// step's own head, never Austin's live HEAD, so in-flight work for the next
	// step is never handed to the verifier as if it had landed.
	if out.EffectiveMode() == project.ModeStep {
		if landed := lastLandedStepHead(out.Steps); landed != "" {
			sync, err := ws.SyncTonyTo(ctx, landed)
			switch {
			case err != nil:
				report.Notes = append(report.Notes, "could not fast-forward Tony onto the landed step: "+err.Error())
			case sync.Skipped:
				report.Notes = append(report.Notes, sync.Reason)
			case sync.Moved:
				report.Notes = append(report.Notes, "Tony's worktree was fast-forwarded to the landed step "+shortHead(sync.Head))
			}
		}
	}

	return Result{Snapshot: out, Report: report}, nil
}

// stepUnderCursor returns the step the queue cursor points at, or nil when the
// queue is finished (or has no cursor), which is the case the two signatures gate.
func stepUnderCursor(steps project.Steps, cursor int) *project.Step {
	if cursor < 1 || cursor > len(steps) {
		return nil
	}
	return &steps[cursor-1]
}

// lastLandedStepHead returns the delivery head of the newest step that reached
// the user's repository, or "" when no step has landed yet.
func lastLandedStepHead(steps project.Steps) string {
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].Done {
			return strings.TrimSpace(steps[i].DeliveryHead)
		}
	}
	return ""
}

func shortHead(head string) string {
	head = strings.TrimSpace(head)
	if len(head) > 10 {
		return head[:10]
	}
	return head
}

// ReconcileAndApply is the common resume path: reconcile, then install the
// result into the live project state.
func ReconcileAndApply(
	ctx context.Context,
	ws *workspace.GitManager,
	state *project.State,
	snap sessionstore.Snapshot,
) (Result, error) {
	result, err := Reconcile(ctx, ws, snap)
	if err != nil {
		return Result{}, err
	}
	if err := result.Apply(state); err != nil {
		return Result{}, err
	}
	return result, nil
}
