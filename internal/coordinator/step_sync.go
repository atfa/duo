package coordinator

import (
	"context"
	"fmt"
	"strings"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/protocol"
)

// syncVerifierToLandedStep fast-forwards Tony's worktree onto the step that just
// landed.
//
// This is deliberately not part of the delivery transaction: delivery moves the
// user's branch, and this moves the verifier so his tree matches it. Without it,
// step N+1 is reviewed and built by Tony on a tree missing steps 1..N, which is
// exactly the review this mode exists to provide, and every step's merge would
// re-conflict against the original session base.
//
// The target is the landed step's own recorded head, never Austin's live HEAD, so
// work in flight for the next step is never presented to the verifier as landed.
// A dirty or already-current worktree is reported rather than forced, so this is
// safe to call on every landing; the same reconciliation runs on resume.
func (c *Coordinator) syncVerifierToLandedStep(ctx context.Context) {
	if c.workspace == nil {
		return
	}
	record := c.CurrentDelivery()
	// Nothing reached the user's repository, so there is nothing to move onto: a
	// blocked delivery must not leave Tony further ahead than the user.
	if !record.Applied() {
		return
	}
	head := strings.TrimSpace(record.AppliedHead)
	if head == "" {
		head = strings.TrimSpace(record.FinalHead)
	}
	if head == "" {
		return
	}

	sync, err := c.workspace.SyncTonyTo(ctx, head)
	switch {
	case err != nil:
		c.logf("fast-forward the verifier onto the landed step: %v", err)
	case sync.Skipped:
		c.emit(events.KindSystem, protocol.Duo, "", "verifier worktree was not fast-forwarded to the landed step: "+sync.Reason)
	case sync.Moved:
		c.emit(events.KindSystem, protocol.Duo, "", fmt.Sprintf("Tony's worktree fast-forwarded to the landed step %s", shortSHA(sync.Head)))
	}
}
