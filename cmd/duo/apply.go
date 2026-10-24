package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

// errDeliveryPending signals that `duo apply` could not safely deliver and the
// user's repository was deliberately left untouched.
var errDeliveryPending = errors.New("delivery is pending; the original repository was not modified")

// errUsageRequested is returned by a parser that was asked for -h/--help/help.
// It is not a failure: the command prints the usage and exits 0.
var errUsageRequested = errors.New("usage requested")

// runApply implements `duo apply [session-id] [--branch <name>] [--keep]`. It is
// dispatched from main before interactive configuration, and never starts Austin
// or Tony.
//
// It reuses the exact same safety rules as automatic delivery: only a clean,
// recorded branch that can be fast-forwarded is moved. A pending delivery is
// never forced. `--branch` is the exception that moves nothing the user is
// looking at: it creates a branch at the verified artifact instead, for
// repositories whose base branch is protected.
func runApply(ctx context.Context, args []string) error {
	parsed, err := parseApplyArgs(args)
	if errors.Is(err, errUsageRequested) {
		printUsage()
		return nil
	}
	if err != nil {
		return err
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

	summary, err := selectDeliveryTarget(baseDir, repoID, parsed.sessionID, root)
	if err != nil {
		return err
	}
	if strings.TrimSpace(summary.Snapshot.Repository) != "" && !workspace.SamePath(summary.Snapshot.Repository, root) {
		return fmt.Errorf("session %s belongs to %s, not %s", summary.SessionID, summary.Snapshot.Repository, root)
	}

	store, err := sessionstore.Open(baseDir, repoID, summary.SessionID)
	if err != nil {
		return err
	}
	lock, err := store.Lock()
	if err != nil {
		return err
	}
	defer lock.Release()

	logger := store.OpenLog()
	logger.Printf("applying session %s to %s", summary.SessionID, root)

	ws := workspace.NewGitManager(workspace.GitConfig{Repository: summary.Snapshot.Repository})
	ws.Restore(setFromSnapshot(summary.Snapshot))

	var outcome deliveryOutcome
	if parsed.branch != "" {
		outcome, err = deliverToNewBranch(ctx, store, ws, summary.Snapshot, parsed.branch)
	} else {
		outcome, err = deliverAndPersist(ctx, store, ws, summary.Snapshot)
	}
	if err != nil {
		return err
	}
	if outcome.Applied {
		logger.Printf("delivered session %s: %s applied to %s@%s",
			summary.SessionID, outcome.Snapshot.Delivery.AppliedHead,
			outcome.Snapshot.Delivery.TargetRepo, outcome.Snapshot.Delivery.TargetBranch)
		printDeliverySuccess(outcome)
		if parsed.branch != "" && !parsed.keep {
			cleanupDeliveredWorktrees(ctx, ws)
		}
		return nil
	}

	logger.Printf("delivery still pending for session %s: %s", summary.SessionID, outcome.Reason)
	printDeliveryPending(outcome)
	return errDeliveryPending
}

// applyArgs is the parsed `duo apply` command line. The zero value is the only
// default: deliver by fast-forwarding the branch Duo started on.
type applyArgs struct {
	sessionID string
	branch    string
	keep      bool
}

func parseApplyArgs(args []string) (applyArgs, error) {
	var out applyArgs
	branchRequested := false
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "":
		case helpRequested(arg):
			return applyArgs{}, errUsageRequested
		case arg == "--keep":
			out.keep = true
		case arg == "--branch":
			if i+1 >= len(args) {
				return applyArgs{}, fmt.Errorf("--branch needs a branch name (usage: %s)", commandUsage("apply"))
			}
			i++
			branchRequested = true
			out.branch = strings.TrimSpace(args[i])
		case strings.HasPrefix(arg, "--branch="):
			branchRequested = true
			out.branch = strings.TrimSpace(strings.TrimPrefix(arg, "--branch="))
		case strings.HasPrefix(arg, "-"):
			return applyArgs{}, fmt.Errorf("unknown flag %q (usage: %s)", arg, commandUsage("apply"))
		default:
			if out.sessionID != "" {
				return applyArgs{}, fmt.Errorf("unexpected extra argument %q", arg)
			}
			out.sessionID = arg
		}
	}
	if branchRequested && out.branch == "" {
		return applyArgs{}, fmt.Errorf("--branch needs a branch name (usage: %s)", commandUsage("apply"))
	}
	return out, nil
}

// cleanupDeliveredWorktrees removes the agent worktrees after the artifact has
// landed on its own branch: their commits are reachable from that branch, so the
// checkouts are only disk. It is best-effort because delivery already succeeded,
// and `--keep` skips it entirely.
func cleanupDeliveredWorktrees(ctx context.Context, ws *workspace.GitManager) {
	set := ws.Set()
	present := false
	for _, wt := range []workspace.Worktree{set.Austin, set.Tony} {
		if wt.Path == "" {
			continue
		}
		if _, err := os.Stat(wt.Path); err == nil {
			present = true
		}
	}
	if !present {
		return
	}
	if err := ws.Cleanup(ctx); err != nil {
		fmt.Printf("The Duo worktrees were left in place (delivery is complete): %v\n", err)
		return
	}
	fmt.Println("Removed the Duo worktrees; pass --keep to preserve them.")
}

func applyRoot(ctx context.Context) (string, error) {
	repo := strings.TrimSpace(os.Getenv("DUO_REPO"))
	if repo == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		repo = cwd
	}
	if abs, err := filepath.Abs(repo); err == nil {
		repo = abs
	}
	return workspace.FindRoot(ctx, repo)
}

// selectDeliveryTarget picks the session to apply: an explicit id, the single
// session with a pending delivery, or an error listing the choices.
func selectDeliveryTarget(baseDir, repoID, requested, root string) (sessionstore.Summary, error) {
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
		case item.Snapshot.NeedsDelivery():
			candidates = append(candidates, item)
		}
	}

	switch len(candidates) {
	case 1:
		return candidates[0], nil
	case 0:
		if len(broken) > 0 {
			return sessionstore.Summary{}, fmt.Errorf(
				"no Duo session for %s has a pending delivery; %d stored session(s) could not be read:\n%s",
				root, len(broken), formatBroken(broken),
			)
		}
		return sessionstore.Summary{}, fmt.Errorf("no Duo session for %s has a pending delivery", root)
	default:
		return sessionstore.Summary{}, fmt.Errorf(
			"multiple Duo sessions for %s have a pending delivery:\n%s\nrun `duo apply <session-id>` to pick one",
			root, formatCandidates(candidates),
		)
	}
}
