package session

import (
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/recovery"
	"github.com/atfa/duo/internal/sessionstore"
)

func stepSnapshot(phase project.Phase, landed int) sessionstore.Snapshot {
	steps := project.Steps{
		{Title: "one"},
		{Title: "two"},
	}
	for i := 0; i < landed; i++ {
		steps[i].Done = true
		steps[i].DeliveryHead = "landed" + string(rune('0'+i))
	}
	return sessionstore.Snapshot{
		SessionID: "s-1",
		Mode:      project.ModeStep,
		Phase:     string(phase),
		Plan:      "the plan",
		Step:      project.FirstOpenStep(steps),
		Steps:     steps,
		Ready:     map[protocol.AgentID]bool{protocol.Austin: true, protocol.Tony: true},
	}
}

// TestLandStepSnapshotAdvancesTheQueue proves the delivery transaction moves the
// queue through the domain rule, so the cursor, the step record and the phase can
// never disagree with each other.
func TestLandStepSnapshotAdvancesTheQueue(t *testing.T) {
	snap := stepSnapshot(project.PhaseIntegrate, 0)
	if err := LandStepSnapshot(&snap, "landed0"); err != nil {
		t.Fatalf("LandStepSnapshot: %v", err)
	}
	if snap.Phase != string(project.PhaseExecute) {
		t.Fatalf("after landing a step the session is %s, want EXECUTE for the next step", snap.Phase)
	}
	if snap.Step != 2 || !snap.Steps[0].Done || snap.Steps[0].DeliveryHead != "landed0" {
		t.Fatalf("queue after landing: cursor=%d steps=%+v", snap.Step, snap.Steps)
	}
	if snap.Steps[0].DeliveredAt == "" {
		t.Fatal("a landed step must record when it landed")
	}

	// The last step stays in INTEGRATE so the finished queue can be signed off.
	st := stepState(t, snap)
	if _, _, err := st.SetReady(protocol.Austin, true, "done", "head-two"); err != nil {
		t.Fatalf("submit step 2: %v", err)
	}
	if _, _, err := st.SetVerification(protocol.Tony, project.VerificationPassed, "clean", "head-two"); err != nil {
		t.Fatalf("verify step 2: %v", err)
	}
	snap = persistStep(t, snap, st)
	if err := LandStepSnapshot(&snap, "landed1"); err != nil {
		t.Fatalf("LandStepSnapshot last step: %v", err)
	}
	if snap.Phase != string(project.PhaseIntegrate) {
		t.Fatalf("the last step must stay in %s, got %s", project.PhaseIntegrate, snap.Phase)
	}
	if snap.Step != 3 {
		t.Fatalf("cursor after the last landing = %d, want 3 (queue finished)", snap.Step)
	}
	// A re-run of the same landing cannot advance the queue twice.
	if err := LandStepSnapshot(&snap, "landed1"); err == nil {
		t.Fatal("re-landing an already landed step must be refused")
	}
}

// stepState restores a snapshot into the domain state, which is how the delivery
// transaction reaches the domain rule.
func stepState(t *testing.T, snap sessionstore.Snapshot) *project.State {
	t.Helper()
	st := project.NewStateFor(project.ModeStep)
	if err := st.Restore(recovery.ProjectSnapshot(snap)); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	return st
}

// persistStep writes the domain state back into a persisted snapshot, the way the
// session composes a snapshot after every mutation.
func persistStep(t *testing.T, snap sessionstore.Snapshot, st *project.State) sessionstore.Snapshot {
	t.Helper()
	next := st.Snapshot()
	snap.Phase = string(next.Phase)
	snap.Step = next.Step
	snap.Steps = next.Steps
	return snap
}

// TestCurrentStepLandedScopesAppliedToTheStep is what makes delivery repeatable:
// the session-level "applied" record must stop meaning "the session is finished"
// and start meaning "this step landed".
func TestCurrentStepLandedScopesAppliedToTheStep(t *testing.T) {
	snap := stepSnapshot(project.PhaseIntegrate, 1)
	snap.Delivery = sessionstore.Delivery{Status: sessionstore.DeliveryApplied, FinalHead: "landed0", AppliedHead: "landed0"}
	// The cursor has already moved to step 2, so the applied record describes the
	// step that landed, not the one awaiting its landing.
	if currentStepLanded(snap) {
		t.Fatal("an applied record for the previous step must not count as this step's landing")
	}
	// Once the queue is finished the same record does mean "nothing left to deliver".
	finished := stepSnapshot(project.PhaseIntegrate, 2)
	finished.Delivery = sessionstore.Delivery{Status: sessionstore.DeliveryApplied, FinalHead: "landed1", AppliedHead: "landed1"}
	if !currentStepLanded(finished) {
		t.Fatal("a finished queue with its last step landed is landed")
	}

	// A new step has been submitted since: the same record must not block its
	// delivery, which is the whole point of scoping Applied to one step.
	snap.Steps[1].Head = "head-two"
	snap.Step = 2
	if currentStepLanded(snap) {
		t.Fatal("an applied record for the previous step must not count as this step's landing")
	}
}

// TestNeedsDeliveryStepCase proves a STEP session asks for delivery per step, and
// that a finished queue asks only when both agents signed it.
func TestNeedsDeliveryStepCase(t *testing.T) {
	open := stepSnapshot(project.PhaseIntegrate, 0)
	if !open.NeedsDelivery() {
		t.Fatal("a reviewed step must be delivered")
	}

	done := stepSnapshot(project.PhaseIntegrate, 2)
	done.Delivery = sessionstore.Delivery{Status: sessionstore.DeliveryApplied, FinalHead: "landed1", AppliedHead: "landed1"}
	if done.NeedsDelivery() {
		t.Fatal("a landed queue must not be delivered again")
	}

	done.Delivery = sessionstore.Delivery{}
	done.Ready[protocol.Tony] = false
	if done.NeedsDelivery() {
		t.Fatal("a finished queue without both signatures is not deliverable")
	}
	done.Ready[protocol.Tony] = true
	if !done.NeedsDelivery() {
		t.Fatal("a finished queue signed by both agents must be delivered")
	}

	// Fast and Goal keep their own predicates: STEP must not leak into them.
	fast := sessionstore.Snapshot{Mode: project.ModeFast, Phase: string(project.PhaseVerify)}
	if fast.NeedsDelivery() {
		t.Fatal("a Fast VERIFY without a passed verdict is not deliverable")
	}
}

// TestRestartRequestReachesTheView is the self-upgrade plumbing: the CLI reads
// the request off the view to write its handoff file and exit with the sentinel
// code, so a request that never reaches the view would stop nothing.
func TestRestartRequestReachesTheView(t *testing.T) {
	svc := &Service{}
	if svc.View().Restart.Reason != "" {
		t.Fatal("a fresh session must not ask to restart")
	}
	if err := svc.requestRestart(RestartDuo{Reason: " install and continue the queue ", Install: true}); err != nil {
		t.Fatalf("requestRestart: %v", err)
	}
	got := svc.View().Restart
	if got.Reason != "install and continue the queue" || !got.Install {
		t.Fatalf("restart request = %+v", got)
	}
}
