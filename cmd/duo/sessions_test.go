package main

import (
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
)

func TestSessionStatusReportsAStepAwaitingDelivery(t *testing.T) {
	step := func(recordHead string) sessionstore.Summary {
		return sessionstore.Summary{
			SessionID: "s",
			Dir:       t.TempDir(),
			Snapshot: sessionstore.Snapshot{
				SessionID: "s",
				Mode:      project.ModeStep,
				Phase:     string(project.PhaseIntegrate),
				Step:      2,
				Steps: project.Steps{
					{Title: "one", Done: true, Head: "landed0", DeliveryHead: "landed0"},
					{Title: "two", Head: "head-two"},
				},
				Ready: map[protocol.AgentID]bool{protocol.Austin: true, protocol.Tony: true},
				Delivery: sessionstore.Delivery{
					Status: sessionstore.DeliveryApplied, FinalHead: recordHead, AppliedHead: recordHead,
				},
			},
		}
	}

	// The record is step 1's landing and step 2 is submitted: the session is
	// waiting to deliver, which is what `duo apply` is offered.
	if got := sessionStatus(step("landed0")); got != "pending" {
		t.Fatalf("status = %q, want pending for a step submitted after the last landing", got)
	}
	// The record is step 2's own landing: nothing is left to deliver.
	if got := sessionStatus(step("head-two")); got != "delivered" {
		t.Fatalf("status = %q, want delivered for the step whose landing is recorded", got)
	}

	// The other modes keep their meaning: a dual-signed INTEGRATE is pending, and
	// a DONE session with its artifact applied is delivered.
	goal := sessionstore.Summary{
		SessionID: "g",
		Dir:       t.TempDir(),
		Snapshot: sessionstore.Snapshot{
			SessionID: "g",
			Mode:      project.ModeGoal,
			Phase:     string(project.PhaseIntegrate),
			Ready:     map[protocol.AgentID]bool{protocol.Austin: true, protocol.Tony: true},
		},
	}
	if got := sessionStatus(goal); got != "pending" {
		t.Fatalf("status = %q, want pending for a dual-signed INTEGRATE", got)
	}

	done := goal.Snapshot
	done.Phase = "DONE"
	done.Delivery = sessionstore.Delivery{Status: sessionstore.DeliveryApplied, FinalHead: "h", AppliedHead: "h"}
	if got := sessionStatus(sessionstore.Summary{SessionID: "g", Dir: t.TempDir(), Snapshot: done}); got != "delivered" {
		t.Fatalf("status = %q, want delivered for a DONE session whose artifact landed", got)
	}
}
