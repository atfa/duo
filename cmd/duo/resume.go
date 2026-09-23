package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/recovery"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

// runResume reloads a persisted session, proves it is still valid against Git,
// revokes anything that is no longer true, and only then starts anything.
func runResume(ctx context.Context, cfg config, root, repoID, baseDir string) error {
	summary, err := selectSession(baseDir, repoID, cfg.resumeSession, root)
	if err != nil {
		return err
	}
	snap := summary.Snapshot

	if snap.Phase == string(project.PhaseDone) {
		return fmt.Errorf("Duo session %s is already DONE; start a new session with `duo`", snap.SessionID)
	}
	if strings.TrimSpace(snap.Repository) != "" && !workspace.SamePath(snap.Repository, root) {
		return fmt.Errorf("session %s belongs to %s, not %s", snap.SessionID, snap.Repository, root)
	}

	store, err := sessionstore.Open(baseDir, repoID, snap.SessionID)
	if err != nil {
		return err
	}
	lock, err := store.Lock()
	if err != nil {
		return err
	}
	defer lock.Release()

	logger := store.OpenLog()
	journal := store.OpenEvents()
	logger.Printf("resuming Duo session %s from %s (persisted phase %s repository=%s scope=%s)", snap.SessionID, store.StatePath(), snap.Phase, snap.Repository, effectiveScope(snap.ScopePath))

	set := setFromSnapshot(snap)
	ws := workspace.NewGitManager(workspace.GitConfig{Repository: snap.Repository})
	ws.Restore(set)

	// Validate worktrees before starting any process: a missing worktree must
	// fail loudly rather than half-start a session.
	if err := recovery.Validate(ctx, ws, snap); err != nil {
		return fmt.Errorf("cannot resume session %s: %w", snap.SessionID, err)
	}

	state := project.NewState()
	result, err := recovery.ReconcileAndApply(ctx, ws, state, snap)
	if err != nil {
		return fmt.Errorf("reconcile session %s: %w", snap.SessionID, err)
	}

	// Persist the reconciled checkpoint before starting anything, so a crash
	// during startup cannot resurrect revoked signatures or a duplicate merge.
	reconciled := result.Snapshot
	reconciled.DuoVersion = version
	if err := store.Save(reconciled); err != nil {
		return fmt.Errorf("persist reconciled session state: %w", err)
	}

	revoked := make([]string, 0, len(result.Report.Revoked))
	for _, agent := range result.Report.Revoked {
		revoked = append(revoked, string(agent))
	}
	journal.Record("session_resume", map[string]any{
		"sessionId":            reconciled.SessionID,
		"phase":                string(result.Report.Phase),
		"revoked":              revoked,
		"integrationRecovered": result.Report.IntegrationRecovered,
		"duoVersion":           version,
	})
	logger.Printf("recovered: %s", result.Report.String())

	// If both agents already approved INTEGRATE, the agent phase is over: the
	// only remaining step is an idempotent hand-off to the user's repository.
	// Reconcile it before starting any Pi process.
	outcome, stop, err := reconcileDelivery(ctx, store, ws, journal, reconciled)
	if err != nil {
		return fmt.Errorf("deliver session %s: %w", snap.SessionID, err)
	}
	if stop {
		logger.Printf("session %s: phase=%s delivery=%s", outcome.Snapshot.SessionID, outcome.Snapshot.Phase, outcome.Snapshot.Delivery.Status)
		if outcome.Applied {
			printDeliverySuccess(outcome)
		} else {
			printDeliveryPending(outcome)
		}
		return nil
	}

	piSessions, err := piSessionIDs(reconciled.PiSessions)
	if err != nil {
		return err
	}

	createdAt := reconciled.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	r := &runtime{
		cfg:        cfg,
		repoID:     repoID,
		sessionID:  reconciled.SessionID,
		createdAt:  createdAt,
		state:      state,
		ws:         ws,
		set:        set,
		store:      store,
		journal:    journal,
		logger:     logger,
		piSessions: piSessions,
		integration: workspace.IntegrationResult{
			AustinBranch: set.Austin.Branch,
			AustinPath:   set.Austin.Path,
			Head:         reconciled.Integration.Head,
			MergedTony:   reconciled.Integration.MergedTony,
			Conflicted:   reconciled.Integration.Conflicted,
		},
		delivery: reconciled.Delivery,
		resume:   true,
	}

	// Record the Pi identities actually in use, including any newly generated
	// one, so the next resume reuses exactly these conversations.
	if err := r.store.Save(r.composeSnapshot(nil)); err != nil {
		return err
	}
	return r.serve(ctx)
}

// selectSession resolves which persisted session to resume: an explicit id, the
// single unfinished one, or an error listing the choices. Unreadable sessions
// are reported rather than silently skipped.
func selectSession(baseDir, repoID, requested, root string) (sessionstore.Summary, error) {
	if requested != "" {
		store, err := sessionstore.Open(baseDir, repoID, requested)
		if err != nil {
			return sessionstore.Summary{}, err
		}
		snap, err := store.Load()
		if err != nil {
			return sessionstore.Summary{}, err
		}
		return sessionstore.Summary{SessionID: requested, Dir: store.Dir(), Snapshot: snap}, nil
	}

	list, err := sessionstore.List(baseDir, repoID)
	if err != nil {
		return sessionstore.Summary{}, err
	}

	var candidates, broken []sessionstore.Summary
	for _, item := range list {
		switch {
		case item.Err != nil:
			broken = append(broken, item)
		case item.Unfinished():
			candidates = append(candidates, item)
		}
	}

	switch len(candidates) {
	case 1:
		return candidates[0], nil
	case 0:
		if len(broken) > 0 {
			return sessionstore.Summary{}, fmt.Errorf(
				"no resumable Duo session for %s; %d stored session(s) could not be read:\n%s",
				root, len(broken), formatBroken(broken),
			)
		}
		return sessionstore.Summary{}, fmt.Errorf("no unfinished Duo session for %s; run `duo` to start a new one", root)
	default:
		return sessionstore.Summary{}, fmt.Errorf(
			"multiple unfinished Duo sessions for %s:\n%s\nrun `duo --resume <session-id>` to pick one",
			root, formatCandidates(candidates),
		)
	}
}

func formatCandidates(items []sessionstore.Summary) string {
	var b strings.Builder
	for i, item := range items {
		if i == 10 {
			fmt.Fprintf(&b, "  … and %d more\n", len(items)-i)
			break
		}
		fmt.Fprintf(&b, "  %s  phase=%s  scope=%s  updated=%s\n",
			item.SessionID, item.Snapshot.Phase, effectiveScope(item.Snapshot.ScopePath), item.Snapshot.UpdatedAt.Local().Format(time.RFC3339))
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatBroken(items []sessionstore.Summary) string {
	var b strings.Builder
	for i, item := range items {
		if i == 10 {
			fmt.Fprintf(&b, "  … and %d more\n", len(items)-i)
			break
		}
		fmt.Fprintf(&b, "  %s: %v\n", item.SessionID, item.Err)
	}
	return strings.TrimRight(b.String(), "\n")
}

// setFromSnapshot rebuilds the workspace set from persisted state without
// touching Git. Recovery validates it afterwards.
func setFromSnapshot(snap sessionstore.Snapshot) workspace.Set {
	set := workspace.Set{
		Repository: snap.Repository,
		BaseBranch: snap.BaseBranch,
		BaseCommit: snap.BaseCommit,
		Session:    snap.SessionID,
		ScopePath:  effectiveScope(snap.ScopePath),
	}
	if wt, ok := snap.Worktree(protocol.Austin); ok {
		set.Austin = workspace.Worktree{Agent: protocol.Austin, Path: wt.Path, Branch: wt.Branch}
	}
	if wt, ok := snap.Worktree(protocol.Tony); ok {
		set.Tony = workspace.Worktree{Agent: protocol.Tony, Path: wt.Path, Branch: wt.Branch}
	}
	if set.Austin.Path != "" {
		set.Root = filepath.Dir(set.Austin.Path)
	}
	return set
}

func effectiveScope(scope string) string {
	if strings.TrimSpace(scope) == "" {
		return "."
	}
	return scope
}

// piSessionIDs returns the stable Pi session identity for each agent, reusing
// persisted ids and generating any that are missing. Austin and Tony always get
// different ids: they are separate conversations, never a shared one.
func piSessionIDs(existing map[protocol.AgentID]string) (map[protocol.AgentID]string, error) {
	austin := strings.TrimSpace(existing[protocol.Austin])
	tony := strings.TrimSpace(existing[protocol.Tony])

	if austin == "" {
		id, err := sessionstore.NewUUID()
		if err != nil {
			return nil, err
		}
		austin = id
	}
	if tony == "" || tony == austin {
		id, err := sessionstore.NewUUID()
		if err != nil {
			return nil, err
		}
		tony = id
	}

	return map[protocol.AgentID]string{
		protocol.Austin: austin,
		protocol.Tony:   tony,
	}, nil
}
