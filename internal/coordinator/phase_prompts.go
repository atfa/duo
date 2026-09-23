package coordinator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
)

func (c *Coordinator) broadcastPhaseAdvance(
	ctx context.Context,
	previous, next project.Phase,
	snap project.Snapshot,
	integrationText string,
) {
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		_ = c.server.Send(ctx, agent, protocol.Message{
			Version:   1,
			Type:      protocol.MsgDuoNotice,
			From:      protocol.Duo,
			To:        agent,
			Text:      c.phasePrompt(agent, previous, next, snap, integrationText),
			Timestamp: time.Now().UnixMilli(),
		})
	}
}

func (c *Coordinator) phasePrompt(
	agent protocol.AgentID,
	previous, next project.Phase,
	snap project.Snapshot,
	integrationText string,
) string {
	set := c.workspace.Set()
	own, _ := set.For(agent)
	peer, _ := set.For(protocol.PeerOf(agent))

	switch next {
	case project.PhaseExecute:
		return fmt.Sprintf(
			"[Duo phase transition: %s → EXECUTE]\nBoth agents approved shared plan v%d.\n\n%s\n\nYour private worktree is %s on branch %s. Your peer works independently at %s on %s. Any exploratory edits you already made during PLAN may remain if they fit the approved plan; revise them if peer feedback changed the design. Execute your assigned part, coordinate with duo_send, commit your finished work, and call duo_set_status ready=true only when your worktree is clean.",
			previous, snap.PlanVersion, snap.Plan,
			own.Path, own.Branch, peer.Path, peer.Branch,
		)

	case project.PhaseReview:
		peerAgent := protocol.PeerOf(agent)
		peerEvidence := snap.Evidence[peerAgent]
		if peerEvidence == "" {
			// Evidence is reset after transition; use current peer branch instead.
			if status, err := c.workspace.Status(context.Background(), peerAgent); err == nil {
				peerEvidence = status.Head
			}
		}
		return fmt.Sprintf(
			"[Duo phase transition: %s → REVIEW]\nBoth agents reported execution complete. Cross-review %s's branch %s, current HEAD %s. You may inspect it with git show/diff from your own worktree; do not edit the peer worktree. If you find an issue, use duo_send so the owner can fix and commit it. Your review signature is bound to the exact peer HEAD you reviewed; if that HEAD changes, Duo revokes stale approval automatically. Sign ready=true only with no unresolved objections.",
			previous, peerAgent, peer.Branch, shortSHA(peerEvidence),
		)

	case project.PhaseIntegrate:
		ownDuty := "\n\nBefore signing final INTEGRATE, inspect the complete integrated tree. Remove temporary collaboration-only artifacts, scratch files, drafts or diagnostic files that are not intended to be part of the user's final project. Commit any cleanup before final sign-off: the final Git tree is exactly what the user receives."
		if agent != protocol.Austin {
			ownDuty = "\n\nReview not only correctness but final repository hygiene. Reject INTEGRATE if the integrated tree contains accidental temporary artifacts, scratch files or drafts that the user did not ask for."
		}
		return fmt.Sprintf(
			"[Duo phase transition: %s → INTEGRATE]\nCross-review passed. %s\n\nAustin's worktree (%s, branch %s) is the integration worktree. Austin owns conflict resolution, final cleanup and final test execution. Tony independently reviews the final integrated branch. Both agents sign INTEGRATE only when the same clean Austin HEAD is acceptable; stale signatures are revoked if that HEAD changes. INTEGRATE means the two agents agree on the final artifact; Duo then hands that exact artifact back to the repository the user launched Duo from before marking DONE.%s",
			previous, integrationText, set.Austin.Path, set.Austin.Branch, ownDuty,
		)

	case project.PhaseDone:
		d := c.CurrentDelivery()
		branch := strings.TrimSpace(d.TargetBranch)
		if branch == "" {
			branch = set.BaseBranch
		}
		applied := shortSHA(d.AppliedHead)
		if applied == "" {
			applied = shortSHA(d.FinalHead)
		}
		return fmt.Sprintf(
			"[Duo phase transition: INTEGRATE → DONE]\nBoth agents approved final integrated HEAD %s.\n\nDelivery complete:\n  original branch: %s\n  applied HEAD:    %s\n\nThe final Duo result is now available in the repository from which the user launched Duo.",
			shortSHA(d.FinalHead), branch, applied,
		)

	default:
		return "[Duo] Phase changed to " + string(next)
	}
}

// broadcastFinalApproval tells both agents that the agent-facing lifecycle is
// over and Duo Core is now delivering the approved artifact. No agent work is
// expected while delivery runs.
func (c *Coordinator) broadcastFinalApproval(ctx context.Context, snap project.Snapshot) {
	finalHead := snap.Evidence[protocol.Austin]
	if strings.TrimSpace(finalHead) == "" {
		finalHead = snap.Evidence[protocol.Tony]
	}
	text := fmt.Sprintf(
		"[Duo final approval]\nBoth agents approved the same final integrated HEAD %s. Agent work is complete. Duo Core is now handing that exact artifact back to the repository the user launched Duo from. Do not start new work; no further agent action is required unless delivery reports a problem.",
		shortSHA(finalHead),
	)
	c.broadcastNotice(ctx, text)
}

// notifyDeliveryPending tells both agents that final approval stands but the
// artifact could not be safely applied, so no agent should resume working.
func (c *Coordinator) notifyDeliveryPending(ctx context.Context, record sessionstore.Delivery) {
	branch := strings.TrimSpace(record.TargetBranch)
	text := fmt.Sprintf(
		"[Duo delivery pending]\nFinal approval is complete, but Duo could not safely apply the result to the user's repository.\n\nFinal result:\n  branch: %s\n  head:   %s\n\nReason: %s\n\nNo user files were overwritten. The session stays in INTEGRATE with both signatures preserved. This is a delivery problem for Duo Core and the human, not agent work: do not edit worktrees or sign again. The human resolves the original repository and runs `duo apply`.",
		strings.TrimSpace(record.FinalBranch),
		shortSHA(record.FinalHead),
		record.Reason,
	)
	_ = branch
	c.broadcastNotice(ctx, text)
}

func (c *Coordinator) broadcastNotice(ctx context.Context, text string) {
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		_ = c.server.Send(ctx, agent, protocol.Message{
			Version:   1,
			Type:      protocol.MsgDuoNotice,
			From:      protocol.Duo,
			To:        agent,
			Text:      text,
			Timestamp: time.Now().UnixMilli(),
		})
	}
}
