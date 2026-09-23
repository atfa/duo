package coordinator

import (
	"context"
	"fmt"
	"time"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
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
		return fmt.Sprintf(
			"[Duo phase transition: %s → INTEGRATE]\nCross-review passed. %s\n\nAustin's worktree (%s, branch %s) is the integration worktree. Austin owns conflict resolution and final test execution. Tony independently reviews the final integrated branch. Both agents sign INTEGRATE only when the same clean Austin HEAD is acceptable; stale signatures are revoked if that HEAD changes.",
			previous, integrationText, set.Austin.Path, set.Austin.Branch,
		)

	case project.PhaseDone:
		status, _ := c.workspace.Status(context.Background(), protocol.Austin)
		return fmt.Sprintf(
			"[Duo phase transition: INTEGRATE → DONE]\nBoth agents approved the same final integrated HEAD %s on branch %s. The Duo task is complete. The original user branch has NOT been modified automatically; the deliverable remains on the Duo integration branch for the human to merge when desired.",
			shortSHA(status.Head), set.Austin.Branch,
		)

	default:
		return "[Duo] Phase changed to " + string(next)
	}
}
