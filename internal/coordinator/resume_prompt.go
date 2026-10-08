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
