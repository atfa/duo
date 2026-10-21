package harness

import (
	"context"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

func stepMonitorState(t *testing.T, steps string) *project.State {
	t.Helper()
	state := project.NewStateFor(project.ModeStep)
	if _, err := state.SetPlan(protocol.Austin, "plan"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.SetSteps(protocol.Austin, steps, false); err != nil {
		t.Fatal(err)
	}
	return state
}

func approveStepPlan(t *testing.T, state *project.State) {
	t.Helper()
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		if _, _, err := state.SetReady(agent, true, "", ""); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStepMonitorNudgesAustinDuringFastWork(t *testing.T) {
	state := stepMonitorState(t, "- [ ] [fast] first\n- [ ] [fast] second")
	approveStepPlan(t, state)
	sender := &recordingSender{connected: true}
	newTestMonitor(state, sender).maybeWake(context.Background())

	if len(sentTo(sender, protocol.Austin)) == 0 {
		t.Fatal("FAST EXECUTE must nudge Austin")
	}
	if len(sentTo(sender, protocol.Tony)) != 0 {
		t.Fatal("FAST EXECUTE must not nudge Tony")
	}
}

func TestStepMonitorNudgesTonyDuringFastReview(t *testing.T) {
	state := stepMonitorState(t, "- [ ] [fast] first\n- [ ] [fast] second")
	approveStepPlan(t, state)
	if _, _, err := state.SetReady(protocol.Austin, true, "submitted", "head-1"); err != nil {
		t.Fatal(err)
	}
	sender := &recordingSender{connected: true}
	newTestMonitor(state, sender).maybeWake(context.Background())

	if len(sentTo(sender, protocol.Tony)) == 0 {
		t.Fatal("FAST REVIEW must nudge Tony to verify")
	}
	if len(sentTo(sender, protocol.Austin)) != 0 {
		t.Fatal("FAST REVIEW must not nudge Austin")
	}
}

func TestStepMonitorNudgesMissingPlanApprover(t *testing.T) {
	state := stepMonitorState(t, "- [ ] [fast] first\n- [ ] [fast] second")
	if _, _, err := state.SetReady(protocol.Austin, true, "", ""); err != nil {
		t.Fatal(err)
	}
	sender := &recordingSender{connected: true}
	newTestMonitor(state, sender).maybeWake(context.Background())

	if len(sentTo(sender, protocol.Tony)) == 0 {
		t.Fatal("STEP PLAN must nudge the agent that has not approved")
	}
	if len(sentTo(sender, protocol.Austin)) != 0 {
		t.Fatal("STEP PLAN must not nudge an agent that already approved")
	}
}

func TestStepMonitorNudgesBothForGoalReview(t *testing.T) {
	state := stepMonitorState(t, "- [ ] [goal] first\n- [ ] [goal] second")
	approveStepPlan(t, state)
	if _, _, err := state.SetReady(protocol.Austin, true, "", "head-a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.SetReady(protocol.Tony, true, "", "head-t"); err != nil {
		t.Fatal(err)
	}
	sender := &recordingSender{connected: true}
	newTestMonitor(state, sender).maybeWake(context.Background())

	if len(sentTo(sender, protocol.Austin)) == 0 || len(sentTo(sender, protocol.Tony)) == 0 {
		t.Fatalf("GOAL REVIEW must nudge both missing reviewers: %+v", sender.sent)
	}
}

func TestStepMonitorNudgesBothForFinalSignOff(t *testing.T) {
	state := stepMonitorState(t, "- [ ] [fast] only")
	approveStepPlan(t, state)
	if _, _, err := state.SetReady(protocol.Austin, true, "submitted", "head-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.SetVerification(protocol.Tony, project.VerificationPassed, "ok", "head-1"); err != nil {
		t.Fatal(err)
	}
	if state.Snapshot().Phase != project.PhaseIntegrate {
		t.Fatalf("setup phase = %s, want INTEGRATE", state.Snapshot().Phase)
	}
	sender := &recordingSender{connected: true}
	newTestMonitor(state, sender).maybeWake(context.Background())

	if len(sentTo(sender, protocol.Austin)) == 0 || len(sentTo(sender, protocol.Tony)) == 0 {
		t.Fatalf("final sign-off must nudge both agents: %+v", sender.sent)
	}
}

func TestStepMonitorDoesNotNudgeFastStepDuringDelivery(t *testing.T) {
	state := stepMonitorState(t, "- [ ] [fast] first\n- [ ] [fast] second")
	approveStepPlan(t, state)
	if _, _, err := state.SetReady(protocol.Austin, true, "submitted", "head-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.SetVerification(protocol.Tony, project.VerificationPassed, "ok", "head-1"); err != nil {
		t.Fatal(err)
	}
	if state.Snapshot().Phase != project.PhaseIntegrate {
		t.Fatalf("setup phase = %s, want INTEGRATE", state.Snapshot().Phase)
	}
	sender := &recordingSender{connected: true}
	newTestMonitor(state, sender).maybeWake(context.Background())

	if sender.count() != 0 {
		t.Fatalf("a non-final FAST step is being delivered; agents must not be nudged: %+v", sender.sent)
	}
}
