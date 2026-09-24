package coordinator

import (
	"fmt"
	"strings"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// ResumePrompt renders a new, authoritative instruction rather than relying
// on what an old Pi conversation happens to remember.
func (c *Coordinator) ResumePrompt(agent protocol.AgentID) string {
	return c.resumePrompt(agent, c.project.Snapshot())
}

func (c *Coordinator) resumePrompt(agent protocol.AgentID, snap project.Snapshot) string {
	set := c.workspace.Set()
	own, _ := set.For(agent)
	peer := protocol.PeerOf(agent)
	plan := strings.TrimSpace(snap.Plan)
	if plan == "" {
		plan = "(none yet)"
	}
	scope := strings.TrimSpace(set.ScopePath)
	if scope == "" {
		scope = "."
	}

	header := fmt.Sprintf("[Duo session resumed]\n\nThe previous Duo session has been restored.\n\nCurrent phase: %s\nShared plan version: v%d\nAustin ready: %t\nTony ready: %t\nCurrent worktree: %s (%s)\nWorking-directory scope: %s\n\nShared plan:\n%s\n\nFirst inspect duo_status and the existing Git state. Do not restart the task from scratch or discard valid existing work.",
		snap.Phase, snap.PlanVersion, snap.Ready[protocol.Austin], snap.Ready[protocol.Tony], own.Path, own.Branch, scope, plan)

	switch snap.Phase {
	case project.PhasePlan:
		if agent == protocol.Austin {
			return header + "\n\nRe-enter the current collaboration state now. Continue coordinating the shared plan; use duo_send for substantive plan discussion if needed, and sign only when you approve the exact current plan. Do not assume Tony is already active. Do not merely acknowledge this message. Take the next concrete action required by PLAN."
		}
		return header + "\n\nRe-enter the current collaboration state now. Independently review the current plan, challenge missing assumptions or incomplete requirements, and sign only when you genuinely approve it. Do not merely acknowledge this message. Take the next concrete action required by PLAN."

	case project.PhaseExecute:
		return header + "\n\nContinue your assigned EXECUTE work from the existing worktree state. Coordinate with your peer via duo_send if interfaces, assumptions, or responsibilities need clarification. When your assigned work is genuinely complete, commit it, keep the worktree clean, and call duo_set_status ready=true. Do not merely acknowledge this message. Resume the next concrete action."

	case project.PhaseReview:
		return fmt.Sprintf("%s\n\nResume the existing cross-review. Inspect %s's current branch/HEAD, not a remembered version. If you find an issue, use duo_send, let the owner fix and commit it, then re-review the new HEAD. Only sign ready=true when there are no unresolved objections. Do not merely acknowledge this message.", header, peer)

	case project.PhaseIntegrate:
		if agent == protocol.Austin {
			return header + "\n\nResume final integration validation. Inspect the current Austin integration worktree, resolve remaining integration issues, clean temporary artifacts, and run final tests. Do not redo the whole task. Do not merely acknowledge this message. Take the next concrete INTEGRATE action."
		}
		return header + "\n\nResume independent review of Austin's current integrated HEAD. Reject final approval if correctness or repository hygiene is not acceptable. Do not merely acknowledge this message. Take the next concrete INTEGRATE action."
	default:
		return header + "\n\nDo not merely acknowledge this message. Take the next concrete action required by the current phase."
	}
}
