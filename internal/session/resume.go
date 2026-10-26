package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	summary, broken, err := SelectSession(s.baseDir, s.repoID, s.opts.ResumeSession, s.root)
	if err != nil {
		return Result{}, err
	}
	snap := summary.Snapshot

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
	if len(broken) > 0 {
		s.logger.Printf("warning: unreadable stored Duo sessions:\n%s", FormatBroken(broken))
	}
	s.journal = store.OpenEvents()
	s.sessionID = snap.SessionID
	// The title is carried across a resume like any other part of the record, and
	// it is what keeps a resumed session from being named a second time.
	s.title = snap.Title
	s.history = s.journal.TUIEntries()
	s.logger.Printf("resuming Duo session %s from %s (persisted mode %s phase %s repository=%s scope=%s)", snap.SessionID, store.StatePath(), snap.EffectiveMode(), snap.Phase, snap.Repository, EffectiveScope(snap.ScopePath))

	// Resume restores the persisted current mode. An explicit --mode that
	// disagrees is an error; an ambient DUO_MODE is only a warning.
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
	reconciled.Title = snap.Title
	reconciled.PauseState = snap.PauseState
	// The cost ledger is carried over verbatim: a resumed session must keep the cost
	// of the work it already did, and reconciliation has no opinion about it.
	reconciled.Usage = snap.Usage
	if snap.PausedAgents != nil {
		reconciled.PausedAgents = map[protocol.AgentID]bool{protocol.Austin: snap.PausedAgents[protocol.Austin], protocol.Tony: snap.PausedAgents[protocol.Tony]}
	}
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
		"phase":                reconciled.Phase,
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

	// ReconcileDelivery may have changed the checkpoint even when the session
	// continues: it lands a step whose landing crashed before the queue moved, and it
	// can complete a delivery. The live session has to run on the reconciled state,
	// because the final composeSnapshot writes the state and the delivery record back
	// out — composing the pre-reconcile ones would undo the landing and leave the
	// session sitting on a step it already delivered.
	reconciled = outcome.Snapshot
	if err := state.Restore(recovery.ProjectSnapshot(reconciled)); err != nil {
		return Result{}, fmt.Errorf("restore reconciled session %s: %w", snap.SessionID, err)
	}

	// Each agent's driver state is opaque to Duo Core: whatever the plugin stored
	// last time is handed back untouched, and the plugin decides what it means. A
	// session with none starts a fresh conversation, which is the plugin's cue to
	// mint an identity or to leave the agent to.
	driverState := DriverSeeds(reconciled)

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
				// The manifest names the restored driver's binary. Drop a command only
				// when it came from the default driver's fallback; explicit commands
				// still reach the plugin as operator overrides.
				s.opts.restoreAgentDriver(ag, drv)
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
	s.set = SetFromSnapshot(reconciled)
	s.state = state
	s.mode = mode
	s.sessionID = reconciled.SessionID
	s.createdAt = createdAt
	s.driverState = driverState
	s.integration = workspace.IntegrationResult{
		AustinBranch: set.Austin.Branch,
		AustinPath:   set.Austin.Path,
		Head:         reconciled.Integration.Head,
		MergedTony:   reconciled.Integration.MergedTony,
		Conflicted:   reconciled.Integration.Conflicted,
	}
	s.delivery = reconciled.Delivery
	s.usage = reconciled.Usage
	s.pauseState = PauseState(reconciled.PauseState)
	s.pausedAgents = map[protocol.AgentID]bool{protocol.Austin: reconciled.PausedAgents[protocol.Austin], protocol.Tony: reconciled.PausedAgents[protocol.Tony]}
	s.resume = true

	// Record the driver identities actually in use, including any newly generated
	// one, so the next resume reuses exactly these conversations.
	if err := s.store.Save(s.composeSnapshot(nil)); err != nil {
		return Result{}, err
	}
	return s.launch(ctx)
}

// DriverSeeds returns the opaque driver state to replay when each agent starts.
func DriverSeeds(snap sessionstore.Snapshot) map[protocol.AgentID]sessionstore.DriverState {
	driverState := make(map[protocol.AgentID]sessionstore.DriverState, len(snap.DriverStates))
	for agent, state := range snap.DriverStates {
		if len(state.State) != 0 {
			driverState[agent] = state
		}
	}
	return driverState
}

// Sessions lists what this repository can resume right now, newest first. The
// dashboard's /sessions picker reads this instead of walking the store itself, so
// "resumable" means the same thing there and in `duo resume`.
func (s *Service) Sessions() []sessionstore.Summary {
	s.mu.RLock()
	baseDir, repoID, current := s.baseDir, s.repoID, s.sessionID
	s.mu.RUnlock()
	if baseDir == "" || repoID == "" {
		return nil
	}
	list, err := sessionstore.List(baseDir, repoID)
	if err != nil {
		return nil
	}
	return resumeCandidates(list, current)
}

// resumeCandidates keeps the sessions a human may switch to: unfinished, readable
// and not owned by a live Duo process. The open session is excluded because
// "resuming" it would only close it, and a locked one is excluded because the
// resume that followed would fail after this session was already closed.
func resumeCandidates(list []sessionstore.Summary, current string) []sessionstore.Summary {
	out := make([]sessionstore.Summary, 0, len(list))
	for _, item := range list {
		if item.SessionID == current || item.Err != nil || !item.Unfinished() {
			continue
		}
		if sessionstore.SessionActive(item.Dir) {
			continue
		}
		out = append(out, item)
	}
	return out
}

// SelectSession resolves which persisted session to resume: an explicit id, the
// single stored session, or an error listing the choices. Completed sessions may
// be reopened for follow-up work. Unreadable sessions are reported rather than
// silently skipped.
func SelectSession(baseDir, repoID, requested, root string) (sessionstore.Summary, []sessionstore.Summary, error) {
	if requested != "" {
		store, err := sessionstore.Open(baseDir, repoID, requested)
		if err != nil {
			return sessionstore.Summary{}, nil, err
		}
		snap, err := store.Load()
		if err != nil {
			return sessionstore.Summary{}, nil, err
		}
		return sessionstore.Summary{SessionID: requested, Dir: store.Dir(), Snapshot: snap}, nil, nil
	}

	list, err := sessionstore.List(baseDir, repoID)
	if err != nil {
		return sessionstore.Summary{}, nil, err
	}

	var candidates, broken []sessionstore.Summary
	for _, item := range list {
		switch {
		case item.Err != nil:
			broken = append(broken, item)
		case item.Snapshot.Phase != "":
			candidates = append(candidates, item)
		}
	}

	switch len(candidates) {
	case 1:
		return candidates[0], broken, nil
	case 0:
		if len(broken) > 0 {
			message := fmt.Sprintf(
				"no resumable Duo session for %s; %d stored session(s) could not be read:\n%s",
				root, len(broken), FormatBroken(broken),
			)
			if hint := otherCheckoutHint(baseDir, repoID, root); hint != "" {
				message += "\n" + hint
			}
			return sessionstore.Summary{}, nil, errors.New(message)
		}
		message := fmt.Sprintf("no Duo session for %s; run `duo` to start a new one", root)
		if hint := otherCheckoutHint(baseDir, repoID, root); hint != "" {
			message += "\n" + hint
		}
		return sessionstore.Summary{}, nil, errors.New(message)
	default:
		message := fmt.Sprintf("multiple Duo sessions for %s:\n%s", root, FormatCandidates(candidates))
		if len(broken) > 0 {
			message += fmt.Sprintf("\n%d stored session(s) could not be read:\n%s", len(broken), FormatBroken(broken))
		}
		return sessionstore.Summary{}, nil, fmt.Errorf(
			"%s\nrun `duo resume <session-id>` to pick one",
			message,
		)
	}
}

func otherCheckoutHint(baseDir, repoID, root string) string {
	commonDir := gitCommonDir(root)
	if commonDir == "" {
		return ""
	}
	repos, err := sessionstore.ListRepos(baseDir)
	if err != nil {
		return ""
	}
	for _, otherID := range repos {
		if otherID == repoID {
			continue
		}
		items, err := sessionstore.List(baseDir, otherID)
		if err != nil {
			continue
		}
		for _, item := range items {
			if item.Err != nil || !item.Unfinished() || item.Snapshot.Repository == "" {
				continue
			}
			if _, err := os.Stat(item.Snapshot.Repository); err != nil {
				continue
			}
			if gitCommonDir(item.Snapshot.Repository) == commonDir {
				return fmt.Sprintf("A Duo session for this Git repository was found under checkout %q; run `duo resume` from that checkout", item.Snapshot.Repository)
			}
		}
	}
	return ""
}

func gitCommonDir(repository string) string {
	repository = strings.TrimSpace(repository)
	if repository == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", repository, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return ""
	}
	path := strings.TrimSpace(string(out))
	if !filepath.IsAbs(path) {
		path = filepath.Join(repository, path)
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}

// FormatCandidates renders a session list for an error message.
func FormatCandidates(items []sessionstore.Summary) string {
	var b strings.Builder
	for i, item := range items {
		if i == 10 {
			fmt.Fprintf(&b, "  … and %d more\n", len(items)-i)
			break
		}
		status := "unfinished"
		if item.Snapshot.Phase == string(project.PhaseDone) {
			status = "completed"
		}
		fmt.Fprintf(&b, "  %s  status=%s  phase=%s  scope=%s  updated=%s\n",
			item.SessionID, status, item.Snapshot.Phase, EffectiveScope(item.Snapshot.ScopePath), item.Snapshot.UpdatedAt.Local().Format(time.RFC3339))
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
// Mode is persisted per session, so resume always uses the mode in force when
// the snapshot was written: an explicit requested mode that disagrees is an
// operator error, while an ambient one is only a warning, so an exported
// environment variable can never break resume.
func ResumeMode(modeRaw string, modeExplicit bool, persisted project.Mode, sessionID, ambient string, warn func(string)) (project.Mode, error) {
	if modeExplicit {
		mode, err := project.ParseMode(modeRaw)
		if err != nil {
			return "", err
		}
		if mode != persisted {
			return "", fmt.Errorf("session %s is persisted as %s; --mode %s cannot be applied on resume", sessionID, persisted, mode)
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
