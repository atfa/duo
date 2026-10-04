package main

import (
	"context"
	"fmt"

	"github.com/atfa/duo/internal/delivery"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

// deliveryOutcome is the shared delivery result. It is an alias, not a copy, so
// `duo apply` and a session report the same type from the same implementation.
type deliveryOutcome = session.DeliveryOutcome

// deliverAndPersist runs the idempotent delivery transaction for a snapshot.
//
// `duo apply` is its own entry point and can be invoked independently of any
// session, which is why this stays reachable from the CLI; the logic itself lives
// with the session.
func deliverAndPersist(
	ctx context.Context,
	store *sessionstore.Store,
	ws *workspace.GitManager,
	snap sessionstore.Snapshot,
) (deliveryOutcome, error) {
	return session.DeliverAndPersist(ctx, store, ws, snap)
}

// reconcileDelivery handles a session whose agent work may already be finished.
func reconcileDelivery(
	ctx context.Context,
	store *sessionstore.Store,
	ws *workspace.GitManager,
	journal *sessionstore.EventLog,
	snap sessionstore.Snapshot,
) (deliveryOutcome, bool, error) {
	return session.ReconcileDelivery(ctx, store, ws, journal, snap)
}

// resolveFinalHead proves which artifact a session considers final.
func resolveFinalHead(ctx context.Context, ws *workspace.GitManager, snap sessionstore.Snapshot) (string, error) {
	return session.ResolveFinalHead(ctx, ws, snap)
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
	fmt.Println("To preserve both histories, merge the final result manually from the repository you launched Duo from:")
	fmt.Printf("  git merge --no-ff %s\n", snap.Delivery.FinalHead)
	fmt.Printf("  duo apply %s\n", snap.SessionID)
}
