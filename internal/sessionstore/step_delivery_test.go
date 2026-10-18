package sessionstore

import (
	"strings"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// stepDeliverySnapshot builds a STEP session whose first step landed at
// "landed0" and whose second step was submitted at "head-two", with the
// session-level delivery record left at the head it is given. That record is the
// latest landing, so it keeps describing step 1 after the queue advances.
func stepDeliverySnapshot(recordHead string) Snapshot {
	snap := testSnapshot("step-delivery")
	snap.Mode = project.ModeStep
	snap.Phase = string(project.PhaseIntegrate)
	snap.Step = 2
	snap.Steps = project.Steps{
		{Title: "one", Done: true, Head: "landed0", DeliveryHead: "landed0"},
		{Title: "two", Head: "head-two"},
	}
	snap.Delivery = Delivery{
		Status: DeliveryApplied, FinalHead: recordHead, AppliedHead: recordHead,
		FinalBranch: "duo/s/austin", TargetRepo: "/tmp/repo", TargetBranch: "main",
	}
	return snap
}

// TestStepDeliveryRecordIsScopedToTheCurrentStep is the scoping rule: an applied
// record means "nothing to deliver" only while it describes the step under the
// cursor.
func TestStepDeliveryRecordIsScopedToTheCurrentStep(t *testing.T) {
	// The record is step 1's landing and the cursor is on step 2.
	next := stepDeliverySnapshot("landed0")
	if next.StepDeliveryIsCurrent() {
		t.Fatal("the previous step's landing is not this step's landing")
	}
	if !next.StepAwaitsDelivery() {
		t.Fatal("a step submitted after the last landing awaits its own delivery")
	}

	// The record is this step's own landing: the crash window where the landing
	// reached the user's repository and the queue never advanced.
	current := stepDeliverySnapshot("head-two")
	if !current.StepDeliveryIsCurrent() {
		t.Fatal("the record describes the step under the cursor")
	}
	if current.StepAwaitsDelivery() {
		t.Fatal("a step whose landing is recorded is not awaiting delivery")
	}

	// The binding is on the artifact, not on how the SHA was written.
	cased := stepDeliverySnapshot(strings.ToUpper("head-two"))
	if !cased.StepDeliveryIsCurrent() {
		t.Fatal("a delivery head must bind the step head case-insensitively")
	}

	// A step that was submitted without a HEAD cannot be bound to a landing.
	unsubmitted := stepDeliverySnapshot("head-two")
	unsubmitted.Steps[1].Head = ""
	if unsubmitted.StepDeliveryIsCurrent() {
		t.Fatal("an unsubmitted step has no artifact for the record to describe")
	}
}

// TestNeedsDeliveryFindsAStepAfterAnAppliedRecord is the user-visible half: the
// stale record made the session look delivered, so `duo apply` and `duo sessions`
// could not act on a STEP session sitting in INTEGRATE with an open step.
func TestNeedsDeliveryFindsAStepAfterAnAppliedRecord(t *testing.T) {
	stale := stepDeliverySnapshot("landed0")
	if !stale.NeedsDelivery() {
		t.Fatal("a step submitted after the last landing must be deliverable")
	}

	landed := stepDeliverySnapshot("head-two")
	if landed.NeedsDelivery() {
		t.Fatal("the step whose landing is recorded must not be delivered twice")
	}

	// A finished queue has no step under the cursor: its record means the last
	// landing, and the session is completed by the sign-off, not re-delivered.
	finished := stepDeliverySnapshot("head-two")
	finished.Steps[1].Done = true
	finished.Steps[1].DeliveryHead = "head-two"
	finished.Delivery.FinalHead = "head-two"
	finished.Delivery.AppliedHead = "head-two"
	finished.Step = 3
	if finished.NeedsDelivery() {
		t.Fatal("a finished queue with its last step landed is delivered")
	}

	// The other modes have no steps, so an applied record keeps meaning "done".
	for _, mode := range []project.Mode{project.ModeGoal, project.ModeFast} {
		other := stepDeliverySnapshot("landed0")
		other.Mode = mode
		other.Steps = nil
		other.Step = 0
		if other.StepAwaitsDelivery() {
			t.Fatalf("%s has no steps, so nothing awaits a step delivery", mode)
		}
		if other.NeedsDelivery() {
			t.Fatalf("%s with an applied record must not need delivery", mode)
		}
	}
}

// TestStepUnderCursorCoversTheQueueEdges keeps the shared predicate honest about
// the two edges every consumer depends on: no cursor, and a finished queue.
func TestStepUnderCursorCoversTheQueueEdges(t *testing.T) {
	snap := stepDeliverySnapshot("landed0")
	if got := snap.StepUnderCursor(); got == nil || got.Title != "two" {
		t.Fatalf("cursor %d resolves to %+v, want step two", snap.Step, got)
	}
	snap.Step = 0
	if snap.StepUnderCursor() != nil {
		t.Fatal("a queue with no cursor has no step under it")
	}
	snap.Step = len(snap.Steps) + 1
	if snap.StepUnderCursor() != nil {
		t.Fatal("a finished queue has no step under the cursor")
	}
	if snap.StepAwaitsDelivery() {
		t.Fatal("a finished queue awaits no step delivery")
	}
}

// TestStepDeliveryIgnoresReadySignatures pins that the predicate is about the
// artifact, not the signatures: the final step's sign-off gate lives elsewhere.
func TestStepDeliveryIgnoresReadySignatures(t *testing.T) {
	snap := stepDeliverySnapshot("landed0")
	snap.Ready = map[protocol.AgentID]bool{protocol.Austin: true, protocol.Tony: true}
	if snap.StepDeliveryIsCurrent() {
		t.Fatal("signatures do not make an old landing describe the current step")
	}
}
