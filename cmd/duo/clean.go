package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

type cleanArgs struct {
	allRepos  bool
	all       bool
	force     bool
	dryRun    bool
	sessionID string
	help      bool
}

func parseCleanArgs(args []string) (cleanArgs, error) {
	var out cleanArgs
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "":
		case helpRequested(arg):
			out.help = true
			return out, nil
		case arg == "--all" || arg == "-a":
			out.all = true
		case arg == "--all-repos":
			out.allRepos = true
		case arg == "--force" || arg == "-f":
			out.force = true
		case arg == "--dry-run" || arg == "-n":
			out.dryRun = true
		case strings.HasPrefix(arg, "-"):
			return out, fmt.Errorf("unknown flag %q (usage: %s)", arg, commandUsage("clean"))
		default:
			if out.sessionID != "" {
				return out, fmt.Errorf("unexpected extra argument %q", arg)
			}
			out.sessionID = arg
		}
	}
	return out, nil
}

func runClean(ctx context.Context, args []string) error {
	parsed, err := parseCleanArgs(args)
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

	if parsed.allRepos {
		return cleanAllRepos(ctx, baseDir, parsed)
	}

	repoPath := strings.TrimSpace(os.Getenv("DUO_REPO"))
	if repoPath == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		repoPath = cwd
	}
	if abs, err := filepath.Abs(repoPath); err == nil {
		repoPath = abs
	}

	root, err := workspace.FindRoot(ctx, repoPath)
	if err != nil {
		// If not inside a git repo and specific session requested, search across repos
		if parsed.sessionID != "" {
			return cleanSessionAcrossRepos(ctx, baseDir, parsed.sessionID, parsed)
		}
		return err
	}
	repoID := sessionstore.RepoID(root)

	return cleanRepoSessions(ctx, baseDir, repoID, root, parsed)
}

func cleanRepoSessions(ctx context.Context, baseDir, repoID, root string, opts cleanArgs) error {
	list, err := sessionstore.List(baseDir, repoID)
	if err != nil {
		return err
	}

	var targets []sessionstore.Summary
	if opts.sessionID != "" {
		found := false
		for _, item := range list {
			if item.SessionID == opts.sessionID {
				targets = append(targets, item)
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("session %q not found for %s", opts.sessionID, root)
		}
	} else {
		targets = list
	}

	cleaned, skipped, err := cleanSummaries(ctx, baseDir, repoID, targets, opts)
	if err != nil {
		return err
	}

	if opts.dryRun {
		fmt.Printf("Dry-run: %d session(s) would be cleaned, %d skipped.\n", cleaned, skipped)
	} else {
		fmt.Printf("Done: cleaned %d session(s), skipped %d.\n", cleaned, skipped)
	}
	return nil
}

func cleanAllRepos(ctx context.Context, baseDir string, opts cleanArgs) error {
	repos, err := sessionstore.ListRepos(baseDir)
	if err != nil {
		return err
	}

	totalCleaned := 0
	totalSkipped := 0
	for _, repoID := range repos {
		list, err := sessionstore.List(baseDir, repoID)
		if err != nil {
			continue
		}
		cleaned, skipped, err := cleanSummaries(ctx, baseDir, repoID, list, opts)
		if err != nil {
			fmt.Printf("Warning: error cleaning repo %s: %v\n", repoID, err)
		}
		totalCleaned += cleaned
		totalSkipped += skipped
	}

	if opts.dryRun {
		fmt.Printf("Dry-run: %d session(s) would be cleaned across all repos, %d skipped.\n", totalCleaned, totalSkipped)
	} else {
		fmt.Printf("Done: cleaned %d session(s) across all repos, skipped %d.\n", totalCleaned, totalSkipped)
	}
	return nil
}

func cleanSessionAcrossRepos(ctx context.Context, baseDir, sessionID string, opts cleanArgs) error {
	repos, err := sessionstore.ListRepos(baseDir)
	if err != nil {
		return err
	}

	for _, repoID := range repos {
		list, err := sessionstore.List(baseDir, repoID)
		if err != nil {
			continue
		}
		for _, item := range list {
			if item.SessionID == sessionID {
				cleaned, _, err := cleanSummaries(ctx, baseDir, repoID, []sessionstore.Summary{item}, opts)
				if err != nil {
					return err
				}
				if cleaned > 0 {
					return nil
				}
				return fmt.Errorf("session %s was not cleaned", sessionID)
			}
		}
	}
	return fmt.Errorf("session %q not found in any repository", sessionID)
}

func cleanSummaries(ctx context.Context, baseDir, repoID string, items []sessionstore.Summary, opts cleanArgs) (int, int, error) {
	cleaned := 0
	skipped := 0

	for _, item := range items {
		// Eligibility check
		isExplicit := opts.sessionID == item.SessionID
		if !isExplicit && !opts.all && opts.sessionID == "" && !opts.force {
			// By default (no --all, no --force and no specific ID), only clean completed (DONE) sessions
			if item.Unfinished() {
				skipped++
				continue
			}
		}

		if item.Unfinished() && !opts.force && !isExplicit {
			fmt.Printf("  Skipping unfinished session %s (phase=%s); pass --force to clean\n", item.SessionID, item.Snapshot.Phase)
			skipped++
			continue
		}

		store, err := sessionstore.Open(baseDir, repoID, item.SessionID)
		if err != nil {
			// Corrupt or invalid session dir
			if opts.force || isExplicit {
				if opts.dryRun {
					fmt.Printf("  [dry-run] would remove corrupt session dir %s\n", item.Dir)
				} else {
					_ = os.RemoveAll(item.Dir)
					fmt.Printf("  Removed corrupt session dir %s\n", item.SessionID)
				}
				cleaned++
			} else {
				skipped++
			}
			continue
		}

		// Check if active
		lock, err := store.Lock()
		if err != nil {
			var lockedErr *sessionstore.LockedError
			if errors.As(err, &lockedErr) {
				fmt.Printf("  Skipping active session %s (%v)\n", item.SessionID, lockedErr)
			} else {
				fmt.Printf("  Skipping session %s: lock error: %v\n", item.SessionID, err)
			}
			skipped++
			continue
		}

		snap := item.Snapshot
		if opts.dryRun {
			lock.Release()
			fmt.Printf("  [dry-run] would clean session %s (mode=%s, phase=%s)\n", item.SessionID, snap.EffectiveMode(), snap.Phase)
			cleaned++
			continue
		}

		// Perform actual cleanup
		cleanSessionWorktrees(ctx, snap)

		lock.Release()
		if err := store.Remove(); err != nil {
			fmt.Printf("  Error removing session directory %s: %v\n", item.SessionID, err)
			skipped++
			continue
		}

		// Clean empty parent repo dir in sessions
		repoSessionDir := filepath.Dir(store.Dir())
		_ = os.Remove(repoSessionDir) // only removes if empty

		fmt.Printf("  Cleaned session %s (phase=%s)\n", item.SessionID, snap.Phase)
		cleaned++
	}

	return cleaned, skipped, nil
}

func cleanSessionWorktrees(ctx context.Context, snap sessionstore.Snapshot) {
	home, _ := os.UserHomeDir()
	duoWorktreesBase := filepath.Join(home, ".duo", "worktrees")

	// 1. If git repo exists, remove worktrees and temporary branches through Git
	if snap.Repository != "" {
		if _, err := os.Stat(snap.Repository); err == nil {
			for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
				if wt, ok := snap.Worktree(agent); ok && wt.Path != "" {
					_ = exec.CommandContext(ctx, "git", "-C", snap.Repository, "worktree", "remove", "--force", wt.Path).Run()
				}
			}
			_ = exec.CommandContext(ctx, "git", "-C", snap.Repository, "worktree", "prune").Run()
			if snap.SessionID != "" {
				austinBranch := fmt.Sprintf("duo/%s/austin", snap.SessionID)
				tonyBranch := fmt.Sprintf("duo/%s/tony", snap.SessionID)
				_ = exec.CommandContext(ctx, "git", "-C", snap.Repository, "branch", "-D", austinBranch, tonyBranch).Run()
			}
		}
	}

	// 2. Remove worktree directory on disk if it still exists and is under ~/.duo/worktrees
	var sessionWorktreeDir string
	if wt, ok := snap.Worktree(protocol.Austin); ok && wt.Path != "" {
		sessionWorktreeDir = filepath.Dir(wt.Path)
	} else if wt, ok := snap.Worktree(protocol.Tony); ok && wt.Path != "" {
		sessionWorktreeDir = filepath.Dir(wt.Path)
	}

	if sessionWorktreeDir != "" && isSafeWorktreeSubdir(sessionWorktreeDir, duoWorktreesBase) {
		_ = os.RemoveAll(sessionWorktreeDir)
		// Try to clean empty parent repository worktree dir
		parentRepoWorktreeDir := filepath.Dir(sessionWorktreeDir)
		if isSafeWorktreeSubdir(parentRepoWorktreeDir, duoWorktreesBase) {
			_ = os.Remove(parentRepoWorktreeDir)
		}
	}
}

func isSafeWorktreeSubdir(path, base string) bool {
	if path == "" || base == "" {
		return false
	}
	cleanPath := filepath.Clean(path)
	cleanBase := filepath.Clean(base)
	if cleanPath == cleanBase {
		return false // Never delete base directory itself
	}
	rel, err := filepath.Rel(cleanBase, cleanPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}
