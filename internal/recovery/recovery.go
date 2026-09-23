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

// ComposeInput collects everything needed to build one durable snapshot.
type ComposeInput struct {
	DuoVersion  string
	SessionID   string
	RepoID      string
	Repository  string
	BaseBranch  string
	BaseCommit  string
	CreatedAt   time.Time
	Project     project.Snapshot
	Worktrees   workspace.Set
	PiSessions  map[protocol.AgentID]string
	Integration workspace.IntegrationResult
	Delivery    sessionstore.Delivery
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
		BaseBranch:    in.BaseBranch,
		BaseCommit:    in.BaseCommit,
		Phase:         string(in.Project.Phase),
		Plan:          in.Project.Plan,
		PlanVersion:   in.Project.PlanVersion,
		Started:       in.Project.Started,
		Ready:         make(map[protocol.AgentID]bool, len(agents)),
		Notes:         make(map[protocol.AgentID]string, len(agents)),
		Evidence:      make(map[protocol.AgentID]string, len(agents)),
		Worktrees:     make(map[protocol.AgentID]sessionstore.Worktree, len(agents)),
		PiSessions:    make(map[protocol.AgentID]string, len(agents)),
		Integration: sessionstore.Integration{
			Started:    in.Integration.Head != "" || in.Integration.Conflicted,
			Conflicted: in.Integration.Conflicted,
			Head:       in.Integration.Head,
			MergedTony: in.Integration.MergedTony,
		},
		Delivery:  in.Delivery,
		CreatedAt: in.CreatedAt,
	}

	for _, agent := range agents {
		snap.Ready[agent] = in.Project.Ready[agent]
		snap.Notes[agent] = in.Project.Notes[agent]
		snap.Evidence[agent] = in.Project.Evidence[agent]
		if wt, ok := in.Worktrees.For(agent); ok {
			snap.Worktrees[agent] = sessionstore.Worktree{Path: wt.Path, Branch: wt.Branch}
		}
		if id := strings.TrimSpace(in.PiSessions[agent]); id != "" {
			snap.PiSessions[agent] = id
		}
	}
	return snap
}

// ProjectSnapshot extracts the domain-only part of a persisted snapshot.
func ProjectSnapshot(snap sessionstore.Snapshot) project.Snapshot {
	out := project.Snapshot{
		Phase:       project.Phase(snap.Phase),
		Plan:        snap.Plan,
		PlanVersion: snap.PlanVersion,
		Started:     snap.Started,
		Ready:       make(map[protocol.AgentID]bool, len(agents)),
		Notes:       make(map[protocol.AgentID]string, len(agents)),
		Evidence:    make(map[protocol.AgentID]string, len(agents)),
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

	mergeState, err := ws.MergeState(ctx, protocol.Austin)
	if err != nil {
		return Result{}, err
	}

	switch {
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

	case snap.Phase == string(project.PhaseReview) || snap.Phase == string(project.PhaseIntegrate):
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
		if merged && tonyHead != "" && austinHead != snap.BaseCommit {
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

	return Result{Snapshot: out, Report: report}, nil
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
