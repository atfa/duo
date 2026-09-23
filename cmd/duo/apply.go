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

// runApply implements `duo apply [session-id]`. It is dispatched from main
// before interactive configuration, and never starts Austin or Tony.
//
// It reuses the exact same safety rules as automatic delivery: only a clean,
// recorded branch that can be fast-forwarded is moved. A pending delivery is
// never forced.
func runApply(ctx context.Context, args []string) error {
	sessionID, err := parseApplyArgs(args)
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

	summary, err := selectDeliveryTarget(baseDir, repoID, sessionID, root)
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

	outcome, err := deliverAndPersist(ctx, store, ws, summary.Snapshot)
	if err != nil {
		return err
	}
	if outcome.Applied {
		logger.Printf("delivered session %s: %s applied to %s@%s",
			summary.SessionID, outcome.Snapshot.Delivery.AppliedHead,
			outcome.Snapshot.Delivery.TargetRepo, outcome.Snapshot.Delivery.TargetBranch)
		printDeliverySuccess(outcome)
		return nil
	}

	logger.Printf("delivery still pending for session %s: %s", summary.SessionID, outcome.Reason)
	printDeliveryPending(outcome)
	return errDeliveryPending
}

func parseApplyArgs(args []string) (string, error) {
	var sessionID string
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "":
		case arg == "--session" || arg == "-s":
			if i+1 >= len(args) {
				return "", fmt.Errorf("--session requires a session id")
			}
			sessionID = strings.TrimSpace(args[i+1])
			i++
		case strings.HasPrefix(arg, "--session="):
			sessionID = strings.TrimSpace(strings.TrimPrefix(arg, "--session="))
		case strings.HasPrefix(arg, "-"):
			return "", fmt.Errorf("unknown flag %q (usage: duo apply [session-id])", arg)
		default:
			if sessionID != "" {
				return "", fmt.Errorf("unexpected extra argument %q", arg)
			}
			sessionID = arg
		}
	}
	return sessionID, nil
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
