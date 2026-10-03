package coordinator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// requestFinalSummary asks Austin to report the finished round to the human.
// It runs at DONE in both modes, which is the one point every completed session
// passes through: Fast after a passed verification and Goal after dual final
// approval, once the artifact is in the user's repository.
//
// Austin answers in its normal assistant message, and the timeline labels that
// `Austin → Human`, so the report reaches the human without the human having
// asked for it. Nothing here changes the phase machine or the delivered
// artifact: the session is already DONE and stays that way.
func (c *Coordinator) requestFinalSummary(ctx context.Context) {
	if !c.claimFinalSummary() {
		return
	}
	snap := c.project.Snapshot()
	if err := c.sendToAgent(ctx, protocol.Austin, protocol.Message{
		Version:   protocol.Version,
		Type:      protocol.MsgDuoNotice,
		From:      protocol.Duo,
		To:        protocol.Austin,
		Text:      c.finalSummaryPrompt(snap),
		Timestamp: time.Now().UnixMilli(),
	}); err != nil {
		// A disconnected Austin is recoverable, so the request is not consumed:
		// the next DONE transition for this round asks again.
		c.unclaimFinalSummary()
		c.logf("final summary request to Austin failed: %v", err)
		c.emit(events.KindError, protocol.Duo, protocol.Austin,
			"Duo could not ask Austin for the final summary: "+err.Error())
		return
	}
	c.recordEvent("summary_requested", map[string]any{
		"mode": snap.EffectiveMode().String(),
		"head": c.CurrentDelivery().FinalHead,
	})
	c.emit(events.KindSystem, protocol.Duo, protocol.Austin,
		"asking Austin to summarise this finished session for the human")
}

// claimFinalSummary reserves the one summary request a round may make. Delivery
// is retried by design — a duplicate final signature can re-enter the delivery
// transaction — and Austin must not be asked to report the same finished round
// twice.
func (c *Coordinator) claimFinalSummary() bool {
	c.summaryMu.Lock()
	defer c.summaryMu.Unlock()
	if c.summaryRequested {
		return false
	}
	c.summaryRequested = true
	return true
}

// unclaimFinalSummary gives the claim back. A request that could not be
// delivered stays owed, and the round the human opens after a finished one
// re-arms the request so it reports on its own delivery.
func (c *Coordinator) unclaimFinalSummary() {
	c.summaryMu.Lock()
	c.summaryRequested = false
	c.summaryMu.Unlock()
}

// finalSummaryPrompt asks Austin for the process and the results of the whole
// session, not a restatement of the DONE notice, and names the delivery facts
// that anchor the report.
func (c *Coordinator) finalSummaryPrompt(snap project.Snapshot) string {
	d := c.CurrentDelivery()
	set := c.workspace.Set()
	branch := strings.TrimSpace(d.TargetBranch)
	if branch == "" {
		branch = set.BaseBranch
	}
	applied := shortSHA(d.AppliedHead)
	if applied == "" {
		applied = shortSHA(d.FinalHead)
	}
	return fmt.Sprintf(
		"[Duo final summary request]\nThis session is DONE in %s mode: the final artifact was delivered to %s on %s as %s.\n\nSummarise the whole session for the human in your normal assistant reply; Duo shows that reply as `Austin → Human`. Do not use duo_send: that message reaches Tony, not the human.\n\nCover two things:\n  - The process: how the work actually went. What was asked and agreed, the approach taken and why, anything that was rejected, corrected or reworked on the way, and how the review, cross-review and verification actually went.\n  - The results: what a user of this repository now has that they did not have before. The behaviour that changed, the important files, the delivered HEAD, and anything they must know before relying on it — limitations, trade-offs, follow-ups and risks.\n\nThe artifact is already delivered: do not edit files, do not commit, and do not start new work. The human can submit a follow-up task at any time, which reopens the session as a fresh round.",
		snap.EffectiveMode().Display(), set.Repository, branch, applied,
	)
}
