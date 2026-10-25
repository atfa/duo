package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

type sessionsArgs struct {
	all           bool
	unfinishedIDs bool
	repository    string
	help          bool
}

func parseSessionsArgs(args []string) (sessionsArgs, error) {
	var out sessionsArgs
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "":
		case helpRequested(arg):
			out.help = true
			return out, nil
		case arg == "--all" || arg == "-a":
			out.all = true
		case arg == "--unfinished-ids":
			out.unfinishedIDs = true
		case strings.HasPrefix(arg, "-"):
			return out, fmt.Errorf("unknown flag %q (usage: %s)", arg, commandUsage("sessions"))
		default:
			if out.repository != "" {
				return out, fmt.Errorf("unexpected extra argument %q", arg)
			}
			out.repository = arg
		}
	}
	if out.all && out.unfinishedIDs {
		return out, fmt.Errorf("--all cannot be used with --unfinished-ids (usage: %s)", commandUsage("sessions"))
	}
	return out, nil
}

func runSessions(ctx context.Context, args []string) error {
	parsed, err := parseSessionsArgs(args)
	if err != nil {
		return err
	}
	if parsed.help {
		printUsage()
		return nil
	}

	baseDir, err := sessionstore.DefaultBaseDir()
	if err != nil {
		return err
	}

	if parsed.all {
		return listAllSessions(baseDir)
	}

	repoPath := parsed.repository
	if repoPath == "" {
		repoPath = strings.TrimSpace(os.Getenv("DUO_REPO"))
		if repoPath == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			repoPath = cwd
		}
	}
	if abs, err := filepath.Abs(repoPath); err == nil {
		repoPath = abs
	}

	root, err := workspace.FindRoot(ctx, repoPath)
	if err != nil {
		return err
	}
	repoID := sessionstore.RepoID(root)

	if parsed.unfinishedIDs {
		return listRepoUnfinishedSessionIDs(baseDir, repoID)
	}
	return listRepoSessions(baseDir, repoID, root)
}

func writeUnfinishedSessionIDs(list []sessionstore.Summary) {
	for _, item := range list {
		if item.Unfinished() && safeSessionIDLine(item.SessionID) && !sessionIsActive(item.Dir) {
			fmt.Println(item.SessionID)
		}
	}
}

// sessionIsActive reports whether a live Duo process owns this session, so a
// session it holds cannot be resumed by a new launcher. The probe itself lives in
// sessionstore, where the dashboard's resume list shares it: one definition of
// "resumable" for every frontend.
func sessionIsActive(sessionDir string) bool {
	return sessionstore.SessionActive(sessionDir)
}

func safeSessionIDLine(value string) bool {
	if value == "" || value == "." || value == ".." || strings.Trim(value, "-._") != value {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func listRepoUnfinishedSessionIDs(baseDir, repoID string) error {
	list, err := sessionstore.List(baseDir, repoID)
	if err != nil {
		return err
	}
	writeUnfinishedSessionIDs(list)
	return nil
}

func listRepoSessions(baseDir, repoID, root string) error {
	list, err := sessionstore.List(baseDir, repoID)
	if err != nil {
		return err
	}

	fmt.Printf("Duo sessions for %s (%s):\n", root, repoID)
	if len(list) == 0 {
		fmt.Println("  (no sessions found)")
		return nil
	}

	fmt.Printf("  %-28s  %-5s  %-9s  %-10s  %-19s  %-9s  %-14s  %-6s  %s\n",
		"SESSION ID", "MODE", "PHASE", "BRANCH", "UPDATED", "WORKTREES", "COST", "SCHEMA", "STATUS")

	var unfinished, completed int
	for _, item := range list {
		if item.Err != nil {
			fmt.Printf("  %-28s  %-5s  %-9s  %-10s  %-19s  %-9s  %-14s  %-6s  %s\n",
				item.SessionID, "-", "ERR", "-", "-", "-", "-", "-", fmt.Sprintf("corrupt (%v)", item.Err))
			continue
		}
		snap := item.Snapshot
		if item.Unfinished() {
			unfinished++
		} else {
			completed++
		}

		mode := string(snap.EffectiveMode())
		phase := snap.Phase
		branch := snap.BaseBranch
		if len(branch) > 10 {
			branch = branch[:10]
		}
		updated := snap.UpdatedAt.Local().Format("2006-01-02 15:04:05")
		wtStatus := worktreePresence(snap)
		status := sessionStatus(item)

		fmt.Printf("  %-28s  %-5s  %-9s  %-10s  %-19s  %-9s  %-14s  %-6s  %s\n",
			item.SessionID, mode, phase, branch, updated, wtStatus, sessionCost(item), schemaLabel(snap), status)
	}

	fmt.Printf("\nTotal: %d session(s) (%d unfinished, %d completed)\n", len(list), unfinished, completed)
	if completed > 0 {
		fmt.Println("Run `duo clean` to remove completed sessions and free worktree disk space.")
	}
	return nil
}

func listAllSessions(baseDir string) error {
	repos, err := sessionstore.ListRepos(baseDir)
	if err != nil {
		return err
	}

	fmt.Println("All Duo sessions:")
	if len(repos) == 0 {
		fmt.Println("  (no repositories found)")
		return nil
	}

	fmt.Printf("  %-20s  %-28s  %-5s  %-9s  %-19s  %-9s  %-14s  %-6s  %s\n",
		"REPO ID", "SESSION ID", "MODE", "PHASE", "UPDATED", "WORKTREES", "COST", "SCHEMA", "STATUS")

	var totalSessions, totalCompleted int
	for _, repoID := range repos {
		list, err := sessionstore.List(baseDir, repoID)
		if err != nil {
			continue
		}
		for _, item := range list {
			totalSessions++
			if item.Err != nil {
				fmt.Printf("  %-20s  %-28s  %-5s  %-9s  %-19s  %-9s  %-14s  %-6s  %s\n",
					repoID, item.SessionID, "-", "ERR", "-", "-", "-", "-", fmt.Sprintf("corrupt (%v)", item.Err))
				continue
			}
			snap := item.Snapshot
			if !item.Unfinished() {
				totalCompleted++
			}

			mode := string(snap.EffectiveMode())
			phase := snap.Phase
			updated := snap.UpdatedAt.Local().Format("2006-01-02 15:04:05")
			wtStatus := worktreePresence(snap)
			status := sessionStatus(item)

			fmt.Printf("  %-20s  %-28s  %-5s  %-9s  %-19s  %-9s  %-14s  %-6s  %s\n",
				repoID, item.SessionID, mode, phase, updated, wtStatus, sessionCost(item), schemaLabel(snap), status)
		}
	}

	fmt.Printf("\nTotal: %d session(s) across %d repository/repositories.\n", totalSessions, len(repos))
	if totalCompleted > 0 {
		fmt.Println("Run `duo clean --all-repos` to remove completed sessions across all repositories.")
	}
	return nil
}

// schemaLabel renders the snapshot schema this Duo read: the current version, or
// the migration applied to an older snapshot. A snapshot built in memory has no
// read version, so it renders as unknown.
func schemaLabel(snap sessionstore.Snapshot) string {
	from := snap.PersistedSchemaVersion
	switch {
	case from <= 0:
		return "-"
	case from == sessionstore.SchemaVersion:
		return fmt.Sprintf("%d", from)
	default:
		return fmt.Sprintf("%d→%d", from, sessionstore.SchemaVersion)
	}
}

func worktreePresence(snap sessionstore.Snapshot) string {
	austinExists := false
	tonyExists := false
	if wt, ok := snap.Worktree(protocol.Austin); ok && wt.Path != "" {
		if _, err := os.Stat(wt.Path); err == nil {
			austinExists = true
		}
	}
	if wt, ok := snap.Worktree(protocol.Tony); ok && wt.Path != "" {
		if _, err := os.Stat(wt.Path); err == nil {
			tonyExists = true
		}
	}

	switch {
	case austinExists && tonyExists:
		return "yes"
	case austinExists || tonyExists:
		return "partial"
	default:
		return "none"
	}
}

// sessionCost renders the ledger's session total for the table. A nil ledger
// means nothing was measured (a snapshot from before the ledger existed, or a
// driver that cannot measure), which is not "$0.00": it renders as "-".
func sessionCost(item sessionstore.Summary) string {
	if item.Snapshot.Usage == nil {
		return "-"
	}
	total := item.Snapshot.Usage.Total()
	return fmt.Sprintf("$%.2f · %dk tok", total.CostUSD, (total.TotalTokens+500)/1000)
}

func sessionStatus(item sessionstore.Summary) string {
	store := &sessionstore.Store{}
	// Test if lock file exists and is active
	lockPath := filepath.Join(item.Dir, "lock")
	if _, err := os.Stat(lockPath); err == nil {
		s, err := sessionstore.Open(filepath.Dir(filepath.Dir(item.Dir)), filepath.Base(filepath.Dir(item.Dir)), item.SessionID)
		if err == nil {
			store = s
			if l, err := store.Lock(); err == nil {
				_ = l.Release()
			} else {
				return "active"
			}
		}
	}

	snap := item.Snapshot
	switch {
	case snap.NeedsDelivery():
		// A STEP session whose last landing is recorded can still have a step that
		// was submitted afterwards: it is awaiting delivery, not delivered, and
		// `duo apply` finds it. Reading the record first hid exactly that session.
		return "pending"
	case snap.Delivery.Applied():
		return "delivered"
	case snap.Phase == "DONE":
		return "completed"
	case item.Unfinished():
		return "in-progress"
	default:
		return "finished"
	}
}
