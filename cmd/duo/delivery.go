package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atfa/duo/internal/delivery"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/recovery"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

// deliveryOutcome is the result of one delivery transaction for a session.
type deliveryOutcome struct {
	Snapshot sessionstore.Snapshot
	Result   delivery.Result
	Applied  bool
	Reason   string
}

// deliverAndPersist runs the idempotent delivery transaction for a snapshot and
// persists every checkpoint. The pending checkpoint (which records the exact
// final HEAD) is written before the user's repository is touched, so a crash
// between the fast-forward and the applied checkpoint can be reconciled on the
// next run by observing that the original HEAD already equals FinalHead.
func deliverAndPersist(
	ctx context.Context,
	store *sessionstore.Store,
	ws *workspace.GitManager,
	snap sessionstore.Snapshot,
) (deliveryOutcome, error) {
	// Applied is monotonic for this final artifact. A stale caller must reconcile
	// DONE, never replace it with a new pending checkpoint or re-run Git.
	if snap.Delivery.Applied() {
		if snap.Phase == string(project.PhaseIntegrate) {
			if err := completeSnapshot(&snap); err != nil {
				return deliveryOutcome{Snapshot: snap}, err
			}
			if err := store.Save(snap); err != nil {
				return deliveryOutcome{Snapshot: snap}, fmt.Errorf("persist reconciled DONE checkpoint: %w", err)
			}
		}
		return deliveryOutcome{Snapshot: snap, Applied: true}, nil
	}
	finalHead, err := resolveFinalHead(ctx, ws, snap)
	if err != nil {
		return deliveryOutcome{Snapshot: snap}, err
	}

	manager := managerFor(snap, finalHead)

	now := time.Now().UTC()
	pending := snap.Delivery
	pending.Status = sessionstore.DeliveryPending
	pending.FinalHead = finalHead
	pending.Reason = ""
	if pending.FinalBranch == "" {
		pending.FinalBranch = strings.TrimSpace(ws.Set().Austin.Branch)
	}
	if pending.TargetRepo == "" {
		pending.TargetRepo = strings.TrimSpace(snap.Repository)
	}
	if pending.TargetBranch == "" {
		pending.TargetBranch = strings.TrimSpace(snap.BaseBranch)
	}
	pending.AttemptedAt = &now
	pending.CompletedAt = nil
	snap.Delivery = pending
	if err := store.Save(snap); err != nil {
		return deliveryOutcome{Snapshot: snap}, err
	}

	result, err := manager.Deliver(ctx)
	if err != nil {
		snap.Delivery = failedDelivery(pending, "delivery failed: "+err.Error())
		if saveErr := store.Save(snap); saveErr != nil {
			return deliveryOutcome{Snapshot: snap, Result: result}, fmt.Errorf("persist failed delivery checkpoint: %w", saveErr)
		}
		return deliveryOutcome{Snapshot: snap, Result: result, Applied: false, Reason: snap.Delivery.Reason}, nil
	}
	if !result.Applied {
		snap.Delivery = failedDelivery(pending, result.Check.Reason)
		if saveErr := store.Save(snap); saveErr != nil {
			return deliveryOutcome{Snapshot: snap, Result: result}, fmt.Errorf("persist pending delivery checkpoint: %w", saveErr)
		}
		return deliveryOutcome{Snapshot: snap, Result: result, Applied: false, Reason: snap.Delivery.Reason}, nil
	}

	applied := pending
	applied.Status = sessionstore.DeliveryApplied
	applied.AppliedHead = result.Check.CurrentHead
	applied.Reason = ""
	applied.CompletedAt = &now
	snap.Delivery = applied

	// The artifact landed, so the project may now become DONE. Passing through
	// the domain rule keeps the two signatures and their evidence intact.
	if snap.Phase == string(project.PhaseIntegrate) {
		if err := completeSnapshot(&snap); err != nil {
			return deliveryOutcome{Snapshot: snap, Result: result}, err
		}
	}
	if err := store.Save(snap); err != nil {
		return deliveryOutcome{Snapshot: snap, Result: result}, err
	}
	return deliveryOutcome{Snapshot: snap, Result: result, Applied: true}, nil
}

func failedDelivery(record sessionstore.Delivery, reason string) sessionstore.Delivery {
	record.Status = sessionstore.DeliveryPending
	record.Reason = strings.TrimSpace(reason)
	return record
}

// completeSnapshot performs the explicit INTEGRATE → DONE transition while
// preserving both signatures and their evidence.
func completeSnapshot(snap *sessionstore.Snapshot) error {
	state := project.NewState()
	if err := state.Restore(recovery.ProjectSnapshot(*snap)); err != nil {
		return err
	}
	done, err := state.Complete()
	if err != nil {
		return err
	}
	snap.Phase = string(done.Phase)
	return nil
}

// resolveFinalHead proves which artifact this session considers final. It trusts
// the durable record first, then the integration checkpoint, then the persisted
// Austin worktree, and finally the persisted Austin branch ref in the original
// repository. Every fallback is verified against Git, never guessed.
func resolveFinalHead(ctx context.Context, ws *workspace.GitManager, snap sessionstore.Snapshot) (string, error) {
	if head := snap.FinalHead(); head != "" {
		return head, nil
	}
	if wt, ok := snap.Worktree(protocol.Austin); ok {
		if strings.TrimSpace(wt.Path) != "" {
			if head, err := ws.Head(ctx, protocol.Austin); err == nil && strings.TrimSpace(head) != "" {
				return head, nil
			}
		}
		if strings.TrimSpace(wt.Branch) != "" {
			if head, err := delivery.BranchHead(ctx, snap.Repository, wt.Branch); err == nil && strings.TrimSpace(head) != "" {
				return head, nil
			}
		}
	}
	return "", fmt.Errorf(
		"session %s has no recoverable final artifact: state records neither an integration head nor an Austin branch",
		snap.SessionID,
	)
}

func managerFor(snap sessionstore.Snapshot, finalHead string) delivery.Manager {
	manager := delivery.Manager{
		Repository: strings.TrimSpace(snap.Repository),
		BaseBranch: strings.TrimSpace(snap.BaseBranch),
		BaseCommit: strings.TrimSpace(snap.BaseCommit),
		FinalHead:  finalHead,
	}
	if wt, ok := snap.Worktree(protocol.Austin); ok {
		manager.FinalBranch = strings.TrimSpace(wt.Branch)
	}
	return manager
}

// reconcileDelivery handles a session whose agent work may already be finished.
// It returns stop == true when no agent should be started: either the artifact
// is now in the user's repository (DONE), or delivery is safely pending and only
// the human can unblock it. The returned outcome is suitable for printing.
func reconcileDelivery(
	ctx context.Context,
	store *sessionstore.Store,
	ws *workspace.GitManager,
	journal *sessionstore.EventLog,
	snap sessionstore.Snapshot,
) (deliveryOutcome, bool, error) {
	// Crash window: delivery was persisted but the DONE transition was not.
	if snap.Phase == string(project.PhaseIntegrate) && snap.Delivery.Applied() {
		if err := completeSnapshot(&snap); err != nil {
			return deliveryOutcome{Snapshot: snap}, false, err
		}
		if err := store.Save(snap); err != nil {
			return deliveryOutcome{Snapshot: snap}, false, err
		}
		journal.Record("session_delivered", map[string]any{
			"sessionId":   snap.SessionID,
			"finalHead":   snap.Delivery.FinalHead,
			"appliedHead": snap.Delivery.AppliedHead,
			"reconciled":  true,
		})
		manager := managerFor(snap, snap.Delivery.AppliedHead)
		changes, _ := manager.ChangedPaths(ctx)
		return deliveryOutcome{Snapshot: snap, Result: delivery.Result{Changes: changes}, Applied: true}, true, nil
	}

	if !snap.NeedsDelivery() {
		return deliveryOutcome{Snapshot: snap}, false, nil
	}

	outcome, err := deliverAndPersist(ctx, store, ws, snap)
	if err != nil {
		return outcome, false, err
	}
	if outcome.Applied {
		journal.Record("session_delivered", map[string]any{
			"sessionId":    outcome.Snapshot.SessionID,
			"finalHead":    outcome.Snapshot.Delivery.FinalHead,
			"appliedHead":  outcome.Snapshot.Delivery.AppliedHead,
			"targetBranch": outcome.Snapshot.Delivery.TargetBranch,
			"changedFiles": len(outcome.Result.Changes),
		})
	} else {
		journal.Record("session_delivery_pending", map[string]any{
			"sessionId": outcome.Snapshot.SessionID,
			"finalHead": outcome.Snapshot.Delivery.FinalHead,
			"reason":    outcome.Reason,
		})
	}
	return outcome, true, nil
}

func printDeliverySuccess(outcome deliveryOutcome) {
	snap := outcome.Snapshot
	applied := snap.Delivery.AppliedHead
	if applied == "" {
		applied = snap.Delivery.FinalHead
	}
	fmt.Println("Duo delivery complete.")
	fmt.Println()
	fmt.Println("The final Duo result is now available in the repository you launched Duo from:")
	fmt.Printf("  repository: %s\n", snap.Delivery.TargetRepo)
	fmt.Printf("  branch:     %s\n", snap.Delivery.TargetBranch)
	fmt.Printf("  applied:    %s\n", applied)
	fmt.Println()
	fmt.Println("Changed files:")
	fmt.Println(delivery.Summary(outcome.Result.Changes))
}

func printDeliveryPending(outcome deliveryOutcome) {
	snap := outcome.Snapshot
	reason := outcome.Reason
	if reason == "" {
		reason = snap.Delivery.Reason
	}
	fmt.Println("Agent work is complete, but delivery is pending.")
	fmt.Println()
	fmt.Println("Final result:")
	fmt.Printf("  branch: %s\n", snap.Delivery.FinalBranch)
	fmt.Printf("  head:   %s\n", snap.Delivery.FinalHead)
	fmt.Println()
	fmt.Println("The original repository could not be updated safely:")
	fmt.Printf("  %s\n", reason)
	fmt.Println()
	fmt.Println("No user files were overwritten.")
	fmt.Println()
	fmt.Println("After resolving the original repository, run:")
	fmt.Printf("  duo apply %s\n", snap.SessionID)
	fmt.Println()
	fmt.Println("To finish the handoff manually instead, from the repository you launched Duo from:")
	fmt.Printf("  git merge --ff-only %s\n", snap.Delivery.FinalHead)
	if base := strings.TrimSpace(snap.BaseCommit); base != "" {
		fmt.Printf("  git cherry-pick %s..%s   # if it cannot fast-forward\n", base, snap.Delivery.FinalHead)
	} else {
		fmt.Printf("  git cherry-pick %s   # if it cannot fast-forward\n", snap.Delivery.FinalHead)
	}
}
