package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/recovery"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

// startResume reloads a persisted session, proves it is still valid against Git,
// revokes anything that is no longer true, and only then starts anything.
func (s *Service) startResume(ctx context.Context) (Result, error) {
	summary, err := SelectSession(s.baseDir, s.repoID, s.opts.ResumeSession, s.root)
	if err != nil {
		return Result{}, err
	}
	snap := summary.Snapshot

	if snap.Phase == string(project.PhaseDone) {
		return Result{}, fmt.Errorf("Duo session %s is already DONE; start a new session with `duo`", snap.SessionID)
	}
	if strings.TrimSpace(snap.Repository) != "" && !workspace.SamePath(snap.Repository, s.root) {
		return Result{}, fmt.Errorf("session %s belongs to %s, not %s", snap.SessionID, snap.Repository, s.root)
	}

	store, err := sessionstore.Open(s.baseDir, s.repoID, snap.SessionID)
	if err != nil {
		return Result{}, err
	}
	lock, err := store.Lock()
	if err != nil {
		return Result{}, err
	}

	s.store, s.lock = store, lock
	s.logger = store.OpenLog()
	s.journal = store.OpenEvents()
	s.sessionID = snap.SessionID
	s.history = s.journal.TUIEntries()
	s.logger.Printf("resuming Duo session %s from %s (persisted mode %s phase %s repository=%s scope=%s)", snap.SessionID, store.StatePath(), snap.EffectiveMode(), snap.Phase, snap.Repository, EffectiveScope(snap.ScopePath))

	// Mode is fixed per session: the persisted mode always wins. An explicit
	// --mode that disagrees is an error; an ambient DUO_MODE is only a warning.
	mode, err := ResumeMode(s.opts.ModeRaw, s.opts.ModeExplicit, snap.EffectiveMode(), snap.SessionID, os.Getenv("DUO_MODE"), func(msg string) {
		s.logger.Printf("warning: %s", msg)
	})
	if err != nil {
		return Result{}, err
	}

	set := SetFromSnapshot(snap)
	ws := workspace.NewGitManager(workspace.GitConfig{Repository: snap.Repository})
	ws.Restore(set)

	// Validate worktrees before starting any process: a missing worktree must
	// fail loudly rather than half-start a session.
	if err := recovery.Validate(ctx, ws, snap); err != nil {
		return Result{}, fmt.Errorf("cannot resume session %s: %w", snap.SessionID, err)
	}

	state := project.NewStateFor(mode)
	result, err := recovery.ReconcileAndApply(ctx, ws, state, snap)
	if err != nil {
		return Result{}, fmt.Errorf("reconcile session %s: %w", snap.SessionID, err)
	}

	// Persist the reconciled checkpoint before starting anything, so a crash
	// during startup cannot resurrect revoked signatures or a duplicate merge.
	reconciled := result.Snapshot
	reconciled.DuoVersion = versionString()
	if err := store.Save(reconciled); err != nil {
		return Result{}, fmt.Errorf("persist reconciled session state: %w", err)
	}

	revoked := make([]string, 0, len(result.Report.Revoked))
	for _, agent := range result.Report.Revoked {
		revoked = append(revoked, string(agent))
	}
	s.journal.Record("session_resume", map[string]any{
		"sessionId":            reconciled.SessionID,
		"phase":                string(result.Report.Phase),
		"revoked":              revoked,
		"integrationRecovered": result.Report.IntegrationRecovered,
		"duoVersion":           versionString(),
	})
	s.logger.Printf("recovered: %s", result.Report.String())

	// If both agents already approved INTEGRATE, the agent phase is over: the only
	// remaining step is an idempotent hand-off to the user's repository. Reconcile
	// it before starting any agent process.
	outcome, stop, err := ReconcileDelivery(ctx, store, ws, s.journal, reconciled)
	if err != nil {
		return Result{}, fmt.Errorf("deliver session %s: %w", snap.SessionID, err)
	}
	if stop {
		s.logger.Printf("session %s: phase=%s delivery=%s", outcome.Snapshot.SessionID, outcome.Snapshot.Phase, outcome.Snapshot.Delivery.Status)
		s.sessionID = outcome.Snapshot.SessionID
		s.set = SetFromSnapshot(outcome.Snapshot)
		return Result{
			SessionID: outcome.Snapshot.SessionID,
			Phase:     project.Phase(outcome.Snapshot.Phase),
			Finished:  true,
			Outcome:   outcome,
		}, nil
	}

	// Each agent's driver state is opaque to Duo Core: whatever the plugin stored
	// last time is handed back untouched, and the plugin decides what it means. A
	// session with none starts a fresh conversation, which is the plugin's cue to
	// mint an identity or to leave the agent to.
	driverState, piSessions := DriverSeeds(reconciled)

	createdAt := reconciled.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	// Restore drivers and models from the snapshot unless explicitly overridden.
	if len(reconciled.AgentDrivers) > 0 {
		if s.opts.AgentDrivers == nil {
			s.opts.AgentDrivers = make(map[protocol.AgentID]string)
		}
		for _, ag := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
			if drv := reconciled.AgentDrivers[ag]; drv != "" && !s.opts.AgentDriverExplicit[ag] {
				// The driver is restored, and nothing else: a restored driver's own
				// manifest names the binary it runs, so there is no command to rewrite
				// and no table here saying which name means which executable.
				s.opts.AgentDrivers[ag] = drv
			}
		}
	}
	if len(reconciled.AgentModels) > 0 {
		if s.opts.AgentModels == nil {
			s.opts.AgentModels = make(map[protocol.AgentID]string)
		}
		for _, ag := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
			if model := reconciled.AgentModels[ag]; model != "" {
				s.opts.AgentModels[ag] = model
			}
		}
	}
	_ = workspace.EnsureGitIgnore(snap.Repository)
	_ = workspace.SaveProjectConfig(snap.Repository, s.opts.AgentDriver(protocol.Austin), s.opts.AgentDrivers, s.opts.AgentModels)

	s.ws = ws
	s.set = set
	s.state = state
	s.mode = mode
	s.sessionID = reconciled.SessionID
	s.createdAt = createdAt
	s.driverState = driverState
	s.piSessions = piSessions
	s.integration = workspace.IntegrationResult{
		AustinBranch: set.Austin.Branch,
		AustinPath:   set.Austin.Path,
		Head:         reconciled.Integration.Head,
		MergedTony:   reconciled.Integration.MergedTony,
		Conflicted:   reconciled.Integration.Conflicted,
	}
	s.delivery = reconciled.Delivery
	s.resume = true

	// Record the driver identities actually in use, including any newly generated
	// one, so the next resume reuses exactly these conversations.
	if err := s.store.Save(s.composeSnapshot(nil)); err != nil {
		return Result{}, err
	}
	return s.launch(ctx)
}

// DriverSeeds turns a reconciled snapshot into the session's two identity maps:
// the authoritative plugin blobs and the bare downgrade mirror. Both are read as
// Load left them, so a v0.9.0 file has already been migrated by the time it
// reaches here. Only bare values enter the mirror — a `{`-prefixed value is a blob
// that a v0.9.0 binary could only misread as an id — and the mirror refills from
// the driver's SessionID() as soon as an agent launches.
func DriverSeeds(snap sessionstore.Snapshot) (map[protocol.AgentID]sessionstore.DriverState, map[protocol.AgentID]string) {
	driverState := make(map[protocol.AgentID]sessionstore.DriverState, len(snap.DriverStates))
	for agent, state := range snap.DriverStates {
		if len(state.State) != 0 {
			driverState[agent] = state
		}
	}
	piSessions := make(map[protocol.AgentID]string, len(snap.PiSessions))
	for agent, id := range snap.PiSessions {
		id = strings.TrimSpace(id)
		if id != "" && !strings.HasPrefix(id, "{") {
			piSessions[agent] = id
		}
	}
	return driverState, piSessions
}

// SelectSession resolves which persisted session to resume: an explicit id, the
// single unfinished one, or an error listing the choices. Unreadable sessions are
// reported rather than silently skipped.
func SelectSession(baseDir, repoID, requested, root string) (sessionstore.Summary, error) {
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
				root, len(broken), FormatBroken(broken),
			)
		}
		return sessionstore.Summary{}, fmt.Errorf("no unfinished Duo session for %s; run `duo` to start a new one", root)
	default:
		return sessionstore.Summary{}, fmt.Errorf(
			"multiple unfinished Duo sessions for %s:\n%s\nrun `duo --resume <session-id>` to pick one",
			root, FormatCandidates(candidates),
		)
	}
}

// FormatCandidates renders a session list for an error message.
func FormatCandidates(items []sessionstore.Summary) string {
	var b strings.Builder
	for i, item := range items {
		if i == 10 {
			fmt.Fprintf(&b, "  … and %d more\n", len(items)-i)
			break
		}
		fmt.Fprintf(&b, "  %s  phase=%s  scope=%s  updated=%s\n",
			item.SessionID, item.Snapshot.Phase, EffectiveScope(item.Snapshot.ScopePath), item.Snapshot.UpdatedAt.Local().Format(time.RFC3339))
	}
	return strings.TrimRight(b.String(), "\n")
}

// FormatBroken renders the sessions that could not be read.
func FormatBroken(items []sessionstore.Summary) string {
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

// SetFromSnapshot rebuilds the workspace set from persisted state without touching
// Git. Recovery validates it afterwards.
func SetFromSnapshot(snap sessionstore.Snapshot) workspace.Set {
	set := workspace.Set{
		Repository: snap.Repository,
		BaseBranch: snap.BaseBranch,
		BaseCommit: snap.BaseCommit,
		Session:    snap.SessionID,
		ScopePath:  EffectiveScope(snap.ScopePath),
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

// EffectiveScope normalises an empty scope to the repository root.
func EffectiveScope(scope string) string {
	if strings.TrimSpace(scope) == "" {
		return "."
	}
	return scope
}

// ResumeMode decides the mode of a resumed session.
//
// Mode is fixed per session, so the persisted mode always wins: an explicit
// requested mode that disagrees is an operator error, while an ambient one is only
// a warning, so an exported environment variable can never break resume.
func ResumeMode(modeRaw string, modeExplicit bool, persisted project.Mode, sessionID, ambient string, warn func(string)) (project.Mode, error) {
	if modeExplicit {
		mode, err := project.ParseMode(modeRaw)
		if err != nil {
			return "", err
		}
		if mode != persisted {
			return "", fmt.Errorf("session %s is a %s session; mode is fixed per session, so --mode %s cannot be applied", sessionID, persisted, mode)
		}
		return persisted, nil
	}
	if raw := strings.TrimSpace(ambient); raw != "" {
		mode, err := project.ParseMode(raw)
		switch {
		case err != nil:
			warn(fmt.Sprintf("ignoring invalid DUO_MODE %q while resuming: %v", raw, err))
		case mode != persisted:
			warn(fmt.Sprintf("ignoring DUO_MODE=%s while resuming: session %s is a %s session", mode, sessionID, persisted))
		}
	}
	return persisted, nil
}
