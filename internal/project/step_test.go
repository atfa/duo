package project

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/protocol"
)

func TestStepModeIsParsedAndNormalized(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"step", "step"}, {"STEP", "step"}, {" step ", "step"},
		{"fast", "fast"}, {"", "goal"}, {"goal", "goal"},
	} {
		got, err := ParseMode(tc.in)
		if tc.in == "" {
			if err != nil || string(got) != tc.want {
				t.Fatalf("ParseMode(\"\") = %q, %v; a legacy session must stay Goal", got, err)
			}
			continue
		}
		if err != nil || string(got) != tc.want {
			t.Fatalf("ParseMode(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	if _, err := ParseMode("steps"); err == nil {
		t.Fatal("an unknown mode must be an error, not a silent Step session")
	}
	if got := Mode("nonsense").Normalize(); got != ModeGoal {
		t.Fatalf("Normalize(nonsense) = %q, want goal", got)
	}
	if got := ModeStep.Normalize(); got != ModeStep {
		t.Fatalf("Normalize(step) = %q, want step", got)
	}
	if got := NewStateFor(ModeStep).Snapshot(); got.Mode != ModeStep || got.Phase != PhasePlan {
		t.Fatalf("a fresh Step session = mode %q phase %s, want step PLAN", got.Mode, got.Phase)
	}
	if got := ModeStep.Display(); got != "STEP" {
		t.Fatalf("Display() = %q, want STEP", got)
	}
	// STEP runs Goal's phases, so recovery's phase validation accepts them.
	for _, p := range []Phase{PhasePlan, PhaseExecute, PhaseReview, PhaseIntegrate, PhaseDone} {
		if !ModeStep.validPhase(p) {
			t.Fatalf("STEP must accept phase %s", p)
		}
	}
	if ModeStep.validPhase(PhaseVerify) {
		t.Fatal("STEP must not accept the Fast-only VERIFY phase")
	}
	// The loop-back is not part of the linear chain recovery reads for no-rewind:
	// Next follows Goal's chain, so INTEGRATE advances to DONE and never backwards.
	if next, ok := ModeStep.Next(PhaseIntegrate); !ok || next != PhaseDone {
		t.Fatalf("STEP INTEGRATE through Next = %q/%t; want DONE/true (the loop-back is not a chain step)", next, ok)
	}
}

func TestStepQueueParsesAtTheEdgeAndRenders(t *testing.T) {
	steps := ParseSteps("prose\n- [ ] first\n* [x] second\n  - [ ] third\n- [ ]\n")
	if len(steps) != 3 {
		t.Fatalf("parsed %d steps, want 3 (prose and empty items are not steps)", len(steps))
	}
	if !steps[1].Done || steps[0].Done {
		t.Fatalf("checkbox state lost: %+v", steps)
	}
	if got := FirstOpenStep(steps); got != 1 {
		t.Fatalf("FirstOpenStep = %d, want 1", got)
	}
	done := ParseSteps("- [x] a\n- [x] b")
	if got := FirstOpenStep(done); got != 3 {
		t.Fatalf("FirstOpenStep(finished) = %d, want len+1", got)
	}
	if QueueFinished(steps[:2]) || QueueFinished(steps) {
		t.Fatal("QueueFinished disagrees with the checkbox states")
	}

	full := Steps{{Title: "one", Done: true, DeliveryHead: "abcdef1234567890"}, {Title: "two", Head: "99887766554433", Verification: Verification{Status: VerificationPassed, Head: "99887766554433"}}}
	rendered := full.Render()
	if !strings.Contains(rendered, "landed abcdef1") || !strings.Contains(rendered, "submitted 998877") {
		t.Fatalf("Render must show where each step went, got:\n%s", rendered)
	}
}

// TestStepGateWalksTheQueue is the mode's contract: two gate calls per step, the
// queue advances by Duo, and only a finished queue can end the session.
func TestStepGateWalksTheQueue(t *testing.T) {
	st := NewStateFor(ModeStep)

	if _, _, err := st.SetReady(protocol.Austin, true, "ready", ""); err == nil {
		t.Fatal("PLAN must refuse approval before a queue exists")
	}
	if _, err := st.SetSteps(protocol.Austin, "- [ ] alpha\n- [ ] beta", false); err != nil {
		t.Fatalf("SetSteps: %v", err)
	}
	if _, err := st.SetPlan(protocol.Austin, "the plan"); err != nil {
		t.Fatalf("SetPlan: %v", err)
	}

	if _, tr, err := st.SetReady(protocol.Austin, true, "austin", ""); err != nil || tr.Advanced {
		t.Fatalf("PLAN after one signature: advanced=%t err=%v; one signature is not approval", tr.Advanced, err)
	}
	if _, tr, err := st.SetReady(protocol.Tony, true, "tony", ""); err != nil || !tr.Advanced || tr.Next != PhaseExecute {
		t.Fatalf("PLAN after both signatures: %q advanced=%t err=%v; want EXECUTE", tr.Next, tr.Advanced, err)
	}
	snap := st.Snapshot()
	if snap.Step != 1 || len(snap.Steps) != 2 {
		t.Fatalf("queue cursor = %d of %d, want 1 of 2", snap.Step, len(snap.Steps))
	}

	// EXECUTE: only Austin moves, and only with a committed HEAD.
	if _, _, err := st.SetReady(protocol.Tony, true, "tony", "head1"); err == nil {
		t.Fatal("Tony must not sign a step; his verb is the verdict")
	}
	if _, _, err := st.SetReady(protocol.Austin, true, "done", "  "); err == nil {
		t.Fatal("a step review request needs a committed HEAD")
	}
	if _, tr, err := st.SetReady(protocol.Austin, true, "done", "head1"); err != nil || tr.Next != PhaseReview {
		t.Fatalf("EXECUTE → %q err=%v; want REVIEW", tr.Next, err)
	}
	if got := st.Snapshot().Steps[0].Head; got != "head1" {
		t.Fatalf("the step must record the reviewed HEAD, got %q", got)
	}

	// REVIEW: only Tony's verdict moves, and it binds to that HEAD.
	if _, _, err := st.SetVerification(protocol.Austin, VerificationPassed, "", "head1"); err == nil {
		t.Fatal("only Tony reports the step verdict")
	}
	if _, _, err := st.SetVerification(protocol.Tony, VerificationPassed, "", "other-head"); err == nil {
		t.Fatal("a verdict bound to another HEAD must be refused")
	}
	if _, tr, err := st.SetVerification(protocol.Tony, VerificationIssueFound, "ship it later", "head1"); err != nil || tr.Next != PhaseExecute {
		t.Fatalf("issue_found → %q err=%v; want EXECUTE", tr.Next, err)
	}
	if _, tr, err := st.SetReady(protocol.Austin, true, "fixed", "head1"); err != nil || tr.Next != PhaseReview {
		t.Fatalf("re-submission → %q err=%v; want REVIEW", tr.Next, err)
	}
	if _, tr, err := st.SetVerification(protocol.Tony, VerificationPassed, "clean", "head1"); err != nil || tr.Next != PhaseIntegrate {
		t.Fatalf("passed → %q err=%v; want INTEGRATE", tr.Next, err)
	}

	// Landing advances the queue and returns to EXECUTE for the next step.
	if _, err := st.Complete(); err == nil {
		t.Fatal("the session cannot complete while a step is unlanded")
	}
	if _, tr, err := st.LandStep("landed1"); err != nil || tr.Next != PhaseExecute {
		t.Fatalf("landStep → %q err=%v; want EXECUTE", tr.Next, err)
	}
	snap = st.Snapshot()
	if snap.Step != 2 || !snap.Steps[0].Done || snap.Steps[0].DeliveryHead != "landed1" {
		t.Fatalf("after landing: cursor=%d step0=%+v", snap.Step, snap.Steps[0])
	}
	if snap.Steps[0].DeliveredAt == "" {
		t.Fatal("a landed step must record when it landed")
	}
	// Landing the same step twice cannot advance the queue twice.
	if _, _, err := st.LandStep("landed1"); err == nil {
		t.Fatal("landing a step that already landed must be refused")
	}

	// Second (last) step.
	if _, _, err := st.SetReady(protocol.Austin, true, "done", "head2"); err != nil {
		t.Fatalf("step 2 submit: %v", err)
	}
	if _, _, err := st.SetVerification(protocol.Tony, VerificationPassed, "clean", "head2"); err != nil {
		t.Fatalf("step 2 verdict: %v", err)
	}
	if _, tr, err := st.LandStep("landed2"); err != nil || tr.Advanced {
		t.Fatalf("the last step must stay in INTEGRATE for sign-off, got %q/%t err=%v", tr.Next, tr.Advanced, err)
	}
	if _, err := st.Complete(); err == nil {
		t.Fatal("a finished queue still needs both signatures to end the session")
	}
	if _, tr, err := st.SetReady(protocol.Austin, true, "queue done", "ev"); err != nil || tr.ReadyForDelivery {
		t.Fatalf("one signature must not trigger delivery (ready=%t err=%v)", tr.ReadyForDelivery, err)
	}
	if _, tr, err := st.SetReady(protocol.Tony, true, "queue done", "ev"); err != nil || !tr.ReadyForDelivery {
		t.Fatalf("both signatures must trigger the final delivery (ready=%t err=%v)", tr.ReadyForDelivery, err)
	}
	if snap, err := st.Complete(); err != nil || snap.Phase != PhaseDone {
		t.Fatalf("complete → %q err=%v; want DONE", snap.Phase, err)
	}
}

func TestStepQueueEditRules(t *testing.T) {
	st := NewStateFor(ModeStep)
	if _, err := st.SetPlan(protocol.Austin, "the plan"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSteps(protocol.Austin, "no checkboxes here", false); err == nil {
		t.Fatal("an empty queue must be refused: STEP with no steps has nothing to run")
	}
	if _, err := st.SetSteps(protocol.Austin, "- [ ] one\n- [ ] two", false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSteps(protocol.Tony, "- [ ] one\n- [ ] two", false); err != nil {
		t.Fatalf("either agent may author the queue in PLAN: %v", err)
	}
	if _, _, err := st.SetReady(protocol.Austin, true, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SetReady(protocol.Tony, true, "", ""); err != nil {
		t.Fatal(err)
	}
	// Outside PLAN the queue may only grow, so a landing can never be invalidated.
	if _, err := st.SetSteps(protocol.Austin, "- [ ] completely new", false); err == nil {
		t.Fatal("replacing the queue outside PLAN must be refused")
	}
	if _, err := st.SetSteps(protocol.Austin, "- [ ] three", true); err != nil {
		t.Fatalf("appending during EXECUTE must be allowed: %v", err)
	}
	if got := st.Snapshot().Steps; len(got) != 3 || got[2].Title != "three" {
		t.Fatalf("append did not extend the queue: %+v", got)
	}
}

// TestStepNudgeIsBounded is the token-cost acceptance test: a nudge repeats on a
// timer, so it carries the cursor and one line per remaining title, never the
// per-step evidence the full rendering carries.
func TestStepNudgeIsBounded(t *testing.T) {
	var steps Steps
	for i := 0; i < 10; i++ {
		steps = append(steps, Step{
			Title:        "step-title-" + string(rune('a'+i)),
			Head:         "1111111111111111",
			DeliveryHead: "2222222222222222",
			Verification: Verification{Status: VerificationPassed, Head: "1111111111111111", Note: strings.Repeat("long review note ", 20)},
		})
	}
	steps[0].Done = true

	nudge := steps.RenderCursor()
	if !strings.Contains(nudge, "step 2/10") {
		t.Fatalf("the nudge must carry the cursor, got:\n%s", nudge)
	}
	if strings.Contains(nudge, "long review note") {
		t.Fatal("the nudge must never carry step bodies")
	}
	if strings.Count(nudge, "landed") > 1 {
		t.Fatalf("the nudge must not repeat per-step landing evidence:\n%s", nudge)
	}
	full := steps.Render()
	if !strings.Contains(full, "landed 2222222") {
		t.Fatal("the full rendering must show where steps landed")
	}
	if len(nudge) >= len(full) {
		t.Fatalf("the nudge must be the cheap rendering: nudge=%d full=%d", len(nudge), len(full))
	}
}

func TestStepStateSurvivesRestore(t *testing.T) {
	st := NewStateFor(ModeStep)
	if _, err := st.SetPlan(protocol.Austin, "the plan"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSteps(protocol.Austin, "- [ ] one\n- [ ] two\n- [ ] three", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SetReady(protocol.Austin, true, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SetReady(protocol.Tony, true, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SetReady(protocol.Austin, true, "", "head1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SetVerification(protocol.Tony, VerificationPassed, "clean", "head1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.LandStep("landed1"); err != nil {
		t.Fatal(err)
	}

	restored := NewStateFor(ModeGoal)
	if err := restored.Restore(st.Snapshot()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got := restored.Snapshot()
	if got.Mode != ModeStep || got.Phase != PhaseExecute || got.Step != 2 || len(got.Steps) != 3 {
		t.Fatalf("restored mode=%q phase=%s cursor=%d steps=%d", got.Mode, got.Phase, got.Step, len(got.Steps))
	}
	if !got.Steps[0].Done || got.Steps[0].DeliveryHead != "landed1" {
		t.Fatalf("the landed step record did not survive restore: %+v", got.Steps[0])
	}
}

// TestRevokeStepSubmissionClearsTheStep is the rule delivery relies on when the
// reviewed HEAD is no longer the artifact under the cursor: the withdrawn step must
// carry neither a HEAD nor a verdict, or the next rendering claims something is
// pending when nothing is.
func TestRevokeStepSubmissionClearsTheStep(t *testing.T) {
	st := NewStateFor(ModeStep)
	if _, err := st.SetPlan(protocol.Austin, "plan"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSteps(protocol.Austin, "- [ ] one\n- [ ] two", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SetReady(protocol.Austin, true, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SetReady(protocol.Tony, true, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SetReady(protocol.Austin, true, "submitted", "reviews-me"); err != nil {
		t.Fatal(err)
	}
	if got := st.Snapshot().Steps[0].Head; got != "reviews-me" {
		t.Fatalf("submitted HEAD = %q", got)
	}

	got, tr, err := st.RevokeStepSubmission("the reviewed HEAD moved")
	if err != nil {
		t.Fatalf("RevokeStepSubmission: %v", err)
	}
	if !tr.Advanced || tr.Next != PhaseExecute {
		t.Fatalf("revocation into %s (advanced=%t), want EXECUTE", tr.Next, tr.Advanced)
	}
	step := got.Steps[0]
	if step.Head != "" || step.Verification.Status != "" {
		t.Fatalf("a withdrawn step must keep no submission or verdict: %+v", step)
	}
	if step.Done || got.Step != 1 {
		t.Fatalf("withdrawal must not advance or land the step: %+v", got.Steps)
	}
	if rendered := got.Steps.Render(); strings.Contains(rendered, "submitted") {
		t.Fatalf("the rendering must not show a withdrawn submission:\n%s", rendered)
	}

	// The final step is also revocable: it is signed and landed together, so a
	// withdrawal from INTEGRATE has to be able to take it back.
	if _, _, err := st.SetReady(protocol.Austin, true, "again", "reviews-me"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SetVerification(protocol.Tony, VerificationPassed, "ok", "reviews-me"); err != nil {
		t.Fatal(err)
	}
	if snap := st.Snapshot(); snap.Phase != PhaseIntegrate || snap.Step != 1 {
		t.Fatalf("precondition: phase=%s cursor=%d", snap.Phase, snap.Step)
	}
	if _, _, err := st.RevokeStepSubmission("the HEAD moved before landing"); err != nil {
		t.Fatalf("revocation from INTEGRATE: %v", err)
	}
	if got := st.Snapshot(); got.Phase != PhaseExecute || got.Steps[0].Head != "" {
		t.Fatalf("after revocation: phase=%s step=%+v", got.Phase, got.Steps[0])
	}
}

// TestFinalStepIsSignedAndLandedTogether pins the ordering the delivery gate
// depends on: the verdict leaves the final step in INTEGRATE while it is still
// unticked, so "the queue is finished" cannot be the test for accepting the
// INTEGRATE sign-off — the cursor has to be.
func TestFinalStepIsSignedAndLandedTogether(t *testing.T) {
	st := NewStateFor(ModeStep)
	if _, err := st.SetPlan(protocol.Austin, "plan"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSteps(protocol.Austin, "- [ ] only", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SetReady(protocol.Austin, true, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SetReady(protocol.Tony, true, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SetReady(protocol.Austin, true, "", "head"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SetVerification(protocol.Tony, VerificationPassed, "ok", "head"); err != nil {
		t.Fatal(err)
	}
	snap := st.Snapshot()
	if snap.Phase != PhaseIntegrate || QueueFinished(snap.Steps) {
		t.Fatalf("the final step must wait for sign-off while unticked: phase=%s steps=%+v", snap.Phase, snap.Steps)
	}
	if _, tr, err := st.SetReady(protocol.Austin, true, "signing", "head"); err != nil || tr.ReadyForDelivery {
		t.Fatalf("one signature must not trigger delivery (ready=%t err=%v)", tr.ReadyForDelivery, err)
	}
	if _, tr, err := st.SetReady(protocol.Tony, true, "signing", "head"); err != nil || !tr.ReadyForDelivery {
		t.Fatalf("both signatures must trigger delivery (ready=%t err=%v)", tr.ReadyForDelivery, err)
	}
	// The landing ticks the final step and leaves the session INTEGRATE for the
	// completion step that follows it.
	if _, _, err := st.LandStep("head"); err != nil {
		t.Fatalf("landStep on the final step: %v", err)
	}
	if got := st.Snapshot(); !QueueFinished(got.Steps) || got.Phase != PhaseIntegrate {
		t.Fatalf("after landing the final step: phase=%s steps=%+v", got.Phase, got.Steps)
	}
	if got, err := st.Complete(); err != nil || got.Phase != PhaseDone {
		t.Fatalf("complete = %s, %v; want DONE", got.Phase, err)
	}
}

// stepSessionWithLandedSteps walks a STEP session through `landed` steps —
// submit, verdict, land — and leaves it on the next step in INTEGRATE with that
// step's verdict passed: the shape a resume meets after a landing.
func stepSessionWithLandedSteps(t *testing.T, landed, total int) *State {
	t.Helper()
	st := NewStateFor(ModeStep)
	var queue strings.Builder
	for i := 1; i <= total; i++ {
		fmt.Fprintf(&queue, "- [ ] step %d\n", i)
	}
	if _, err := st.SetSteps(protocol.Austin, queue.String(), false); err != nil {
		t.Fatalf("SetSteps: %v", err)
	}
	if _, err := st.SetPlan(protocol.Austin, "the plan"); err != nil {
		t.Fatalf("SetPlan: %v", err)
	}
	if _, _, err := st.SetReady(protocol.Austin, true, "austin", ""); err != nil {
		t.Fatalf("austin sign: %v", err)
	}
	if _, _, err := st.SetReady(protocol.Tony, true, "tony", ""); err != nil {
		t.Fatalf("tony sign: %v", err)
	}
	for i := 1; i <= landed; i++ {
		head := fmt.Sprintf("head%d", i)
		if _, _, err := st.SetReady(protocol.Austin, true, "done", head); err != nil {
			t.Fatalf("step %d submit: %v", i, err)
		}
		if _, _, err := st.SetVerification(protocol.Tony, VerificationPassed, "clean", head); err != nil {
			t.Fatalf("step %d verdict: %v", i, err)
		}
		if _, _, err := st.LandStep(fmt.Sprintf("landed%d", i)); err != nil {
			t.Fatalf("step %d land: %v", i, err)
		}
	}
	head := fmt.Sprintf("head%d", landed+1)
	if _, _, err := st.SetReady(protocol.Austin, true, "done", head); err != nil {
		t.Fatalf("step %d submit: %v", landed+1, err)
	}
	if _, _, err := st.SetVerification(protocol.Tony, VerificationPassed, "clean", head); err != nil {
		t.Fatalf("step %d verdict: %v", landed+1, err)
	}
	return st
}

// TestLandStepRefusalNamesTheStepThatLandedTheHead is the message the user read:
// "6b65ff3cdcb5 already landed as step 4" named the queue cursor for a head that
// landed as step 3, so it pointed at the step being landed and hid the record that
// made the landing redundant.
func TestLandStepRefusalNamesTheStepThatLandedTheHead(t *testing.T) {
	st := stepSessionWithLandedSteps(t, 3, 6)
	snap := st.Snapshot()
	if snap.Step != 4 || snap.Phase != PhaseIntegrate {
		t.Fatalf("fixture: cursor=%d phase=%s, want 4 INTEGRATE", snap.Step, snap.Phase)
	}

	for i := 1; i <= 3; i++ {
		head := fmt.Sprintf("landed%d", i)
		_, _, err := st.LandStep(head)
		if err == nil {
			t.Fatalf("landing %s, which already landed, must be refused", head)
		}
		if !strings.Contains(err.Error(), fmt.Sprintf("%s already landed as step %d", head, i)) {
			t.Fatalf("refusal = %v; want it to name step %d, the step that landed the head", err, i)
		}
		if !errors.Is(err, ErrWrongPhase) {
			t.Fatalf("refusal = %v; want ErrWrongPhase", err)
		}
	}

	// The refusal is not a mutation: the queue and the cursor are untouched.
	if got := st.Snapshot(); got.Step != 4 || got.Steps[3].Done {
		t.Fatalf("a refused landing moved the queue: cursor=%d step4=%+v", got.Step, got.Steps[3])
	}
}
