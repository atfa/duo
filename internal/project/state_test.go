package project

import (
	"testing"

	"github.com/atfa/duo/internal/protocol"
)

func TestPlanVersionInvalidatesSignaturesAndLifecycle(t *testing.T) {
	s := NewState()

	snap, err := s.SetPlan(protocol.Austin, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.PlanVersion != 1 {
		t.Fatalf("version=%d, want 1", snap.PlanVersion)
	}

	if _, tr, err := s.SetReady(protocol.Austin, true, "approve v1", "plan-v1"); err != nil || tr.Advanced {
		t.Fatalf("Austin approval: tr=%+v err=%v", tr, err)
	}

	snap, err = s.SetPlan(protocol.Tony, "v2")
	if err != nil {
		t.Fatal(err)
	}
	if snap.PlanVersion != 2 || snap.Ready[protocol.Austin] || snap.Ready[protocol.Tony] {
		t.Fatalf("plan update did not reset signatures: %+v", snap)
	}

	if _, _, err := s.SetReady(protocol.Austin, true, "approve", "plan-v2"); err != nil {
		t.Fatal(err)
	}
	snap, tr, err := s.SetReady(protocol.Tony, true, "approve", "plan-v2")
	if err != nil || !tr.Advanced || tr.Next != PhaseExecute || snap.Phase != PhaseExecute {
		t.Fatalf("PLAN transition: snap=%+v tr=%+v err=%v", snap, tr, err)
	}

	for _, tc := range []struct {
		want Phase
	}{
		{PhaseReview},
		{PhaseIntegrate},
	} {
		if _, _, err := s.SetReady(protocol.Austin, true, "done", "a-evidence"); err != nil {
			t.Fatal(err)
		}
		snap, tr, err = s.SetReady(protocol.Tony, true, "done", "t-evidence")
		if err != nil || !tr.Advanced || snap.Phase != tc.want {
			t.Fatalf("transition to %s failed: snap=%+v tr=%+v err=%v", tc.want, snap, tr, err)
		}
	}

	// INTEGRATE no longer advances on dual sign-off: it records final approval
	// and waits for Duo Core to deliver the artifact before DONE.
	snap, tr, err = s.SetReady(protocol.Austin, true, "done", "a-evidence")
	if err != nil {
		t.Fatal(err)
	}
	snap, tr, err = s.SetReady(protocol.Tony, true, "done", "t-evidence")
	if err != nil || tr.Advanced || !tr.ReadyForDelivery || snap.Phase != PhaseIntegrate {
		t.Fatalf("INTEGRATE dual sign-off: snap=%+v tr=%+v err=%v", snap, tr, err)
	}
	if snap, err = s.Complete(); err != nil || snap.Phase != PhaseDone {
		t.Fatalf("Complete: snap=%+v err=%v", snap, err)
	}
}

func TestCannotApproveEmptyPlan(t *testing.T) {
	s := NewState()
	if _, _, err := s.SetReady(protocol.Austin, true, "", ""); err != ErrMissingPlan {
		t.Fatalf("err=%v, want %v", err, ErrMissingPlan)
	}
}

// TestIntegrateDualSignRequiresDeliveryBeforeDone is the v0.4.1 core rule:
// signing INTEGRATE no longer advances straight to DONE. The phase stays
// INTEGRATE with both signatures intact until an explicit Complete().
func TestIntegrateDualSignRequiresDeliveryBeforeDone(t *testing.T) {
	s := NewState()
	advanceTo(t, s, PhaseIntegrate)

	if _, _, err := s.SetReady(protocol.Austin, true, "final ok", "final-head"); err != nil {
		t.Fatal(err)
	}
	snap, tr, err := s.SetReady(protocol.Tony, true, "final ok", "final-head")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Advanced {
		t.Fatalf("INTEGRATE dual sign must not advance to DONE directly: %+v", tr)
	}
	if !tr.ReadyForDelivery {
		t.Fatalf("INTEGRATE dual sign must request delivery: %+v", tr)
	}
	if snap.Phase != PhaseIntegrate {
		t.Fatalf("phase = %s, want INTEGRATE", snap.Phase)
	}
	if !snap.Ready[protocol.Austin] || !snap.Ready[protocol.Tony] {
		t.Fatalf("final signatures must be kept for delivery: %+v", snap.Ready)
	}
}

func TestIntegrateFinalApprovalIsEdgeTriggered(t *testing.T) {
	s := NewState()
	advanceTo(t, s, PhaseIntegrate)

	if _, tr, err := s.SetReady(protocol.Austin, true, "ok", "head"); err != nil || tr.ReadyForDelivery {
		t.Fatalf("Austin sign = %+v, %v", tr, err)
	}
	if _, tr, err := s.SetReady(protocol.Tony, true, "ok", "head"); err != nil || !tr.ReadyForDelivery {
		t.Fatalf("Tony sign = %+v, %v", tr, err)
	}
	for _, agent := range []protocol.AgentID{protocol.Tony, protocol.Austin} {
		if _, tr, err := s.SetReady(agent, true, "retry", "head"); err != nil || tr.ReadyForDelivery {
			t.Fatalf("duplicate %s sign = %+v, %v", agent, tr, err)
		}
	}
	if _, _, err := s.SetReady(protocol.Tony, false, "revoke", ""); err != nil {
		t.Fatal(err)
	}
	if _, tr, err := s.SetReady(protocol.Tony, true, "again", "head"); err != nil || !tr.ReadyForDelivery {
		t.Fatalf("renewed Tony sign = %+v, %v", tr, err)
	}
}

// TestCompleteRequiresSignedIntegrateAndKeepsSignatures proves DONE is only
// reachable from a signed INTEGRATE and that the final signatures remain
// visible in DONE instead of being reset to empty circles.
func TestCompleteRequiresSignedIntegrateAndKeepsSignatures(t *testing.T) {
	s := NewState()
	if _, err := s.Complete(); err == nil {
		t.Fatal("Complete from PLAN must be refused")
	}

	advanceTo(t, s, PhaseIntegrate)
	if _, _, err := s.SetReady(protocol.Austin, true, "final ok", "final-head"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete(); err == nil {
		t.Fatal("Complete with a single signature must be refused")
	}

	if _, _, err := s.SetReady(protocol.Tony, true, "final ok", "final-head"); err != nil {
		t.Fatal(err)
	}
	done, err := s.Complete()
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if done.Phase != PhaseDone {
		t.Fatalf("phase = %s, want DONE", done.Phase)
	}
	if !done.Ready[protocol.Austin] || !done.Ready[protocol.Tony] {
		t.Fatalf("DONE must keep the final signatures: %+v", done.Ready)
	}
	if done.Evidence[protocol.Austin] != "final-head" || done.Evidence[protocol.Tony] != "final-head" {
		t.Fatalf("DONE must keep the final evidence: %+v", done.Evidence)
	}
}

// advanceTo drives the state machine to the target phase with placeholder
// evidence. Tests that care about real evidence set it themselves.
func advanceTo(t *testing.T, s *State, target Phase) {
	t.Helper()
	if _, err := s.SetPlan(protocol.Austin, "plan"); err != nil {
		t.Fatal(err)
	}
	for s.Snapshot().Phase != target {
		phase := s.Snapshot().Phase
		if phase == PhaseIntegrate {
			// INTEGRATE is the last agent phase; stop here rather than Complete.
			if target == PhaseDone {
				t.Fatalf("advanceTo(DONE) must sign INTEGRATE explicitly")
			}
			return
		}
		for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
			if _, _, err := s.SetReady(agent, true, "ok", "evidence-"+string(agent)); err != nil {
				t.Fatalf("signing %s in %s: %v", agent, phase, err)
			}
		}
	}
}
