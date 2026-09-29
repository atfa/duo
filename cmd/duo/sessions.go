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
	all        bool
	repository string
}

func parseSessionsArgs(args []string) (sessionsArgs, error) {
	var out sessionsArgs
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "":
		case arg == "--all" || arg == "-a":
			out.all = true
		case strings.HasPrefix(arg, "-"):
			return out, fmt.Errorf("unknown flag %q (usage: duo sessions [--all] [repository])", arg)
		default:
			if out.repository != "" {
				return out, fmt.Errorf("unexpected extra argument %q", arg)
			}
			out.repository = arg
		}
	}
	return out, nil
}

func runSessions(ctx context.Context, args []string) error {
	parsed, err := parseSessionsArgs(args)
	if err != nil {
		return err
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

	return listRepoSessions(baseDir, repoID, root)
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

	fmt.Printf("  %-28s  %-5s  %-9s  %-10s  %-19s  %-9s  %s\n",
		"SESSION ID", "MODE", "PHASE", "BRANCH", "UPDATED", "WORKTREES", "STATUS")

	var unfinished, completed int
	for _, item := range list {
		if item.Err != nil {
			fmt.Printf("  %-28s  %-5s  %-9s  %-10s  %-19s  %-9s  %s\n",
				item.SessionID, "-", "ERR", "-", "-", "-", fmt.Sprintf("corrupt (%v)", item.Err))
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

		fmt.Printf("  %-28s  %-5s  %-9s  %-10s  %-19s  %-9s  %s\n",
			item.SessionID, mode, phase, branch, updated, wtStatus, status)
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

	fmt.Printf("  %-20s  %-28s  %-5s  %-9s  %-19s  %-9s  %s\n",
		"REPO ID", "SESSION ID", "MODE", "PHASE", "UPDATED", "WORKTREES", "STATUS")

	var totalSessions, totalCompleted int
	for _, repoID := range repos {
		list, err := sessionstore.List(baseDir, repoID)
		if err != nil {
			continue
		}
		for _, item := range list {
			totalSessions++
			if item.Err != nil {
				fmt.Printf("  %-20s  %-28s  %-5s  %-9s  %-19s  %-9s  %s\n",
					repoID, item.SessionID, "-", "ERR", "-", "-", fmt.Sprintf("corrupt (%v)", item.Err))
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

			fmt.Printf("  %-20s  %-28s  %-5s  %-9s  %-19s  %-9s  %s\n",
				repoID, item.SessionID, mode, phase, updated, wtStatus, status)
		}
	}

	fmt.Printf("\nTotal: %d session(s) across %d repository/repositories.\n", totalSessions, len(repos))
	if totalCompleted > 0 {
		fmt.Println("Run `duo clean --all-repos` to remove completed sessions across all repositories.")
	}
	return nil
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
	case snap.Delivery.Applied():
		return "delivered"
	case snap.NeedsDelivery():
		return "pending"
	case snap.Phase == "DONE":
		return "completed"
	case item.Unfinished():
		return "in-progress"
	default:
		return "finished"
	}
}
