package coordinator

import (
	"context"
	"fmt"
	"strings"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/recovery"
	"github.com/atfa/duo/internal/transport"
)

// handleStepSetStatus is the STEP-mode gate. Per step it is the Fast shape —
// Austin submits a committed HEAD, Tony's verdict is the independent check — and
// the Goal sign-off is kept for the two moments where two agents must agree: the
// queue in PLAN, and the finished queue at the end.
func (c *Coordinator) handleStepSetStatus(ctx context.Context, client *transport.Client, message protocol.Message) {
	if message.Ready == nil {
		_ = c.respond(ctx, client, message.RequestID, false, "ready must be true or false", c.statusText(ctx))
		return
	}

	snap := c.project.Snapshot()
	switch snap.Phase {
	case project.PhasePlan:
		c.stepPlanApproval(ctx, client, message)
		return
	case project.PhaseExecute:
		if client.Identity() != protocol.Austin {
			_ = c.respond(ctx, client, message.RequestID, false,
				"in STEP mode Tony checks a step with duo_set_verification, not with a sign-off", c.statusText(ctx))
			return
		}
		if !*message.Ready {
			_ = c.respond(ctx, client, message.RequestID, false, "a step review request cannot be withdrawn from EXECUTE", c.statusText(ctx))
			return
		}
		if err := c.runTestGateIfSet(ctx); err != nil {
			_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
			return
		}
	case project.PhaseReview:
		if client.Identity() != protocol.Austin {
			_ = c.respond(ctx, client, message.RequestID, false,
				"the step is under review; Tony reports with duo_set_verification", c.statusText(ctx))
			return
		}
	case project.PhaseIntegrate:
		// The finished queue is the only thing signed at INTEGRATE.
		if project.QueueFinished(snap.Steps) {
			c.stepQueueSignOff(ctx, client, message)
			return
		}
		_ = c.respond(ctx, client, message.RequestID, false,
			fmt.Sprintf("step %d of %d is being delivered; sign-off is only for the finished queue", snap.Step, len(snap.Steps)),
			c.statusText(ctx))
		return
	case project.PhaseDone:
		_ = c.respond(ctx, client, message.RequestID, false, project.ErrProjectDone.Error(), c.statusText(ctx))
		return
	default:
		_ = c.respond(ctx, client, message.RequestID, false, "STEP mode does not run in phase "+string(snap.Phase), c.statusText(ctx))
		return
	}

	head, err := recovery.FastVerificationTarget(ctx, c.workspace)
	if err != nil {
		_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
		return
	}
	snap, tr, err := c.project.SetReady(client.Identity(), *message.Ready, message.Note, head)
	if err != nil {
		_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
		return
	}
	c.tracker.Touch(client.Identity())
	c.recordEvent("step_submitted", map[string]any{"step": snap.Step, "head": head, "phase": string(tr.Next)})
	if tr.Advanced {
		c.recordEvent("phase_transition", map[string]any{"from": string(tr.Previous), "to": string(tr.Next)})
	}
	_ = c.respond(ctx, client, message.RequestID, true,
		fmt.Sprintf("Step %d of %d submitted at %s for review.", snap.Step, len(snap.Steps), shortSHA(head)),
		c.statusText(ctx))
	c.notifyStepReviewRequested(ctx, snap)
}

// stepPlanApproval is the queue's dual approval: the queue is the PLAN artifact,
// so it needs both agents, exactly like a Goal plan.
func (c *Coordinator) stepPlanApproval(ctx context.Context, client *transport.Client, message protocol.Message) {
	snapBefore := c.project.Snapshot()
	if *message.Ready {
		evidence, err := c.evidenceForReady(ctx, client.Identity(), snapBefore.Phase, snapBefore)
		if err != nil {
			_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
			return
		}
		snap, tr, err := c.project.SetReady(client.Identity(), true, message.Note, evidence)
		if err != nil {
			_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
			return
		}
		c.tracker.Touch(client.Identity())
		if tr.Advanced {
			c.recordEvent("phase_transition", map[string]any{"from": string(tr.Previous), "to": string(tr.Next)})
			c.emit(events.KindSystem, protocol.Duo, "", fmt.Sprintf("step queue approved → %s; step 1 of %d starts now", tr.Next, len(snap.Steps)))
		}
		_ = c.respond(ctx, client, message.RequestID, true,
			fmt.Sprintf("Queue approved (%d steps). Step %d: %s", len(snap.Steps), snap.Step, snap.Steps[snap.Step-1].Title),
			c.statusText(ctx))
		return
	}
	snap, _, err := c.project.SetReady(client.Identity(), false, message.Note, "")
	if err != nil {
		_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
		return
	}
	_ = c.respond(ctx, client, message.RequestID, true, "queue approval withdrawn.", c.statusText(ctx))
	_ = snap
}

// stepQueueSignOff is the Goal gate at the end: both agents sign the finished
// queue, and the last landing is what delivery completes.
func (c *Coordinator) stepQueueSignOff(ctx context.Context, client *transport.Client, message protocol.Message) {
	snapBefore := c.project.Snapshot()
	evidence := ""
	if *message.Ready {
		ev, err := c.evidenceForReady(ctx, client.Identity(), snapBefore.Phase, snapBefore)
		if err != nil {
			_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
			return
		}
		evidence = ev
	}
	snap, tr, err := c.project.SetReady(client.Identity(), *message.Ready, message.Note, evidence)
	if err != nil {
		_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
		return
	}
	c.tracker.Touch(client.Identity())
	if !tr.ReadyForDelivery {
		_ = c.respond(ctx, client, message.RequestID, true,
			fmt.Sprintf("Signed. The finished queue needs both agents' signatures (%d steps landed).", len(snap.Steps)),
			c.statusText(ctx))
		return
	}
	c.recordEvent("queue_signed_off", map[string]any{"steps": len(snap.Steps)})
	_ = c.respond(ctx, client, message.RequestID, true,
		fmt.Sprintf("Both agents signed the finished %d-step queue. Duo is completing the session.", len(snap.Steps)),
		c.statusText(ctx))
	c.deliverFinal(ctx, snap)
}

// handleStepVerification is the per-step independent check. It binds to the exact
// HEAD Austin submitted, so a commit after the request revokes the review.
func (c *Coordinator) handleStepVerification(ctx context.Context, client *transport.Client, message protocol.Message) {
	if client.Identity() != protocol.Tony {
		_ = c.respond(ctx, client, message.RequestID, false,
			"only Tony reports a STEP-mode step verdict", c.statusText(ctx))
		return
	}
	snap := c.project.Snapshot()
	if snap.Phase != project.PhaseReview {
		_ = c.respond(ctx, client, message.RequestID, false,
			"a step can only be verified during REVIEW", c.statusText(ctx))
		return
	}
	result, err := project.ParseVerificationResult(message.Verification)
	if err != nil {
		_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
		return
	}
	if snap.Step < 1 || snap.Step > len(snap.Steps) {
		_ = c.respond(ctx, client, message.RequestID, false, "the queue has no current step", c.statusText(ctx))
		return
	}
	target := strings.TrimSpace(snap.Steps[snap.Step-1].Head)

	// The verdict must describe the artifact that was submitted. If Austin committed
	// after the request, the review is stale: withdraw it so the new HEAD is
	// reviewed on a fresh request.
	current, err := recovery.FastVerificationTarget(ctx, c.workspace)
	if err != nil || current != target {
		c.project.SetReady(protocol.Austin, false, "the artifact changed after the step review was requested", "")
		c.recordEvent("step_review_revoked", map[string]any{"step": snap.Step, "requested": target, "current": current})
		reason := "Austin's HEAD changed after the step review was requested, so the review was withdrawn. Austin must submit the new HEAD."
		if err != nil {
			reason = err.Error() + "; the step review was withdrawn."
		}
		_ = c.respond(ctx, client, message.RequestID, false, reason, c.statusText(ctx))
		return
	}

	snap, tr, err := c.project.SetVerification(client.Identity(), result, message.Note, target)
	if err != nil {
		_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
		return
	}
	c.tracker.Touch(protocol.Tony)

	switch result {
	case project.VerificationPassed:
		if err := c.runTestGateIfSet(ctx); err != nil {
			c.recordEvent("test_gate_failed", map[string]any{"command": c.TestCommand(), "error": err.Error()})
			_ = c.respond(ctx, client, message.RequestID, false, err.Error(), c.statusText(ctx))
			return
		}
		c.recordEvent("verification", map[string]any{"result": "passed", "head": target, "step": snap.Step})
		c.recordEvent("phase_transition", map[string]any{"from": string(tr.Previous), "to": string(tr.Next)})
		c.emit(events.KindSystem, protocol.Duo, "", fmt.Sprintf("Tony verified step %d at %s → passed; landing it", snap.Step, shortSHA(target)))
		_ = c.respond(ctx, client, message.RequestID, true,
			fmt.Sprintf("Step %d verified. Duo is landing it on %s and starting the next step.", snap.Step, c.stepTargetBranch()),
			c.statusText(ctx))
		c.deliverFinal(ctx, snap)

	case project.VerificationIssueFound:
		c.recordEvent("verification", map[string]any{"result": "issue_found", "note": message.Note, "step": snap.Step})
		c.emit(events.KindVerdict, protocol.Tony, protocol.Austin, message.Note)
		_ = c.respond(ctx, client, message.RequestID, true,
			"Issue recorded. The step returned to EXECUTE; Austin fixes it and submits again.",
			c.statusText(ctx))
		c.steerToAustin(ctx, fmt.Sprintf(
			"[Duo step review] Tony found an issue with step %d (%s):\n\n%s\n\nFix it, commit, and submit the step again with duo_set_status ready=true.",
			snap.Step, shortSHA(target), strings.TrimSpace(message.Note)))
	}
}

// notifyStepReviewRequested tells Tony a step is ready for his verdict.
func (c *Coordinator) notifyStepReviewRequested(ctx context.Context, snap project.Snapshot) {
	step := snap.Steps[snap.Step-1]
	text := fmt.Sprintf(
		"[Duo step review]\nAustin submitted step %d of %d, %q, at HEAD %s.\n\nInspect that exact artifact yourself, then report a verdict with duo_set_verification: result=passed, or result=issue_found with a concrete note. STEP mode is single-writer: do not commit or edit the artifact; report the issue so Austin fixes it.",
		snap.Step, len(snap.Steps), step.Title, shortSHA(step.Head),
	)
	if c.language == "zh" {
		text = fmt.Sprintf(
			"[Duo 步骤审查]\nAustin 已提交第 %d/%d 步「%s」，HEAD 为 %s。\n\n请独立检查该精确产物，然后用 duo_set_verification 给出结论：result=passed，或 result=issue_found 并附上具体问题。STEP 模式只有 Austin 可以写入：不要提交或编辑产物；报告问题由 Austin 修复。",
			snap.Step, len(snap.Steps), step.Title, shortSHA(step.Head))
	}
	c.sendFastNotice(ctx, protocol.Tony, text)
}

// stepTargetBranch names the branch a landed step moves, for the response text.
func (c *Coordinator) stepTargetBranch() string {
	if branch := strings.TrimSpace(c.CurrentDelivery().TargetBranch); branch != "" {
		return branch
	}
	return "the base branch"
}

// handleRestartDuo records a request to continue this session on a new Duo
// binary. The agent asking is a child of the Duo being upgraded, so it cannot
// relaunch itself: Duo stops and prints the upgrade exit code, and a launcher
// outside Duo installs and resumes. Only STEP mode may ask, because only STEP has
// a queue to continue on the other side of the restart.
func (c *Coordinator) handleRestartDuo(ctx context.Context, client *transport.Client, message protocol.Message) {
	snap := c.project.Snapshot()
	if snap.EffectiveMode() != project.ModeStep {
		_ = c.respond(ctx, client, message.RequestID, false,
			"a self-upgrade restart is only available in STEP mode, which can resume the queue it was in the middle of",
			c.statusText(ctx))
		return
	}
	if snap.Phase != project.PhaseExecute {
		_ = c.respond(ctx, client, message.RequestID, false,
			fmt.Sprintf("Duo only restarts between steps, not during %s", snap.Phase), c.statusText(ctx))
		return
	}
	reason := strings.TrimSpace(message.Note)
	if reason == "" {
		reason = strings.TrimSpace(message.Text)
	}
	if reason == "" {
		reason = "requested a restart on a newly installed binary"
	}
	install := message.Text == "install" || message.Note == "install" || message.Install
	c.RequestRestart(reason, install)
	c.recordEvent("restart_requested", map[string]any{
		"agent": string(client.Identity()), "step": snap.Step, "reason": reason, "install": install,
	})
	c.emit(events.KindSystem, protocol.Duo, "", fmt.Sprintf("restart requested at step %d/%d: %s", snap.Step, len(snap.Steps), reason))
	_ = c.respond(ctx, client, message.RequestID, true,
		fmt.Sprintf("Restart recorded at step %d/%d. This Duo process is stopping; the launcher installs the new binary and resumes the session, and the queue continues at step %d.", snap.Step, len(snap.Steps), snap.Step),
		c.statusText(ctx))
}

// runTestGateIfSet runs the configured test command, and is a no-op when the
// operator set none. The error is the message to show the agent.
func (c *Coordinator) runTestGateIfSet(ctx context.Context) error {
	if c.TestCommand() == "" || c.workspace == nil {
		return nil
	}
	c.emit(events.KindSystem, protocol.Duo, "", fmt.Sprintf("running automated test gate: %s", c.TestCommand()))
	if err := c.runTestGate(ctx); err != nil {
		c.recordEvent("test_gate_failed", map[string]any{"command": c.TestCommand(), "error": err.Error()})
		c.emit(events.KindSystem, protocol.Duo, "", fmt.Sprintf("automated test gate failed: %v", err))
		return fmt.Errorf("automated test gate (%s) failed:\n%s\nFix the failure before requesting review.", c.TestCommand(), err.Error())
	}
	c.recordEvent("test_gate_passed", map[string]any{"command": c.TestCommand()})
	c.emit(events.KindSystem, protocol.Duo, "", fmt.Sprintf("automated test gate passed: %s", c.TestCommand()))
	return nil
}
