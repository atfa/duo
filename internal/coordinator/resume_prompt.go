package coordinator

import (
	"fmt"
	"strings"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// stepResumePrompt re-injects the queue an agent has no memory of. A STEP
// session can be resumed by a different Duo process on a new binary, so the
// queue, the cursor and the current step's title are the whole contract: without
// them an agent restarts a step that already landed.
func (c *Coordinator) stepResumePrompt(agent protocol.AgentID, snap project.Snapshot) string {
	if c.language == "zh" {
		return c.stepResumePromptZh(agent, snap)
	}
	set := c.workspace.Set()
	own, _ := set.For(agent)
	queue := strings.TrimSpace(snap.Steps.Render())
	if queue == "" {
		queue = "(no queue set: Austin publishes it with duo_set_steps)"
	}
	total := len(snap.Steps)
	cursor := snap.Step
	if cursor > total {
		cursor = total
	}
	title := "(none)"
	if snap.Step >= 1 && snap.Step <= total {
		title = snap.Steps[snap.Step-1].Title
	}
	scope := strings.TrimSpace(set.ScopePath)
	if scope == "" {
		scope = "."
	}

	header := fmt.Sprintf(
		"[Duo session resumed]\n\nThe previous Duo session has been restored.\n\nCurrent phase: %s\nStep: %d of %d — %s\nShared plan version: v%d\nAustin ready: %t\nTony ready: %t\nCurrent worktree: %s (%s)\nWorking-directory scope: %s\n\nStep queue (Duo owns the ticks; never edit them by hand):\n%s\n\nFirst inspect duo_status and the existing Git state. Do not restart the task from scratch, do not re-land a step that is already ticked, and do not discard valid existing work.",
		snap.Phase, cursor, total, title, snap.PlanVersion,
		snap.Ready[protocol.Austin], snap.Ready[protocol.Tony],
		own.Path, own.Branch, scope, queue,
	)

	switch snap.Phase {
	case project.PhasePlan:
		return header + "\n\nRe-enter the current collaboration state now. Continue co-designing the queue and the prose plan, use duo_send for substantive discussion, and sign only the exact queue version you approve. Do not merely acknowledge this message. Take the next concrete action required by PLAN."
	case project.PhaseExecute:
		if agent == protocol.Austin {
			return header + "\n\nContinue the current step from the existing worktree state: do not restart it and do not start the next one. When it is genuinely complete, commit it, keep the worktree clean, and call duo_set_status ready=true. Do not merely acknowledge this message. Resume the next concrete action."
		}
		return header + "\n\nYou are read-only on the delivered artifact while a step is in flight. Inspect, build and test in your own worktree, which Duo keeps fast-forwarded to the last landed step, and answer Austin with duo_send when he asks. Do not merely acknowledge this message."
	case project.PhaseReview:
		return header + "\n\nResume the verification of the current step. Inspect Austin's CURRENT HEAD, not a remembered one, then report with duo_set_verification: passed lands the step and advances the queue, issue_found returns it to EXECUTE with a concrete note. Do not merely acknowledge this message."
	case project.PhaseIntegrate:
		if total > 0 && cursor == total {
			return header + "\n\nThis is the final step. Validate the landed result and sign INTEGRATE with duo_set_status ready=true only when the tree is acceptable; Duo revokes a signature whose HEAD moved. Do not redo the whole task. Do not merely acknowledge this message."
		}
		return header + "\n\nA step landing may have been interrupted. Duo reconciles the delivery itself: do not land a step by hand and do not start new work until the next EXECUTE notice names the next step."
	default:
		return header + "\n\nDo not merely acknowledge this message. Take the next concrete action required by the current phase."
	}
}

func (c *Coordinator) stepResumePromptZh(agent protocol.AgentID, snap project.Snapshot) string {
	set := c.workspace.Set()
	own, _ := set.For(agent)
	queue := strings.TrimSpace(snap.Steps.Render())
	if queue == "" {
		queue = "（尚未设置队列：Austin 用 duo_set_steps 发布）"
	}
	total := len(snap.Steps)
	cursor := snap.Step
	if cursor > total {
		cursor = total
	}
	title := "（无）"
	if snap.Step >= 1 && snap.Step <= total {
		title = snap.Steps[snap.Step-1].Title
	}
	scope := strings.TrimSpace(set.ScopePath)
	if scope == "" {
		scope = "."
	}

	header := fmt.Sprintf(
		"[Duo 会话已恢复]\n\n上一个 Duo 会话已恢复。\n\n当前阶段：%s\n步骤：第 %d/%d 步 — %s\n共享计划版本：v%d\nAustin 就绪：%t\nTony 就绪：%t\n当前 worktree：%s（%s）\n工作目录范围：%s\n\n步骤队列（打勾由 Duo 维护，不要手工修改）：\n%s\n\n请先检查 duo_status 和现有 Git 状态。不要从头重做任务，不要重复落地已打勾的步骤，也不要丢弃有效的现有工作。",
		snap.Phase, cursor, total, title, snap.PlanVersion,
		snap.Ready[protocol.Austin], snap.Ready[protocol.Tony],
		own.Path, own.Branch, scope, queue,
	)

	switch snap.Phase {
	case project.PhasePlan:
		return header + "\n\n请重新进入当前协作状态：继续共同设计队列与文字计划，必要时用 duo_send 实质讨论，并只对你确实批准的队列版本签署。不要仅仅确认本消息。"
	case project.PhaseExecute:
		if agent == protocol.Austin {
			return header + "\n\n基于现有 worktree 继续当前步骤：不要重做它，也不要开始下一步。确实完成后提交、保持 worktree 干净，并调用 duo_set_status ready=true。不要仅仅确认本消息。"
		}
		return header + "\n\n步骤进行中时你对交付物只读。可在自己的 worktree 检查、构建、测试（Duo 会把它快进到最新落地的 HEAD），Austin 询问时具体作答。不要仅仅确认本消息。"
	case project.PhaseReview:
		return header + "\n\n继续验证当前步骤。请检查 Austin 当前的 HEAD，而不是记忆中的版本，然后用 duo_set_verification 报告：passed 落地该步骤并推进队列；issue_found 附具体说明并退回 EXECUTE。不要仅仅确认本消息。"
	case project.PhaseIntegrate:
		if total > 0 && cursor == total {
			return header + "\n\n这是最后一个步骤。验证落地结果后，仅在其可接受时用 duo_set_status ready=true 签署 INTEGRATE；HEAD 变动时 Duo 会撤销签署。不要重做整个任务。不要仅仅确认本消息。"
		}
		return header + "\n\n某次步骤落地可能被中断。交付由 Duo 自行调节：不要手工落地步骤，在下一条 EXECUTE 提示指明下一步之前不要开始新工作。"
	default:
		return header + "\n\n不要仅仅确认本消息。请执行当前阶段要求的下一个具体动作。"
	}
}

// ResumePrompt renders a new, authoritative instruction rather than relying
// on what an old Pi conversation happens to remember.
func (c *Coordinator) ResumePrompt(agent protocol.AgentID) string {
	return c.resumePrompt(agent, c.project.Snapshot())
}

func (c *Coordinator) resumePrompt(agent protocol.AgentID, snap project.Snapshot) string {
	// STEP is checked before the language: its resume prompt is the one surface
	// that must re-inject the queue, and a language dispatch that ran first handed
	// a STEP session the Goal wake, which never mentions the queue.
	if snap.EffectiveMode() == project.ModeStep {
		return c.stepResumePrompt(agent, snap)
	}
	if c.language == "zh" {
		return c.chineseResumePrompt(agent, snap)
	}
	if snap.EffectiveMode() == project.ModeFast {
		return c.fastResumePrompt(agent, snap)
	}
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

func (c *Coordinator) chineseResumePrompt(agent protocol.AgentID, snap project.Snapshot) string {
	set := c.workspace.Set()
	own, _ := set.For(agent)
	scope := strings.TrimSpace(set.ScopePath)
	if scope == "" {
		scope = "."
	}
	if snap.EffectiveMode() == project.ModeFast {
		role := "主驱动"
		if agent == protocol.Tony {
			role = "副驾驶 / 独立验证者"
		}
		target := shortSHA(snap.Verification.Head)
		if target == "" {
			target = "（无）"
		}
		header := fmt.Sprintf("[Duo session resumed]\n\n之前的 Duo FAST 会话已恢复。\n\n模式：FAST\n当前阶段：%s\n你的角色：%s\n验证状态：%s\n验证目标：%s\n当前 worktree：%s（%s）\n工作目录范围：%s\n\n请先检查 duo_status 和现有 Git 状态。不要从头重做任务，也不要丢弃有效工作。FAST 模式仅 Austin 写入：只有 Austin 可以提交；Tony 不编辑、不提交，只向 Austin 报告问题。", snap.Phase, role, snap.Verification.Label(), target, own.Path, own.Branch, scope)
		switch snap.Phase {
		case project.PhaseRunning:
			if agent == protocol.Austin {
				return header + "\n\n从现有 worktree 状态继续推进任务。Tony 思考时可以继续工作；Tony 的建议仅供参考。工作真实完成、已提交且 worktree 干净后，调用 duo_set_status ready=true 请求验证。"
			}
			return header + "\n\n作为独立验证者待命。不要提交或编辑产物。Austin 通过 duo_send 请求建议或检查时简要回复，否则等待；Austin 请求验证时 Duo 会唤醒你。"
		case project.PhaseVerify:
			if agent == protocol.Austin {
				return header + "\n\n你已请求验证，正在等待。验证期间不要更改被验证的 HEAD；若必须更改，Duo 会撤销请求并返回 RUNNING。"
			}
			return fmt.Sprintf("%s\n\nAustin 请求验证 HEAD %s。请独立检查该精确产物，再使用 duo_set_verification 给出结构化结论：result=passed，或 result=issue_found 并附具体说明。不要提交或编辑产物；需要修复时报告问题，由 Austin 修改。", header, shortSHA(snap.Verification.Head))
		case project.PhaseDone:
			return header + "\n\nFAST 会话已完成，结果已交付到用户仓库。不需要进一步操作。"
		}
	}
	plan := strings.TrimSpace(snap.Plan)
	if plan == "" {
		plan = "（尚无）"
	}
	header := fmt.Sprintf("[Duo session resumed]\n\n之前的 Duo 会话已恢复。\n\n当前阶段：%s\n共享计划版本：v%d\nAustin 已就绪：%t\nTony 已就绪：%t\n当前 worktree：%s（%s）\n工作目录范围：%s\n\n共享计划：\n%s\n\n请先检查 duo_status 和现有 Git 状态。不要从头重做任务，也不要丢弃有效工作。", snap.Phase, snap.PlanVersion, snap.Ready[protocol.Austin], snap.Ready[protocol.Tony], own.Path, own.Branch, scope, plan)
	switch snap.Phase {
	case project.PhasePlan:
		if agent == protocol.Austin {
			return header + "\n\n重新进入当前协作状态并继续完善共享计划；必要时用 duo_send 讨论实质内容。仅在批准当前精确计划后签署。不要假定 Tony 已经开始，也不要只回复收到；立即执行 PLAN 阶段下一项具体工作。"
		}
		return header + "\n\n独立审查当前计划，指出遗漏的假设或需求；仅在确实批准后签署。不要只回复收到；立即执行 PLAN 阶段下一项具体工作。"
	case project.PhaseExecute:
		return header + "\n\n从现有 worktree 继续负责的 EXECUTE 工作。接口、假设或分工需要澄清时通过 duo_send 协调。工作真实完成后提交、保持 worktree 干净，并调用 duo_set_status ready=true。不要只回复收到；继续下一项具体工作。"
	case project.PhaseReview:
		return fmt.Sprintf("%s\n\n继续现有交叉审查。检查 %s 当前分支和 HEAD，不要依赖记忆中的版本。发现问题时使用 duo_send，等待负责人修复并提交，然后重新审查新 HEAD。没有未解决问题时才调用 ready=true。不要只回复收到。", header, protocol.PeerOf(agent))
	case project.PhaseIntegrate:
		if agent == protocol.Austin {
			return header + "\n\n继续最终集成验证。检查 Austin 当前集成 worktree，解决剩余问题，清理临时文件并运行最终测试。不要重做整个任务，也不要只回复收到；继续 INTEGRATE 阶段下一项具体工作。"
		}
		return header + "\n\n独立审查 Austin 当前集成 HEAD。若正确性或仓库整洁度不可接受，拒绝最终批准。不要只回复收到；继续 INTEGRATE 阶段下一项具体工作。"
	}
	return header + "\n\n不要只回复收到；执行当前阶段下一项具体工作。"
}

// fastResumePrompt restores a Fast session without Goal-only plan/sign-off
// noise. It re-states the single-writer boundary because that is the invariant
// that makes Fast delivery sound: only Austin's HEAD is ever delivered.
func (c *Coordinator) fastResumePrompt(agent protocol.AgentID, snap project.Snapshot) string {
	set := c.workspace.Set()
	own, _ := set.For(agent)
	scope := strings.TrimSpace(set.ScopePath)
	if scope == "" {
		scope = "."
	}
	role := "DRIVER"
	if agent != protocol.Austin {
		role = "COPILOT / independent verifier"
	}
	target := shortSHA(snap.Verification.Head)
	if strings.TrimSpace(snap.Verification.Head) == "" {
		target = "(none)"
	}
	header := fmt.Sprintf("[Duo session resumed]\n\nThe previous Duo FAST session has been restored.\n\nMode: FAST\nCurrent phase: %s\nYour role: %s\nVerification: %s\nVerified target: %s\nCurrent worktree: %s (%s)\nWorking-directory scope: %s\n\nFirst inspect duo_status and the existing Git state. Do not restart the task from scratch or discard valid existing work. Fast mode is single-writer: only Austin commits; Tony never edits or commits and reports issues to Austin.",
		snap.Phase, role, snap.Verification.Label(), target, own.Path, own.Branch, scope)

	switch snap.Phase {
	case project.PhaseRunning:
		if agent == protocol.Austin {
			return header + "\n\nContinue driving the task from the existing worktree state. Work ahead while Tony is thinking; Tony's advice is advisory. When the work is genuinely complete and your worktree is clean and committed, request verification with duo_set_status ready=true."
		}
		return header + "\n\nStay available as the independent verifier. Do not commit or edit the delivered artifact. If Austin asks for advice or a check via duo_send, respond concisely; otherwise wait. Duo will wake you when Austin requests verification."

	case project.PhaseVerify:
		if agent == protocol.Austin {
			return header + "\n\nYou requested verification and are waiting. Do not change the verified HEAD while verification is in flight; if you must change it, Duo revokes the request and returns to RUNNING."
		}
		return fmt.Sprintf("%s\n\nAustin requested verification of HEAD %s. Independently inspect that exact artifact, then report a structured verdict with duo_set_verification: result=passed, or result=issue_found with a concrete note. Do not commit or edit the artifact; if a fix is needed, report the issue so Austin applies it.", header, shortSHA(snap.Verification.Head))

	case project.PhaseDone:
		return header + "\n\nThe FAST session is DONE: the result was delivered to the user's repository. No further agent action is required."

	default:
		return header + "\n\nDo not merely acknowledge this message. Take the next concrete action required by the current phase."
	}
}
