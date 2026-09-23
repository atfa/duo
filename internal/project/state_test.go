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
		{PhaseDone},
	} {
		if _, _, err := s.SetReady(protocol.Austin, true, "done", "a-evidence"); err != nil {
			t.Fatal(err)
		}
		snap, tr, err = s.SetReady(protocol.Tony, true, "done", "t-evidence")
		if err != nil || !tr.Advanced || snap.Phase != tc.want {
			t.Fatalf("transition to %s failed: snap=%+v tr=%+v err=%v", tc.want, snap, tr, err)
		}
	}
}

func TestCannotApproveEmptyPlan(t *testing.T) {
	s := NewState()
	if _, _, err := s.SetReady(protocol.Austin, true, "", ""); err != ErrMissingPlan {
		t.Fatalf("err=%v, want %v", err, ErrMissingPlan)
	}
}
