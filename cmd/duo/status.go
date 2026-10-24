package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/recovery"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

type statusArgs struct {
	sessionID string
	json      bool
	help      bool
}

func parseStatusArgs(args []string) (statusArgs, error) {
	var out statusArgs
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "":
		case helpRequested(arg):
			out.help = true
			return out, nil
		case arg == "--json":
			out.json = true
		case strings.HasPrefix(arg, "-"):
			return statusArgs{}, fmt.Errorf("unknown flag %q (usage: %s)", arg, commandUsage("status"))
		default:
			if out.sessionID != "" {
				return statusArgs{}, fmt.Errorf("unexpected extra argument %q", arg)
			}
			out.sessionID = arg
		}
	}
	return out, nil
}

// statusAgent is one agent's Git evidence. It is what duo_status reports per
// agent, so a script reading --json sees the same HEAD, dirtiness and ahead count.
type statusAgent struct {
	Branch string `json:"branch"`
	Head   string `json:"head"`
	Dirty  bool   `json:"dirty"`
	Ahead  int    `json:"ahead"`
	Path   string `json:"path"`
	Error  string `json:"error,omitempty"`
}

// statusWorkspace is the recorded workspace, the second part of duo_status.
type statusWorkspace struct {
	Repository string `json:"repository"`
	Scope      string `json:"scope"`
	BaseBranch string `json:"baseBranch"`
	BaseCommit string `json:"baseCommit"`
}

// statusReport is `duo status --json`. It is built from the same three sources
// the coordinator's status answer uses — the project snapshot, the recorded
// workspace and each agent's Git status — so the CLI cannot invent a second
// account of the session. StatusText carries that answer verbatim.
type statusReport struct {
	SessionID    string                           `json:"sessionId"`
	Phase        string                           `json:"phase"`
	Mode         string                           `json:"mode"`
	PlanVersion  int                              `json:"planVersion"`
	Plan         string                           `json:"plan"`
	Step         int                              `json:"step,omitempty"`
	Steps        project.Steps                    `json:"steps,omitempty"`
	Ready        map[protocol.AgentID]bool        `json:"ready"`
	Notes        map[protocol.AgentID]string      `json:"notes,omitempty"`
	Evidence     map[protocol.AgentID]string      `json:"evidence,omitempty"`
	Verification project.Verification             `json:"verification"`
	Workspace    statusWorkspace                  `json:"workspace"`
	Agents       map[protocol.AgentID]statusAgent `json:"agents"`
	StatusText   string                           `json:"statusText"`
}

// runStatus implements `duo status [session-id] [--json]`. It reads the stored
// session and Git, never a frontend: the same authority duo_status answers from.
func runStatus(ctx context.Context, args []string) error {
	parsed, err := parseStatusArgs(args)
	if err != nil {
		return err
	}
	if parsed.help {
		printUsage()
		return nil
	}

	root, err := applyRoot(ctx)
	if err != nil {
		return err
	}
	baseDir, err := sessionstore.DefaultBaseDir()
	if err != nil {
		return err
	}
	repoID := sessionstore.RepoID(root)

	summary, err := selectStatusSession(baseDir, repoID, parsed.sessionID)
	if err != nil {
		return err
	}
	report, err := buildStatusReport(ctx, summary.Snapshot)
	if err != nil {
		return err
	}

	if parsed.json {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	fmt.Println(report.StatusText)
	return nil
}

// selectStatusSession picks the session to report: an explicit id, or the most
// recently updated session for this repository.
func selectStatusSession(baseDir, repoID, requested string) (sessionstore.Summary, error) {
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
	for _, item := range list {
		if item.Err != nil {
			continue
		}
		return item, nil
	}
	if len(list) > 0 {
		return sessionstore.Summary{}, fmt.Errorf("no readable Duo session for %s:\n%s", repoID, formatBroken(list))
	}
	return sessionstore.Summary{}, fmt.Errorf("no Duo session for %s", repoID)
}

// buildStatusReport composes the three parts of a status answer. The project part
// comes from the persisted snapshot through the same projection the coordinator
// restores, so phase, signatures and the queue cannot drift from what duo_status
// reports.
func buildStatusReport(ctx context.Context, snap sessionstore.Snapshot) (statusReport, error) {
	state := recovery.ProjectSnapshot(snap)
	set := setFromSnapshot(snap)

	report := statusReport{
		SessionID:    snap.SessionID,
		Phase:        string(state.Phase),
		Mode:         string(state.EffectiveMode()),
		PlanVersion:  state.PlanVersion,
		Plan:         state.Plan,
		Step:         state.Step,
		Steps:        state.Steps,
		Ready:        state.Ready,
		Notes:        state.Notes,
		Evidence:     state.Evidence,
		Verification: state.Verification,
		Workspace: statusWorkspace{
			Repository: set.Repository,
			Scope:      effectiveScope(set.ScopePath),
			BaseBranch: set.BaseBranch,
			BaseCommit: set.BaseCommit,
		},
		Agents: map[protocol.AgentID]statusAgent{},
	}

	ws := workspace.NewGitManager(workspace.GitConfig{Repository: set.Repository})
	ws.Restore(set)

	parts := []string{state.String(), set.String()}
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		entry := statusAgent{}
		if wt, ok := set.For(agent); ok {
			entry.Branch, entry.Path = wt.Branch, wt.Path
		}
		status, err := ws.Status(ctx, agent)
		if err != nil {
			// A removed worktree is a normal end state after delivery, so the
			// reason is reported instead of failing the whole status.
			entry.Error = err.Error()
			report.Agents[agent] = entry
			continue
		}
		entry.Branch, entry.Path = status.Branch, status.Path
		entry.Head, entry.Dirty, entry.Ahead = status.Head, status.Dirty, status.Ahead
		report.Agents[agent] = entry
		parts = append(parts, status.String())
	}
	report.StatusText = strings.Join(parts, "\n")
	return report, nil
}

// effectiveScope renders an empty scope the way the workspace answer does.
func effectiveScope(scope string) string {
	if strings.TrimSpace(scope) == "" {
		return "."
	}
	return scope
}
