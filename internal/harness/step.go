package harness

import (
	"context"
	"fmt"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// maybeWakeStep is the STEP-mode watchdog. STEP is not Goal: every phase has a
// per-step policy, and a FAST step in REVIEW needs Tony, not Austin. Nudging the
// wrong agent turns one verification wait into an unnecessary round trip through
// the peer.
func (m *Monitor) maybeWakeStep(ctx context.Context) {
	snap := m.project.Snapshot()
	if !snap.Started || snap.Phase == project.PhaseDone {
		return
	}
	targets := stepNudgeTargets(snap)
	if len(targets) == 0 {
		return
	}

	now := time.Now()
	var idle []protocol.AgentID
	for _, agent := range targets {
		if !m.sender.IsAgentConnected(agent) {
			continue
		}
		rt := m.tracker.Snapshot(agent)
		if rt.HumanAttached {
			continue
		}
		last := rt.LastActivity
		if snap.LastMutation.After(last) {
			last = snap.LastMutation
		}
		quietFor := now.Sub(last)
		active := rt.Busy || rt.ProviderActive || rt.ToolDepth > 0
		hardStalled := quietFor >= m.cfg.StallThreshold
		if !hardStalled && (active || quietFor < m.cfg.IdleThreshold) {
			continue
		}
		idle = append(idle, agent)
	}
	if len(idle) == 0 {
		return
	}

	m.mu.Lock()
	if !m.lastWake.IsZero() && now.Sub(m.lastWake) < m.cfg.Cooldown {
		m.mu.Unlock()
		return
	}
	m.lastWake = now
	m.mu.Unlock()

	for _, agent := range idle {
		text := m.stepNudgePrompt(snap, agent)
		if text == "" {
			continue
		}
		m.tracker.Touch(agent)
		if m.bus != nil {
			m.bus.Emit(events.Event{Kind: events.KindHarness, Agent: agent, Text: "nudging " + string(agent) + " for the current STEP action"})
		}
		m.sendHarnessPrompt(ctx, agent, text)
	}
}

// stepNudgeTargets returns the agents whose next action is required by the
// current STEP phase. A target that is already ready is omitted: the harness
// should not ask an agent to repeat a signature it already supplied.
func stepNudgeTargets(snap project.Snapshot) []protocol.AgentID {
	missing := func() []protocol.AgentID {
		var out []protocol.AgentID
		for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
			if !snap.Ready[agent] {
				out = append(out, agent)
			}
		}
		return out
	}

	switch snap.Phase {
	case project.PhasePlan:
		return missing()
	case project.PhaseExecute:
		if stepPolicy(snap) == project.ModeGoal {
			return missing()
		}
		return []protocol.AgentID{protocol.Austin}
	case project.PhaseReview:
		if stepPolicy(snap) == project.ModeGoal {
			return missing()
		}
		return []protocol.AgentID{protocol.Tony}
	case project.PhaseIntegrate:
		// The final step always needs both signatures. A FAST non-final step in
		// INTEGRATE is already being landed by Duo Core, so no agent should be
		// asked to move.
		if snap.Step >= len(snap.Steps) || stepPolicy(snap) == project.ModeGoal {
			return missing()
		}
	}
	return nil
}

func (m *Monitor) stepNudgePrompt(snap project.Snapshot, agent protocol.AgentID) string {
	title := ""
	if step, ok := currentStep(snap); ok {
		title = step.Title
	}
	num, total := snap.Step, len(snap.Steps)
	if num < 1 {
		num = 1
	}
	policy := stepPolicy(snap).Display()
	label := "STEP"
	if total > 0 && num >= 1 {
		label = fmt.Sprintf("STEP %d/%d [%s] %q", num, total, policy, title)
	}

	var en, zh string
	switch snap.Phase {
	case project.PhasePlan:
		en = "[Duo Harness]\n" + label + " is in PLAN. Inspect duo_status, co-design the plan and step queue with the other agent, then approve the current plan with duo_set_status ready=true. Do not merely acknowledge this message."
		zh = "[Duo Harness]\n" + label + " 处于 PLAN。先用 duo_status 查看状态，与另一个 Agent 共同确定计划和步骤队列，然后调用 duo_set_status ready=true 批准当前计划。不要只回复收到。"
	case project.PhaseExecute:
		if stepPolicy(snap) == project.ModeGoal {
			en = "[Duo Harness]\n" + label + " is in EXECUTE. Implement in your own worktree, commit when clean, and sign EXECUTE with duo_set_status ready=true. Both agents must finish this joint step. Do not merely acknowledge this message."
			zh = "[Duo Harness]\n" + label + " 处于 EXECUTE。请在你自己的 worktree 实现，工作区干净后提交，并用 duo_set_status ready=true 签署 EXECUTE。这个 GOAL 步骤需要双方完成。不要只回复收到。"
		} else {
			en = "[Duo Harness]\n" + label + " is in EXECUTE. Austin: implement in your worktree, commit when clean, then call duo_set_status ready=true with the committed HEAD. Tony is only the verifier for this FAST step; do not wait for him. Do not merely acknowledge this message."
			zh = "[Duo Harness]\n" + label + " 处于 EXECUTE。Austin：在 worktree 实现，工作区干净后提交，然后用 duo_set_status ready=true 提交该 HEAD。这个 FAST 步骤中 Tony 只负责验证，不要等他。不要只回复收到。"
		}
	case project.PhaseReview:
		if stepPolicy(snap) == project.ModeGoal {
			en = "[Duo Harness]\n" + label + " is in REVIEW. Cross-review the peer's exact HEAD, then sign REVIEW with duo_set_status ready=true. Do not merely acknowledge this message."
			zh = "[Duo Harness]\n" + label + " 处于 REVIEW。请交叉审查对方精确的 HEAD，然后用 duo_set_status ready=true 签署 REVIEW。不要只回复收到。"
		} else {
			en = "[Duo Harness]\n" + label + " is in REVIEW. Tony: inspect Austin's exact HEAD and report result=passed, or result=issue_found with a concrete note, using duo_set_verification. This FAST step is single-writer: do not edit or commit the artifact. Do not merely acknowledge this message."
			zh = "[Duo Harness]\n" + label + " 处于 REVIEW。Tony：请独立检查 Austin 精确的 HEAD，并用 duo_set_verification 报告 result=passed，或 result=issue_found 并附具体说明。这个 FAST 步骤只有 Austin 可以写入：不要编辑或提交产物。不要只回复收到。"
		}
	case project.PhaseIntegrate:
		if snap.Step >= len(snap.Steps) {
			en = "[Duo Harness]\nThe STEP queue is finished. Both agents must sign the finished queue with duo_set_status ready=true before Duo delivers it. Do not merely acknowledge this message."
			zh = "[Duo Harness]\nSTEP 步骤队列已完成。双方都需要用 duo_set_status ready=true 签署完成队列，Duo 才会交付。不要只回复收到。"
		} else if stepPolicy(snap) == project.ModeGoal {
			en = "[Duo Harness]\n" + label + " is in INTEGRATE. Review the integrated Austin HEAD, then sign INTEGRATE with duo_set_status ready=true. Do not merely acknowledge this message."
			zh = "[Duo Harness]\n" + label + " 处于 INTEGRATE。请审查集成后的 Austin HEAD，然后用 duo_set_status ready=true 签署 INTEGRATE。不要只回复收到。"
		} else {
			return ""
		}
	default:
		return ""
	}
	if m.cfg.Language == "zh" {
		return zh + "\n\n" + snap.NudgeState()
	}
	return en + "\n\n" + snap.NudgeState()
}

func stepPolicy(snap project.Snapshot) project.Mode {
	if step, ok := currentStep(snap); ok {
		return step.EffectivePolicy()
	}
	return project.ModeFast
}

func currentStep(snap project.Snapshot) (project.Step, bool) {
	if snap.Step >= 1 && snap.Step <= len(snap.Steps) {
		return snap.Steps[snap.Step-1], true
	}
	return project.Step{}, false
}
