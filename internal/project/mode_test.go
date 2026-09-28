package project

import (
	"strings"
	"testing"

	"github.com/atfa/duo/internal/protocol"
)

func TestParseModeDefaultsLegacySessionsToGoal(t *testing.T) {
	// ParseMode("") is used for persisted values: an absent mode must mean Goal,
	// never Fast, because every pre-mode session was a Goal session.
	for _, tc := range []struct {
		in   string
		want Mode
	}{
		{"", ModeGoal},
		{"goal", ModeGoal},
		{"GOAL", ModeGoal},
		{" fast ", ModeFast},
		{"FAST", ModeFast},
	} {
		got, err := ParseMode(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("ParseMode(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	if _, err := ParseMode("auto"); err == nil {
		t.Fatal("an unknown mode must be an error so a future mode cannot be silently downgraded")
	}
}

func TestNewSessionDefaultsAreExplicit(t *testing.T) {
	if DefaultMode != ModeFast {
		t.Fatalf("DefaultMode = %q, want fast", DefaultMode)
	}
	if got := NewStateFor(DefaultMode).Snapshot(); got.Mode != ModeFast || got.Phase != PhaseRunning {
		t.Fatalf("a fresh Fast session must start RUNNING, got mode=%q phase=%s", got.Mode, got.Phase)
	}
	// A zero mode behaves like a legacy session rather than silently becoming Fast.
	if got := NewStateFor("").Snapshot(); got.Mode != ModeGoal || got.Phase != PhasePlan {
		t.Fatalf("zero mode = mode %q phase %s, want goal PLAN", got.Mode, got.Phase)
	}
	// NewState stays Goal so every existing caller and test is unaffected.
	if got := NewState().Snapshot(); got.Mode != ModeGoal || got.Phase != PhasePlan {
		t.Fatalf("NewState = mode %q phase %s, want goal PLAN", got.Mode, got.Phase)
	}
}

func TestModeNextChainsAreSeparate(t *testing.T) {
	if next, ok := ModeFast.Next(PhaseRunning); !ok || next != PhaseVerify {
		t.Fatalf("Fast RUNNING → %q, %t; want VERIFY", next, ok)
	}
	if next, ok := ModeFast.Next(PhaseVerify); !ok || next != PhaseDone {
		t.Fatalf("Fast VERIFY → %q, %t; want DONE", next, ok)
	}
	if _, ok := ModeFast.Next(PhasePlan); ok {
		t.Fatal("Fast must never advance a Goal phase")
	}
	if next, ok := ModeGoal.Next(PhasePlan); !ok || next != PhaseExecute {
		t.Fatalf("Goal PLAN → %q, %t; want EXECUTE", next, ok)
	}
	if _, ok := ModeGoal.Next(PhaseVerify); ok {
		t.Fatal("Goal must never advance a Fast phase")
	}
}

func TestFastWorkflowLifecycle(t *testing.T) {
	state := NewStateFor(ModeFast)

	// Fast has no shared plan and no phase sign-off.
	if _, err := state.SetPlan(protocol.Austin, "plan"); err == nil {
		t.Fatal("Fast mode accepted a shared plan")
	}
	if _, _, err := state.SetReady(protocol.Tony, true, "", "head"); err == nil {
		t.Fatal("Fast mode accepted Tony's phase sign-off")
	}
	// A verdict cannot exist before it is requested.
	if _, _, err := state.SetVerification(protocol.Tony, VerificationPassed, "", "head"); err == nil {
		t.Fatal("Fast mode accepted a verdict in RUNNING")
	}
	// A completion request must name a committed HEAD.
	if _, _, err := state.SetReady(protocol.Austin, true, "", ""); err == nil {
		t.Fatal("Fast mode accepted a completion request without a HEAD")
	}

	snap, tr, err := state.SetReady(protocol.Austin, true, "done", "commit-1")
	if err != nil || snap.Phase != PhaseVerify || !tr.Advanced {
		t.Fatalf("RUNNING → VERIFY failed: phase=%s advanced=%t err=%v", snap.Phase, tr.Advanced, err)
	}
	if snap.Verification.Head != "commit-1" || snap.Verification.Status != VerificationNone {
		t.Fatalf("VERIFY must record the requested HEAD: %+v", snap.Verification)
	}

	// issue_found requires a concrete note and returns to RUNNING, keeping the
	// issue as an actionable result for Austin.
	if _, _, err := state.SetVerification(protocol.Tony, VerificationIssueFound, "   ", "commit-1"); err == nil {
		t.Fatal("issue_found without a note was accepted")
	}
	snap, _, err = state.SetVerification(protocol.Tony, VerificationIssueFound, "broken output", "commit-1")
	if err != nil || snap.Phase != PhaseRunning {
		t.Fatalf("issue_found must return to RUNNING: phase=%s err=%v", snap.Phase, err)
	}
	if snap.Verification.Status != VerificationIssueFound || snap.Verification.Note != "broken output" {
		t.Fatalf("issue_found must be recorded for Austin: %+v", snap.Verification)
	}

	// Austin fixes it, requests verification again, and only a passed verdict
	// opens delivery.
	if _, _, err := state.SetReady(protocol.Austin, true, "", "commit-2"); err != nil {
		t.Fatal(err)
	}
	snap, tr, err = state.SetVerification(protocol.Tony, VerificationPassed, "looks good", "commit-2")
	if err != nil || !tr.ReadyForDelivery {
		t.Fatalf("a passed verdict must open delivery: %+v err=%v", tr, err)
	}
	if snap.Verification.Status != VerificationPassed || snap.Verification.At == nil {
		t.Fatalf("a passed verdict must be recorded with a timestamp: %+v", snap.Verification)
	}
	if _, err := state.Complete(); err != nil {
		t.Fatalf("VERIFY → DONE failed: %v", err)
	}
	if got := state.Snapshot().Phase; got != PhaseDone {
		t.Fatalf("phase = %s, want DONE", got)
	}
	// A duplicate verdict after DONE is refused exactly like a duplicate signature.
	if _, _, err := state.SetVerification(protocol.Tony, VerificationPassed, "", "commit-2"); err != ErrProjectDone {
		t.Fatalf("verdict after DONE = %v, want ErrProjectDone", err)
	}
}

func TestFastVerificationMustBindToRequestedHead(t *testing.T) {
	state := NewStateFor(ModeFast)
	if _, _, err := state.SetReady(protocol.Austin, true, "", "commit-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.SetVerification(protocol.Tony, VerificationPassed, "", "commit-2"); err == nil {
		t.Fatal("a verdict about a different artifact was accepted")
	}
	if got := state.Snapshot().Phase; got != PhaseVerify {
		t.Fatalf("a rejected verdict must leave the request open, phase=%s", got)
	}
}

func TestFastAustinWithdrawsCompletionRequest(t *testing.T) {
	state := NewStateFor(ModeFast)
	if _, _, err := state.SetReady(protocol.Austin, true, "", "commit-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.SetReady(protocol.Austin, false, "more to do", ""); err != nil {
		t.Fatal(err)
	}
	snap := state.Snapshot()
	if snap.Phase != PhaseRunning {
		t.Fatalf("withdrawing must return VERIFY → RUNNING, got %s", snap.Phase)
	}
	if snap.Verification.Status != VerificationNone || snap.Verification.Head != "" {
		t.Fatalf("withdrawing must clear the pending request, got %+v", snap.Verification)
	}
	// Withdrawing while already RUNNING is a harmless no-op.
	if _, _, err := state.SetReady(protocol.Austin, false, "", ""); err != nil {
		t.Fatal(err)
	}
	if got := state.Snapshot().Phase; got != PhaseRunning {
		t.Fatalf("phase = %s, want RUNNING", got)
	}
}

func TestRestoreIsModeScopedAndLegacyIsGoal(t *testing.T) {
	// A legacy snapshot has no mode and must load as Goal.
	legacy := NewState()
	if err := legacy.Restore(Snapshot{Phase: PhasePlan, PlanVersion: 1, Plan: "old plan"}); err != nil {
		t.Fatalf("legacy Goal snapshot failed to restore: %v", err)
	}
	if got := legacy.Snapshot(); got.Mode != ModeGoal || got.Phase != PhasePlan {
		t.Fatalf("legacy restore = mode %q phase %s, want goal PLAN", got.Mode, got.Phase)
	}

	// A Goal phase can never be loaded into a Fast session.
	fast := NewStateFor(ModeFast)
	if err := fast.Restore(Snapshot{Mode: ModeFast, Phase: PhasePlan}); err == nil {
		t.Fatal("Restore accepted the Goal phase PLAN in Fast mode")
	}
	if err := fast.Restore(Snapshot{Mode: ModeFast, Phase: PhaseVerify, Verification: Verification{Status: VerificationPassed, Head: "h"}}); err != nil {
		t.Fatalf("Fast restore failed: %v", err)
	}
	if snap := fast.Snapshot(); snap.Mode != ModeFast || snap.Verification.Status != VerificationPassed || snap.Verification.Head != "h" {
		t.Fatalf("Fast restore lost mode/verification: %+v", snap)
	}

	// Fast phases are rejected in Goal mode, and Goal phases still restore.
	goal := NewState()
	if err := goal.Restore(Snapshot{Mode: ModeGoal, Phase: PhaseVerify}); err == nil {
		t.Fatal("Restore accepted the Fast phase VERIFY in Goal mode")
	}
	if err := goal.Restore(Snapshot{Mode: ModeGoal, Phase: PhaseReview}); err != nil {
		t.Fatalf("Goal restore failed: %v", err)
	}
}

func TestSnapshotStringIsModeAware(t *testing.T) {
	fast := NewStateFor(ModeFast)
	if _, _, err := fast.SetReady(protocol.Austin, true, "", "abcdef0123456789"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fast.SetVerification(protocol.Tony, VerificationPassed, "checked", "abcdef0123456789"); err != nil {
		t.Fatal(err)
	}
	text := fast.Snapshot().String()
	if !strings.Contains(text, "FAST") || !strings.Contains(text, "verification: passed") {
		t.Fatalf("fast status text = %q", text)
	}
	if strings.Contains(text, "Austin ready:") || strings.Contains(text, "plan version") {
		t.Fatalf("fast status must not print plan/sign-off fields: %q", text)
	}

	goal := NewState()
	if _, err := goal.SetPlan(protocol.Austin, "the plan"); err != nil {
		t.Fatal(err)
	}
	goalText := goal.Snapshot().String()
	if !strings.Contains(goalText, "Austin ready:") || !strings.Contains(goalText, "plan version: 1") {
		t.Fatalf("goal status text = %q", goalText)
	}
	if strings.Contains(goalText, "FAST") {
		t.Fatalf("goal status must not claim to be FAST: %q", goalText)
	}
}
