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

	// Pushed reports that the requested push also updated the upstream branch.
	// PushReason explains why a requested push did not happen; the artifact is
	// still delivered locally in that case.
	Pushed     bool
	PushReason string
}

// DeliveryOptions selects where the artifact lands. The zero value is the only
// default: a local fast-forward of the branch Duo started on.
type DeliveryOptions struct {
	// Branch lands the artifact on that new branch instead of moving the branch
	// Duo started on, so a protected branch is never touched.
	Branch string

	// Push updates the delivered branch's upstream after it lands. The remote is
	// read first: a remote that moved ahead refuses the whole delivery before the
	// local branch is touched.
	Push bool
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
	return DeliverAndPersistTo(ctx, store, ws, snap, DeliveryOptions{})
}

// DeliverAndPersistTo is DeliverAndPersist with an explicit landing target, so a
// caller can ask for a new branch instead of the default fast-forward.
func DeliverAndPersistTo(
	ctx context.Context,
	store *sessionstore.Store,
	ws *workspace.GitManager,
	snap sessionstore.Snapshot,
	opts DeliveryOptions,
) (DeliveryOutcome, error) {
	// Applied is monotonic for this final artifact. A stale caller must reconcile
	// DONE, never replace it with a new pending checkpoint or re-run Git.
	if snap.Delivery.Applied() {
		// In STEP mode the record describes the last landed step, not the session, so
		// "applied" only means "nothing to deliver" while it still describes the step
		// the cursor is on. A newly submitted step is a new transaction.
		if snap.EffectiveMode() == project.ModeStep && !currentStepLanded(snap) {
			snap.Delivery = sessionstore.Delivery{
				TargetRepo:   snap.Delivery.TargetRepo,
				TargetBranch: snap.Delivery.TargetBranch,
			}
		} else {
			if snap.Phase == string(project.PhaseIntegrate) || snap.Phase == string(project.PhaseVerify) {
				if err := CompleteSnapshot(&snap); err != nil {
					return DeliveryOutcome{Snapshot: snap}, err
				}
				if err := store.Save(snap); err != nil {
					return DeliveryOutcome{Snapshot: snap}, fmt.Errorf("persist reconciled DONE checkpoint: %w", err)
				}
			}
			// A retry re-reads the diff as well: the pull request body is built from
			// these changes, so leaving them empty would claim the delivery changed
			// nothing at all.
			outcome := DeliveryOutcome{Snapshot: snap, Applied: true}
			if head := deliveredHead(snap); head != "" {
				outcome.Result.Changes, _ = ManagerFor(snap, head).ChangedPaths(ctx)
			}
			// The artifact is already delivered, but a requested push may still be
			// outstanding: the remote is read again, and pushed only if it is behind.
			if opts.Push {
				pushOutcome, err := pushDelivered(ctx, snap, opts)
				if err != nil {
					return DeliveryOutcome{Snapshot: snap}, err
				}
				outcome.Pushed = pushOutcome.Pushed
				outcome.PushReason = pushOutcome.PushReason
			}
			return outcome, nil
		}
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

	// A STEP landing is gated by Tony's verdict for that step, bound to one exact
	// HEAD. The same re-check belongs here: `duo apply` runs this transaction on
	// its own, so a checkpoint that claims INTEGRATE without a verdict binding the
	// HEAD must not land unreviewed work. A finished queue has no step left to gate.
	if snap.EffectiveMode() == project.ModeStep && !project.QueueFinished(snap.Steps) && !stepVerdictBinds(snap, finalHead) {
		return DeliveryOutcome{Snapshot: snap}, fmt.Errorf(
			"session %s: step %d has no passed verdict binding the HEAD to deliver; Tony must verify the step before apply",
			snap.SessionID, snap.Step,
		)
	}

	manager := ManagerFor(snap, finalHead)
	manager.Branch = strings.TrimSpace(opts.Branch)

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
	// A new-branch delivery lands somewhere other than the branch Duo started on,
	// so the record must name the branch the artifact actually went to.
	if branch := strings.TrimSpace(opts.Branch); branch != "" {
		pending.TargetBranch = branch
	}

	// A push is proven safe before anything moves, so a remote that moved refuses
	// the delivery while the local branch is still where the user left it.
	var pushCheck *delivery.PushCheck
	if opts.Push {
		check, err := delivery.CheckPush(ctx, pending.TargetRepo, pending.TargetBranch, finalHead)
		if err != nil {
			return DeliveryOutcome{Snapshot: snap}, err
		}
		if !check.CanPush {
			pending.Status = sessionstore.DeliveryPending
			pending.Reason = check.Reason
			pending.AttemptedAt = &now
			snap.Delivery = pending
			if err := store.Save(snap); err != nil {
				return DeliveryOutcome{Snapshot: snap}, fmt.Errorf("persist refused push checkpoint: %w", err)
			}
			return DeliveryOutcome{Snapshot: snap, Applied: false, Reason: check.Reason}, nil
		}
		pushCheck = &check
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

	// A Step session lands one step at a time. The step record is the durable proof
	// that the user received it, and the session-level record keeps meaning "the
	// latest landing", so `duo apply`, NeedsDelivery and recovery read one shape.
	// A finished queue has no step left to land, so it only needs completing.
	if snap.EffectiveMode() == project.ModeStep {
		if !project.QueueFinished(snap.Steps) {
			if err := LandStepSnapshot(&snap, finalHead); err != nil {
				return DeliveryOutcome{Snapshot: snap, Result: result}, err
			}
		}
		if project.QueueFinished(snap.Steps) {
			if err := CompleteSnapshot(&snap); err != nil {
				return DeliveryOutcome{Snapshot: snap, Result: result}, err
			}
		}
		if err := store.Save(snap); err != nil {
			return DeliveryOutcome{Snapshot: snap, Result: result}, err
		}
		return finishDelivery(ctx, snap, result, pushCheck), nil
	}

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
	return finishDelivery(ctx, snap, result, pushCheck), nil
}

// finishDelivery reports a landed artifact and performs the push a caller asked
// for. A failed push does not undo the landing: the artifact is delivered
// locally, and the reason is reported so `duo apply --push` can retry it.
func finishDelivery(ctx context.Context, snap sessionstore.Snapshot, result delivery.Result, push *delivery.PushCheck) DeliveryOutcome {
	outcome := DeliveryOutcome{Snapshot: snap, Result: result, Applied: true}
	if push == nil {
		return outcome
	}
	if err := delivery.Push(ctx, *push); err != nil {
		outcome.PushReason = err.Error()
		return outcome
	}
	outcome.Pushed = true
	return outcome
}

// deliveredHead is the artifact an applied delivery record landed.
func deliveredHead(snap sessionstore.Snapshot) string {
	if head := strings.TrimSpace(snap.Delivery.AppliedHead); head != "" {
		return head
	}
	return strings.TrimSpace(snap.Delivery.FinalHead)
}

// pushDelivered completes a push for an artifact that is already delivered, so a
// retry does not move the local branch a second time.
func pushDelivered(ctx context.Context, snap sessionstore.Snapshot, opts DeliveryOptions) (DeliveryOutcome, error) {
	branch := strings.TrimSpace(snap.Delivery.TargetBranch)
	if branch == "" {
		branch = strings.TrimSpace(snap.BaseBranch)
	}
	head := deliveredHead(snap)
	check, err := delivery.CheckPush(ctx, strings.TrimSpace(snap.Delivery.TargetRepo), branch, head)
	if err != nil {
		return DeliveryOutcome{}, err
	}
	if !check.CanPush {
		return DeliveryOutcome{PushReason: check.Reason}, nil
	}
	if err := delivery.Push(ctx, check); err != nil {
		return DeliveryOutcome{PushReason: err.Error()}, nil
	}
	return DeliveryOutcome{Pushed: true}, nil
}

// currentStepLanded reports whether the session-level delivery record describes the
// step the cursor is on. It is what scopes "already applied" to one step.
func currentStepLanded(snap sessionstore.Snapshot) bool {
	if snap.Delivery.FinalHead == "" {
		return false
	}
	idx := snap.Step
	if idx < 1 || idx > len(snap.Steps) {
		return project.QueueFinished(snap.Steps) && snap.Delivery.FinalHead == snap.Delivery.AppliedHead
	}
	return snap.Steps[idx-1].Done && snap.Steps[idx-1].DeliveryHead == snap.Delivery.FinalHead
}

// stepVerdictBinds reports whether the step under the cursor carries Tony's passed
// verdict for exactly the HEAD about to be delivered.
func stepVerdictBinds(snap sessionstore.Snapshot, head string) bool {
	current := snap.StepUnderCursor()
	return current != nil &&
		current.Verification.Status == project.VerificationPassed &&
		current.Verification.Targets(head)
}

// LandStepSnapshot performs the step-landing transition through the domain rule,
// so the queue, the cursor and the phase move by one rule and stay consistent.
func LandStepSnapshot(snap *sessionstore.Snapshot, head string) error {
	state := project.NewState()
	if err := state.Restore(recovery.ProjectSnapshot(*snap)); err != nil {
		return err
	}
	next, _, err := state.LandStep(head)
	if err != nil {
		return err
	}
	snap.Phase = string(next.Phase)
	snap.Step = next.Step
	snap.Steps = next.Steps
	return nil
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
	// A Step session that landed a step and crashed before the queue advanced is
	// "step landed, continue", not "finished": this branch must be read before the
	// Goal and Fast crash window, which mean DONE. Landing is idempotent on the step
	// record, so a re-run cannot advance the queue twice.
	if snap.EffectiveMode() == project.ModeStep && snap.Delivery.Applied() && snap.Phase == string(project.PhaseIntegrate) {
		switch {
		case currentStepLanded(snap):
			journal.Record("step_landed", map[string]any{
				"sessionId":   snap.SessionID,
				"step":        snap.Step,
				"finalHead":   snap.Delivery.FinalHead,
				"appliedHead": snap.Delivery.AppliedHead,
				"reconciled":  true,
			})
			return DeliveryOutcome{Snapshot: snap, Applied: true}, false, nil

		case snap.StepDeliveryIsCurrent() && stepVerdictBinds(snap, snap.Delivery.FinalHead):
			// The record is this step's own landing, interrupted before the queue
			// moved: finish the transaction the crash left half-done.
			if err := LandStepSnapshot(&snap, snap.Delivery.FinalHead); err != nil {
				return DeliveryOutcome{Snapshot: snap}, false, err
			}
			if err := store.Save(snap); err != nil {
				return DeliveryOutcome{Snapshot: snap}, false, err
			}
			journal.Record("step_landed", map[string]any{
				"sessionId":   snap.SessionID,
				"step":        snap.Step,
				"finalHead":   snap.Delivery.FinalHead,
				"appliedHead": snap.Delivery.AppliedHead,
				"reconciled":  true,
			})
			return DeliveryOutcome{Snapshot: snap, Applied: true}, false, nil
		}
		// Either the record is an earlier step's landing - the session-level record
		// keeps meaning "the latest landing", so it survives the queue advancing - or
		// it describes this step with no verdict binding it. The step under the cursor
		// has never been delivered, and landing the recorded head would land the
		// previous step a second time and abort resume, so the record is dropped and
		// the step goes through the ordinary transaction below, which re-checks the
		// verdict gate before it touches the user's repository.
		snap.Delivery = sessionstore.Delivery{
			FinalBranch:  snap.Delivery.FinalBranch,
			TargetRepo:   snap.Delivery.TargetRepo,
			TargetBranch: snap.Delivery.TargetBranch,
		}
	}

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
		// A landed non-final step is not the end of the session: the queue has more
		// work and this transaction runs once per step. Stopping here would end the
		// resumed process after one landing, while the live coordinator continues to
		// the next step, so the human would have to resume a second time.
		if outcome.Snapshot.EffectiveMode() == project.ModeStep && !project.QueueFinished(outcome.Snapshot.Steps) {
			return outcome, false, nil
		}
	} else {
		journal.Record("session_delivery_pending", map[string]any{
			"sessionId": outcome.Snapshot.SessionID,
			"finalHead": outcome.Snapshot.Delivery.FinalHead,
			"reason":    outcome.Reason,
		})
	}
	return outcome, true, nil
}
