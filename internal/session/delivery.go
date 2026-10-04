package session

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

// DeliveryOutcome is the result of one delivery transaction for a session.
type DeliveryOutcome struct {
	Snapshot sessionstore.Snapshot
	Result   delivery.Result
	Applied  bool
	Reason   string
}

// DeliverAndPersist runs the idempotent delivery transaction for a snapshot and
// persists every checkpoint. The pending checkpoint (which records the exact final
// HEAD) is written before the user's repository is touched, so a crash between the
// fast-forward and the applied checkpoint can be reconciled on the next run by
// observing that the original history already contains FinalHead.
func DeliverAndPersist(
	ctx context.Context,
	store *sessionstore.Store,
	ws *workspace.GitManager,
	snap sessionstore.Snapshot,
) (DeliveryOutcome, error) {
	// Applied is monotonic for this final artifact. A stale caller must reconcile
	// DONE, never replace it with a new pending checkpoint or re-run Git.
	if snap.Delivery.Applied() {
		if snap.Phase == string(project.PhaseIntegrate) || snap.Phase == string(project.PhaseVerify) {
			if err := CompleteSnapshot(&snap); err != nil {
				return DeliveryOutcome{Snapshot: snap}, err
			}
			if err := store.Save(snap); err != nil {
				return DeliveryOutcome{Snapshot: snap}, fmt.Errorf("persist reconciled DONE checkpoint: %w", err)
			}
		}
		return DeliveryOutcome{Snapshot: snap, Applied: true}, nil
	}
	finalHead, err := ResolveFinalHead(ctx, ws, snap)
	if err != nil {
		return DeliveryOutcome{Snapshot: snap}, err
	}

	// A Fast pass describes one exact Austin HEAD. Re-check the binding here as
	// well as in the live coordinator and in recovery, because `duo apply` can be
	// invoked independently of both.
	if snap.EffectiveMode() == project.ModeFast && !snap.VerificationResult().Passed(finalHead) {
		return DeliveryOutcome{Snapshot: snap}, fmt.Errorf(
			"session %s: the verified Austin HEAD no longer matches the artifact to deliver; verification must be repeated before apply",
			snap.SessionID,
		)
	}

	manager := ManagerFor(snap, finalHead)

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
		return DeliveryOutcome{Snapshot: snap}, err
	}

	result, err := manager.Deliver(ctx)
	if err != nil {
		snap.Delivery = FailedDelivery(pending, "delivery failed: "+err.Error())
		if saveErr := store.Save(snap); saveErr != nil {
			return DeliveryOutcome{Snapshot: snap, Result: result}, fmt.Errorf("persist failed delivery checkpoint: %w", saveErr)
		}
		return DeliveryOutcome{Snapshot: snap, Result: result, Applied: false, Reason: snap.Delivery.Reason}, nil
	}
	if !result.Applied {
		snap.Delivery = FailedDelivery(pending, result.Check.Reason)
		if saveErr := store.Save(snap); saveErr != nil {
			return DeliveryOutcome{Snapshot: snap, Result: result}, fmt.Errorf("persist pending delivery checkpoint: %w", saveErr)
		}
		return DeliveryOutcome{Snapshot: snap, Result: result, Applied: false, Reason: snap.Delivery.Reason}, nil
	}

	applied := pending
	applied.Status = sessionstore.DeliveryApplied
	applied.AppliedHead = result.Check.CurrentHead
	applied.Reason = ""
	applied.CompletedAt = &now
	snap.Delivery = applied

	// The artifact landed, so the project may now become DONE. Passing through the
	// domain rule keeps the two signatures and their evidence intact.
	if snap.Phase == string(project.PhaseIntegrate) || snap.Phase == string(project.PhaseVerify) {
		if err := CompleteSnapshot(&snap); err != nil {
			return DeliveryOutcome{Snapshot: snap, Result: result}, err
		}
	}
	if err := store.Save(snap); err != nil {
		return DeliveryOutcome{Snapshot: snap, Result: result}, err
	}
	return DeliveryOutcome{Snapshot: snap, Result: result, Applied: true}, nil
}

// FailedDelivery marks a delivery record pending with a reason.
func FailedDelivery(record sessionstore.Delivery, reason string) sessionstore.Delivery {
	record.Status = sessionstore.DeliveryPending
	record.Reason = strings.TrimSpace(reason)
	return record
}

// CompleteSnapshot performs the explicit INTEGRATE → DONE transition while
// preserving both signatures and their evidence.
func CompleteSnapshot(snap *sessionstore.Snapshot) error {
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

// ResolveFinalHead proves which artifact this session considers final. It trusts
// the durable record first, then the integration checkpoint, then the persisted
// Austin worktree, and finally the persisted Austin branch ref in the original
// repository. Every fallback is verified against Git, never guessed.
func ResolveFinalHead(ctx context.Context, ws *workspace.GitManager, snap sessionstore.Snapshot) (string, error) {
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

// ManagerFor builds the delivery manager for a snapshot and its final head.
func ManagerFor(snap sessionstore.Snapshot, finalHead string) delivery.Manager {
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

// ReconcileDelivery handles a session whose agent work may already be finished. It
// returns stop == true when no agent should be started: either the artifact is now
// in the user's repository (DONE), or delivery is safely pending and only the
// human can unblock it. The returned outcome is suitable for reporting.
func ReconcileDelivery(
	ctx context.Context,
	store *sessionstore.Store,
	ws *workspace.GitManager,
	journal *sessionstore.EventLog,
	snap sessionstore.Snapshot,
) (DeliveryOutcome, bool, error) {
	// Crash window: delivery was persisted but the DONE transition was not.
	if (snap.Phase == string(project.PhaseIntegrate) || snap.Phase == string(project.PhaseVerify)) && snap.Delivery.Applied() {
		if err := CompleteSnapshot(&snap); err != nil {
			return DeliveryOutcome{Snapshot: snap}, false, err
		}
		if err := store.Save(snap); err != nil {
			return DeliveryOutcome{Snapshot: snap}, false, err
		}
		journal.Record("session_delivered", map[string]any{
			"sessionId":   snap.SessionID,
			"finalHead":   snap.Delivery.FinalHead,
			"appliedHead": snap.Delivery.AppliedHead,
			"reconciled":  true,
		})
		manager := ManagerFor(snap, snap.Delivery.AppliedHead)
		changes, _ := manager.ChangedPaths(ctx)
		return DeliveryOutcome{Snapshot: snap, Result: delivery.Result{Changes: changes}, Applied: true}, true, nil
	}

	if !snap.NeedsDelivery() {
		return DeliveryOutcome{Snapshot: snap}, false, nil
	}

	outcome, err := DeliverAndPersist(ctx, store, ws, snap)
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
