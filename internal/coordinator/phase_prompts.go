package coordinator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

func (c *Coordinator) broadcastPhaseAdvance(
	ctx context.Context,
	previous, next project.Phase,
	snap project.Snapshot,
	integrationText string,
) {
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		_ = c.sendToAgent(ctx, agent, protocol.Message{
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
	if c.language == "zh" {
		return c.chinesePhasePrompt(agent, previous, next, snap, integrationText)
	}
	if snap.EffectiveMode() == project.ModeFast {
		return c.fastPhasePrompt(previous, next, snap)
	}
	if snap.EffectiveMode() == project.ModeStep {
		return c.stepPhasePrompt(agent, previous, next, snap)
	}
	set := c.workspace.Set()
	own, _ := set.For(agent)
	peer, _ := set.For(protocol.PeerOf(agent))

	switch next {
	case project.PhaseExecute:
		return fmt.Sprintf(
			"[Duo phase transition: %s → EXECUTE]\nBoth agents approved shared plan v%d.\n\n%s\n\nYour private worktree is %s on branch %s. Your peer works independently at %s on %s. Any exploratory edits you already made during PLAN may remain if they fit the approved plan; revise them if peer feedback changed the design. Execute your assigned part, coordinate with duo_send, commit your finished work, and call duo_set_status ready=true only when your worktree is clean.\n\nDuo merges your branch into your peer's at INTEGRATE, so commit only work that is yours. Do not cherry-pick your peer's commits onto your branch: the merge already carries them across, and a cherry-picked copy leaves two commits with the same change in the delivered history, which makes `git revert` ambiguous and `git bisect` unreliable. If you need your peer's code to build or test, read it with git show from your own worktree.",
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
			"[Duo phase transition: %s → REVIEW]\nBoth agents reported execution complete. Cross-review %s's branch %s, current HEAD %s. You may inspect it with git show/diff from your own worktree; do not edit the peer worktree. If you find an issue, use duo_send so the owner can fix and commit it. Your review signature is bound to the exact peer HEAD you reviewed; if that HEAD changes, Duo revokes stale approval automatically. Sign ready=true only with no unresolved objections.%s",
			previous, peerAgent, peer.Branch, shortSHA(peerEvidence),
			c.duplicateCommitNotice(),
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

func (c *Coordinator) chinesePhasePrompt(agent protocol.AgentID, previous, next project.Phase, snap project.Snapshot, integrationText string) string {
	set := c.workspace.Set()
	if snap.EffectiveMode() == project.ModeStep {
		return c.stepPhasePromptZh(agent, previous, next, snap)
	}
	if snap.EffectiveMode() == project.ModeFast {
		if next != project.PhaseDone {
			return fmt.Sprintf("[Duo] FAST 阶段已变为 %s。Austin 负责实现，Tony 独立验证；验证是交付前唯一门槛。", next)
		}
		d := c.CurrentDelivery()
		branch := strings.TrimSpace(d.TargetBranch)
		if branch == "" {
			branch = set.BaseBranch
		}
		applied := shortSHA(d.AppliedHead)
		if applied == "" {
			applied = shortSHA(d.FinalHead)
		}
		verified := shortSHA(snap.Verification.Head)
		if verified == "" {
			verified = shortSHA(d.FinalHead)
		}
		return fmt.Sprintf("[Duo phase transition: %s → DONE]\nTony 已独立验证 Austin 的 HEAD %s。\n\n交付完成：\n  原分支：%s\n  已应用 HEAD：%s\n\n最终结果已交付到用户启动 Duo 的仓库。", previous, verified, branch, applied)
	}
	switch next {
	case project.PhaseExecute:
		own, _ := set.For(agent)
		peer, _ := set.For(protocol.PeerOf(agent))
		return fmt.Sprintf("[Duo phase transition: %s → EXECUTE]\n双方已批准共享计划 v%d。\n\n%s\n\n你的独立 worktree 是 %s（分支 %s），队友在 %s（分支 %s）独立工作。按分工实现，并用 duo_send 协作。完成后提交自己的工作，并且仅在 worktree 干净时调用 duo_set_status ready=true。Duo 会在 INTEGRATE 阶段将你的分支合并到队友分支，因此只提交属于你的工作。不要把队友的提交 cherry-pick 到自己的分支：合并会自动带入这些更改，而 cherry-pick 会让交付历史包含两条内容相同的提交，使 git revert 结果难以判断、git bisect 也不可靠。若需要队友代码来构建或测试，请在自己的 worktree 使用 git show 查看。", previous, snap.PlanVersion, snap.Plan, own.Path, own.Branch, peer.Path, peer.Branch)
	case project.PhaseReview:
		peer := protocol.PeerOf(agent)
		head := snap.Evidence[peer]
		if head == "" {
			if status, err := c.workspace.Status(context.Background(), peer); err == nil {
				head = status.Head
			}
		}
		return fmt.Sprintf("[Duo phase transition: %s → REVIEW]\n双方已报告实现完成。请交叉审查 %s 的分支 %s（当前 HEAD %s）。只能在自己的 worktree 查看，不要修改队友 worktree。发现问题时用 duo_send 通知负责人修复并提交。批准绑定到你审查的精确 HEAD；HEAD 改变后旧批准会自动撤销。没有未解决问题时才调用 ready=true。%s", previous, peer, func() string { w, _ := set.For(peer); return w.Branch }(), shortSHA(head), c.duplicateCommitNotice())
	case project.PhaseIntegrate:
		duty := "在最终批准前检查完整集成树，移除非项目所需的临时文件、草稿和诊断文件；清理后提交。最终 Git tree 就是用户收到的内容。"
		if agent != protocol.Austin {
			duty = "独立检查最终集成分支的正确性和仓库整洁度；发现误入的临时文件或草稿时拒绝批准。"
		}
		return fmt.Sprintf("[Duo phase transition: %s → INTEGRATE]\n交叉审查通过。%s\n\nAustin 的 worktree（%s，分支 %s）负责集成、冲突处理、清理和最终测试；Tony 独立审查最终分支。双方只在同一干净 Austin HEAD 可接受时签署 INTEGRATE。Duo 会把该精确产物交付回用户启动 Duo 的仓库，然后标记 DONE。\n\n%s", previous, integrationText, set.Austin.Path, set.Austin.Branch, duty)
	case project.PhaseDone:
		d := c.CurrentDelivery()
		branch := strings.TrimSpace(d.TargetBranch)
		if branch == "" {
			branch = set.BaseBranch
		}
		head := shortSHA(d.AppliedHead)
		if head == "" {
			head = shortSHA(d.FinalHead)
		}
		return fmt.Sprintf("[Duo phase transition: INTEGRATE → DONE]\n双方已批准最终集成 HEAD %s。\n\n交付完成：\n  原分支：%s\n  已应用 HEAD：%s\n\n最终结果已交付到用户启动 Duo 的仓库。", shortSHA(d.FinalHead), branch, head)
	}
	return "[Duo] 阶段已变为 " + string(next)
}

// fastPhasePrompt renders the only Fast phase notice the coordinator
// broadcasts, VERIFY → DONE. Fast does not use INTEGRATE, so the shared Goal
// text (which talks about dual final approval) would be wrong here.
func (c *Coordinator) fastPhasePrompt(previous, next project.Phase, snap project.Snapshot) string {
	if next != project.PhaseDone {
		return fmt.Sprintf("[Duo] FAST phase changed to %s. Austin drives; Tony independently verifies. Verification is the only gate before delivery to the user's repository.", next)
	}
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
	verified := shortSHA(snap.Verification.Head)
	if verified == "" {
		verified = shortSHA(d.FinalHead)
	}
	return fmt.Sprintf(
		"[Duo phase transition: %s → DONE]\nTony independently verified Austin's HEAD %s.\n\nDelivery complete:\n  original branch: %s\n  applied HEAD:    %s\n\nThe final Duo result is now available in the repository from which the user launched Duo.",
		previous, verified, branch, applied,
	)
}

// stepPhasePrompt is the STEP-mode phase notice. STEP runs Goal's phase chain
// once per step of a queue Duo owns, so every notice names the step under work:
// an agent that has to guess which step is current will work the wrong one. The
// full queue is injected here, once per transition, because a step prompt is the
// one place an agent needs to see what came before and what comes next.
func (c *Coordinator) stepPhasePrompt(
	agent protocol.AgentID,
	previous, next project.Phase,
	snap project.Snapshot,
) string {
	set := c.workspace.Set()
	own, _ := set.For(agent)
	target := strings.TrimSpace(c.CurrentDelivery().TargetBranch)
	if target == "" {
		target = set.BaseBranch
	}
	queue := strings.TrimSpace(snap.Steps.Render())
	if queue == "" {
		queue = "(no queue set: Austin publishes it with duo_set_steps)"
	}
	total := len(snap.Steps)
	cursor := snap.Step
	if cursor > total {
		cursor = total
	}
	title := ""
	if snap.Step >= 1 && snap.Step <= total {
		title = snap.Steps[snap.Step-1].Title
	}
	isFinal := total > 0 && cursor == total

	switch next {
	case project.PhasePlan:
		return fmt.Sprintf(
			"[Duo phase transition: %s → PLAN]\nSTEP mode: the step queue is the contract of this session. Austin publishes the ordered queue with duo_set_steps (a markdown checkbox list) and the prose design with duo_set_plan. Each step must be a committable increment whose test command passes at its own HEAD.\n\nBoth approve the queue with duo_set_status ready=true; Duo advances to EXECUTE only when both have signed, and any queue update resets both approvals, so the new queue must be approved again.\n\nCurrent queue:\n%s\n\nShared plan v%d:\n%s",
			previous, queue, snap.PlanVersion, strings.TrimSpace(snap.Plan),
		)

	case project.PhaseExecute:
		who := "Austin implements this step; Tony stays read-only on the delivered artifact."
		if agent == protocol.Austin {
			who = "Implement exactly this step in your worktree (" + own.Path + ", branch " + own.Branch + "), commit it, keep the worktree clean, then call duo_set_status ready=true. That binds your HEAD and hands the step to Tony for verification. Do not start the next step, and do not tick a box: Duo owns the queue."
		} else {
			who = "You are read-only on the delivered artifact while a step is in flight: do not commit into it, and leave every fix to Austin. Inspect, build and test in your own worktree (" + own.Path + ", branch " + own.Branch + "), which Duo keeps fast-forwarded to the last landed step, and answer Austin with duo_send when he asks."
		}
		return fmt.Sprintf(
			"[Duo phase transition: %s → EXECUTE]\nWorking step %d/%d: %s\n\n%s\n\nQueue:\n%s\n\nEach landed step goes to %s, the branch the user launched Duo from.",
			previous, cursor, total, title, who, queue, target,
		)

	case project.PhaseReview:
		// The submitted artifact lives on the step record, never in Evidence: STEP
		// keeps no per-step signature. Falling back to the worktree would name
		// Austin's LIVE HEAD, so a commit after the submission would tell Tony to
		// review a HEAD that was never submitted — the exact case the server-side
		// rule refuses.
		head := "(none)"
		if snap.Step >= 1 && snap.Step <= total {
			head = shortSHA(snap.Steps[snap.Step-1].Head)
		}
		if agent == protocol.Tony {
			return fmt.Sprintf(
				"[Duo phase transition: %s → REVIEW]\nVerify step %d/%d (%s) at Austin's exact HEAD %s. Inspect it from your own worktree with git show/diff; do not edit Austin's worktree. Check correctness, edge cases, destructive operations, repository hygiene and this step's own requirement.\n\nCall duo_set_verification with result=passed to land this step on %s and advance the queue, or result=issue_found with a concrete note naming the file/behavior and the required fix, which returns the step to EXECUTE. Never pass work you did not actually verify, and report the verdict once: Duo Core relays it to Austin.\n\nQueue:\n%s",
				previous, cursor, total, title, head, target, queue,
			)
		}
		return fmt.Sprintf(
			"[Duo phase transition: %s → REVIEW]\nStep %d/%d (%s) is under Tony's verification at HEAD %s. Do not commit while it is under review: a new HEAD invalidates the verdict. If Tony reports issue_found, fix that concrete issue, commit, and call duo_set_status ready=true again.\n\nQueue:\n%s",
			previous, cursor, total, title, head, queue,
		)

	case project.PhaseIntegrate:
		if isFinal {
			return fmt.Sprintf(
				"[Duo phase transition: %s → INTEGRATE]\nThis is the FINAL step (%d/%d). Duo lands the verified HEAD on %s, and then both agents must sign INTEGRATE with duo_set_status ready=true before the session can reach DONE.\n\nAustin: run the full test command on the integration worktree and remove temporary collaboration artifacts before signing. Tony: independently check the landed result for correctness and repository hygiene, and reject the sign-off if the tree contains artifacts the user did not ask for. Your signature is bound to the exact HEAD you signed; Duo revokes it if that HEAD moves.\n\nQueue:\n%s",
				previous, cursor, total, target, queue,
			)
		}
		return fmt.Sprintf(
			"[Duo phase transition: %s → INTEGRATE]\nTony's verification passed on step %d/%d. Duo is landing it on %s, fast-forwarding Tony's worktree to the landed HEAD, and advancing the queue to step %d. No agent action is needed; the next EXECUTE notice names the next step.\n\nQueue:\n%s",
			previous, cursor, total, target, cursor+1, queue,
		)

	case project.PhaseDone:
		d := c.CurrentDelivery()
		applied := shortSHA(d.AppliedHead)
		if applied == "" {
			applied = shortSHA(d.FinalHead)
		}
		return fmt.Sprintf(
			"[Duo phase transition: INTEGRATE → DONE]\nAll %d steps have landed on %s. Last applied HEAD: %s.\n\nThe queue is complete and the result is in the repository from which the user launched Duo.\n\nFinal queue:\n%s",
			total, target, applied, queue,
		)

	default:
		return "[Duo] Phase changed to " + string(next)
	}
}

// stepPhasePromptZh is the Chinese STEP-mode phase notice. It carries the same
// contract as the English one: the current step is always named, and the queue is
// injected once per transition.
func (c *Coordinator) stepPhasePromptZh(agent protocol.AgentID, previous, next project.Phase, snap project.Snapshot) string {
	set := c.workspace.Set()
	own, _ := set.For(agent)
	target := strings.TrimSpace(c.CurrentDelivery().TargetBranch)
	if target == "" {
		target = set.BaseBranch
	}
	queue := strings.TrimSpace(snap.Steps.Render())
	if queue == "" {
		queue = "（尚未设置队列：Austin 用 duo_set_steps 发布）"
	}
	total := len(snap.Steps)
	cursor := snap.Step
	if cursor > total {
		cursor = total
	}
	title := ""
	if snap.Step >= 1 && snap.Step <= total {
		title = snap.Steps[snap.Step-1].Title
	}
	isFinal := total > 0 && cursor == total

	switch next {
	case project.PhasePlan:
		return fmt.Sprintf(
			"[Duo 阶段变更：%s → PLAN]\nSTEP 模式：步骤队列就是本会话的契约。Austin 用 duo_set_steps 发布有序队列（markdown 复选框列表），并用 duo_set_plan 维护文字设计。每个步骤必须是可提交的增量，其测试命令在该 HEAD 上通过。\n\n双方用 duo_set_status ready=true 批准该队列；只有双方都批准后 Duo 才进入 EXECUTE，任何队列更新都会撤销双方批准，必须重新批准。\n\n当前队列：\n%s\n\n共享计划 v%d：\n%s",
			previous, queue, snap.PlanVersion, strings.TrimSpace(snap.Plan),
		)

	case project.PhaseExecute:
		who := "Austin 实现本步骤；Tony 对交付物保持只读。"
		if agent == protocol.Austin {
			who = "只实现当前这个步骤（worktree " + own.Path + "，分支 " + own.Branch + "），提交并保持 worktree 干净，然后调用 duo_set_status ready=true。这会绑定你的 HEAD 并把该步骤交给 Tony 验证。不要提前开始下一步，也不要自己打勾：队列由 Duo 管理。"
		} else {
			who = "步骤进行中时你对交付物只读：不要提交，所有修复由 Austin 负责。可在自己的 worktree（" + own.Path + "，分支 " + own.Branch + "）检查、构建、测试，Duo 会把它快进到最新落地的 HEAD；Austin 用 duo_send 询问时具体作答。"
		}
		return fmt.Sprintf(
			"[Duo 阶段变更：%s → EXECUTE]\n正在执行步骤 %d/%d：%s\n\n%s\n\n队列：\n%s\n\n每个落地的步骤都会进入 %s，即用户启动 Duo 的分支。",
			previous, cursor, total, title, who, queue, target,
		)

	case project.PhaseReview:
		// 提交的产物记录在步骤条目上，从不在 Evidence 中：STEP 没有逐步签名。
		// 回退到工作区会指向 Austin 的当前 HEAD，从而让 Tony 审查一个从未提交的
		// HEAD——这正是服务端规则会拒绝的情况。
		head := "（无）"
		if snap.Step >= 1 && snap.Step <= total {
			head = shortSHA(snap.Steps[snap.Step-1].Head)
		}
		if agent == protocol.Tony {
			return fmt.Sprintf(
				"[Duo 阶段变更：%s → REVIEW]\n请验证步骤 %d/%d（%s）的 Austin 精确 HEAD %s。只能在自己的 worktree 用 git show/diff 查看，不要修改队友 worktree。检查正确性、边界情况、破坏性操作、仓库整洁度以及该步骤自身的要求。\n\n调用 duo_set_verification：passed 会把该步骤落地到 %s 并推进队列；issue_found 必须附文件/行为及所需修复的具体说明，Duo 会把该步骤退回 EXECUTE。不要批准未实际验证的工作；报告一次即可，Duo Core 会转告 Austin。\n\n队列：\n%s",
				previous, cursor, total, title, head, target, queue,
			)
		}
		return fmt.Sprintf(
			"[Duo 阶段变更：%s → REVIEW]\n步骤 %d/%d（%s）正在由 Tony 验证，HEAD %s。验证期间不要提交：新 HEAD 会使结论失效。若 Tony 报告 issue_found，请修复该具体问题、提交并重新调用 duo_set_status ready=true。\n\n队列：\n%s",
			previous, cursor, total, title, head, queue,
		)

	case project.PhaseIntegrate:
		if isFinal {
			return fmt.Sprintf(
				"[Duo 阶段变更：%s → INTEGRATE]\n这是最后一个步骤（%d/%d）。Duo 会把通过验证的 HEAD 落地到 %s，随后双方必须用 duo_set_status ready=true 签署 INTEGRATE，会话才能进入 DONE。\n\nAustin：签署前在集成 worktree 运行完整测试命令并清理临时协作产物。Tony：独立检查落地结果的正确性与仓库整洁度，若树中包含用户未要求的产物则拒绝签署。签署绑定到你签署的精确 HEAD；HEAD 改变时 Duo 会撤销它。\n\n队列：\n%s",
				previous, cursor, total, target, queue,
			)
		}
		return fmt.Sprintf(
			"[Duo 阶段变更：%s → INTEGRATE]\nTony 已验证通过步骤 %d/%d。Duo 正在把它落地到 %s，将 Tony 的 worktree 快进到落地的 HEAD，并把队列推进到步骤 %d。无需 agent 操作；下一条 EXECUTE 提示会指明下一个步骤。\n\n队列：\n%s",
			previous, cursor, total, target, cursor+1, queue,
		)

	case project.PhaseDone:
		d := c.CurrentDelivery()
		applied := shortSHA(d.AppliedHead)
		if applied == "" {
			applied = shortSHA(d.FinalHead)
		}
		return fmt.Sprintf(
			"[Duo 阶段变更：INTEGRATE → DONE]\n全部 %d 个步骤已落地到 %s。最后应用的 HEAD：%s。\n\n队列已完成，结果已交付到用户启动 Duo 的仓库。\n\n最终队列：\n%s",
			total, target, applied, queue,
		)

	default:
		return "[Duo] 阶段已变为 " + string(next)
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
	if c.language == "zh" {
		c.broadcastNotice(ctx, fmt.Sprintf("[Duo final approval]\n双方已批准相同的最终集成 HEAD %s。Agent 工作已完成。Duo Core 正在将该精确产物交付回用户启动 Duo 的仓库。请勿开始新工作；除非交付报告问题，否则不需要进一步操作。", shortSHA(finalHead)))
		return
	}
	text := fmt.Sprintf(
		"[Duo final approval]\nBoth agents approved the same final integrated HEAD %s. Agent work is complete. Duo Core is now handing that exact artifact back to the repository the user launched Duo from. Do not start new work; no further agent action is required unless delivery reports a problem.",
		shortSHA(finalHead),
	)
	c.broadcastNotice(ctx, text)
}

// notifyDeliveryPending tells both agents that approval stands but the
// artifact could not be safely applied, so no agent should resume working.
func (c *Coordinator) notifyDeliveryPending(ctx context.Context, record sessionstore.Delivery) {
	if c.language == "zh" {
		mode := c.project.Snapshot().EffectiveMode()
		phase, action := "INTEGRATE", "不要编辑 worktree 或重新签署"
		if mode == project.ModeFast {
			phase, action = "VERIFY", "不要编辑 worktree 或重新验证"
		}
		c.broadcastNotice(ctx, fmt.Sprintf("[Duo delivery pending]\n最终批准已完成，但 Duo 无法安全地将结果应用到用户仓库。\n\n最终结果：\n  分支：%s\n  HEAD：%s\n\n原因：%s\n\n没有覆盖用户文件。会话保留在 %s，已保存的批准仍有效。这是 Duo Core 与用户需要处理的交付问题，不是 agent 工作：%s。请用户修复原仓库状态后运行 `duo apply`。", strings.TrimSpace(record.FinalBranch), shortSHA(record.FinalHead), record.Reason, phase, action))
		return
	}
	final := fmt.Sprintf(
		"[Duo delivery pending]\nFinal approval is complete, but Duo could not safely apply the result to the user's repository.\n\nFinal result:\n  branch: %s\n  head:   %s\n\nReason: %s\n\nNo user files were overwritten. The session stays in INTEGRATE with both signatures preserved. This is a delivery problem for Duo Core and the human, not agent work: do not edit worktrees or sign again. The human resolves the original repository and runs `duo apply`.",
		strings.TrimSpace(record.FinalBranch),
		shortSHA(record.FinalHead),
		record.Reason,
	)
	if c.project.Snapshot().EffectiveMode() == project.ModeFast {
		final = fmt.Sprintf(
			"[Duo delivery pending]\nVerification passed, but Duo could not safely apply the result to the user's repository.\n\nFinal result:\n  branch: %s\n  head:   %s\n\nReason: %s\n\nNo user files were overwritten. The session stays in VERIFY with the verification preserved. This is a delivery problem for Duo Core and the human, not agent work: do not edit worktrees or re-verify. The human resolves the original repository and runs `duo apply`.",
			strings.TrimSpace(record.FinalBranch),
			shortSHA(record.FinalHead),
			record.Reason,
		)
	}
	c.broadcastNotice(ctx, final)
}

func (c *Coordinator) broadcastNotice(ctx context.Context, text string) {
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		_ = c.sendToAgent(ctx, agent, protocol.Message{
			Version:   1,
			Type:      protocol.MsgDuoNotice,
			From:      protocol.Duo,
			To:        agent,
			Text:      text,
			Timestamp: time.Now().UnixMilli(),
		})
	}
}

// duplicateCommitNotice reports commits that both agent branches carry, which is
// what a cherry-pick leaves behind. It runs at the EXECUTE → REVIEW transition
// because that is the last point where no signature is bound to either branch, so
// an agent can still drop the copy from its own history without revoking
// anything. Detection is best effort: a failure to inspect the repository must
// not stop the session from advancing.
func (c *Coordinator) duplicateCommitNotice() string {
	dupes, err := c.workspace.DuplicateCommits(context.Background())
	if err != nil || len(dupes) == 0 {
		return ""
	}
	if c.language == "zh" {
		var b strings.Builder
		fmt.Fprintf(&b, "\n\n[Duo：两条分支都包含 %d 项相同改动]\n", len(dupes))
		b.WriteString("Duo 会在 INTEGRATE 阶段将你的分支合并到队友分支，因此因 cherry-pick 队友工作而产生的重复提交是多余的。它会让 git revert 的结果难以判断，也会让 git bisect 遇到不改变内容的提交。\n\n")
		for _, duplicate := range dupes {
			b.WriteString("两条分支都提交了同一项改动：\n")
			for _, side := range duplicate.Sides {
				fmt.Fprintf(&b, "  %-6s  %-10s  %s\n", side.Agent, shortSHA(side.SHA), side.Subject)
			}
			b.WriteString("\n")
		}
		b.WriteString("如果其中一项是 cherry-pick，请执行该操作的 agent 在签署 REVIEW 前从自己的分支删除对应提交（例如用交互式 rebase 删除提交）；这只改变历史，不改变文件树。Git 不记录相同补丁的来源，因此 Duo 无法判断应由谁删除；如果双方独立完成了相同改动，请说明原因并保留两边提交。Duo 不会改写 agent 分支；若删除提交会丢失真实工作，请保留提交并说明情况。此时尚未绑定任何签名，是处理重复提交的最佳时机。")
		return b.String()
	}
	return workspace.DuplicateCommitNotice(dupes)
}
