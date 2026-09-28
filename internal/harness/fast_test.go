package harness

import (
	"context"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

func sentTo(sender *recordingSender, agent protocol.AgentID) []protocol.Message {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	var out []protocol.Message
	for _, message := range sender.sent {
		if message.To == agent {
			out = append(out, message)
		}
	}
	return out
}

func fastMonitorState(t *testing.T) *project.State {
	t.Helper()
	state := project.NewStateFor(project.ModeFast)
	state.MarkStarted()
	return state
}

// TestFastMonitorNudgesAustinWhileTonyIsBusy pins the anti-double-wait rule: in
// RUNNING the monitor cares only about Austin, so a busy or idle Tony can never
// leave Austin stalled.
func TestFastMonitorNudgesAustinWhileTonyIsBusy(t *testing.T) {
	state := fastMonitorState(t)
	sender := &recordingSender{connected: true}
	m := newTestMonitor(state, sender)

	m.tracker.Handle(protocol.Tony, protocol.ActivityAgentStart)
	m.tracker.Handle(protocol.Tony, protocol.ActivityProviderStart)

	m.maybeWake(context.Background())
	if len(sentTo(sender, protocol.Austin)) == 0 {
		t.Fatal("Fast RUNNING must nudge Austin even while Tony is busy")
	}
}

// TestFastMonitorConnectionGateIsTargetSpecific is the regression guard for the
// old "both agents connected" gate: a disconnected Tony must not suppress
// Austin's RUNNING nudge, and a disconnected Austin must not suppress Tony's
// VERIFY nudge.
func TestFastMonitorConnectionGateIsTargetSpecific(t *testing.T) {
	ctx := context.Background()

	running := fastMonitorState(t)
	offlineTony := &recordingSender{connected: true, offline: map[protocol.AgentID]bool{protocol.Tony: true}}
	newTestMonitor(running, offlineTony).maybeWake(ctx)
	if len(sentTo(offlineTony, protocol.Austin)) == 0 {
		t.Fatal("a disconnected Tony must not suppress Austin's RUNNING nudge")
	}

	verifying := fastMonitorState(t)
	if _, _, err := verifying.SetReady(protocol.Austin, true, "", "head-1"); err != nil {
		t.Fatal(err)
	}
	offlineAustin := &recordingSender{connected: true, offline: map[protocol.AgentID]bool{protocol.Austin: true}}
	newTestMonitor(verifying, offlineAustin).maybeWake(ctx)
	if len(sentTo(offlineAustin, protocol.Tony)) == 0 {
		t.Fatal("VERIFY must nudge Tony even when Austin is disconnected")
	}
	if len(sentTo(offlineAustin, protocol.Austin)) != 0 {
		t.Fatal("VERIFY must not nudge Austin")
	}
}

// TestFastMonitorNudgesTonyInVerify makes the verify nudge target-specific.
func TestFastMonitorNudgesTonyInVerify(t *testing.T) {
	state := fastMonitorState(t)
	if _, _, err := state.SetReady(protocol.Austin, true, "", "head-1"); err != nil {
		t.Fatal(err)
	}
	sender := &recordingSender{connected: true}
	newTestMonitor(state, sender).maybeWake(context.Background())

	if len(sentTo(sender, protocol.Tony)) == 0 {
		t.Fatal("Fast VERIFY must nudge Tony to verify")
	}
	if len(sentTo(sender, protocol.Austin)) != 0 {
		t.Fatal("Fast VERIFY must not nudge Austin")
	}
}

// TestFastMonitorStopsAfterVerificationPassed: once a pass exists, agent work is
// over and Duo Core delivers. Nudging either agent would ask it to redo work.
func TestFastMonitorStopsAfterVerificationPassed(t *testing.T) {
	state := fastMonitorState(t)
	if _, _, err := state.SetReady(protocol.Austin, true, "", "head-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.SetVerification(protocol.Tony, project.VerificationPassed, "", "head-1"); err != nil {
		t.Fatal(err)
	}
	sender := &recordingSender{connected: true}
	newTestMonitor(state, sender).maybeWake(context.Background())
	if sender.count() != 0 {
		t.Fatalf("no agent work is expected after a passed verification: %+v", sender.sent)
	}
}

// TestFastMonitorAsksForOneDiagnosisPerEpisode pins the escalation contract: two
// consecutive idle nudges earn exactly one copilot diagnosis, and real Austin
// activity starts a fresh episode that can earn one more.
func TestFastMonitorAsksForOneDiagnosisPerEpisode(t *testing.T) {
	state := fastMonitorState(t)
	sender := &recordingSender{connected: true}
	m := newTestMonitor(state, sender)
	ctx := context.Background()

	m.maybeWake(ctx) // nudge 1
	m.maybeWake(ctx) // nudge 2 → diagnosis
	m.maybeWake(ctx) // nudge 3 → already diagnosed in this episode
	if got := len(sentTo(sender, protocol.Tony)); got != 1 {
		t.Fatalf("diagnoses in one episode = %d, want 1", got)
	}

	// Austin works, then stalls again.
	m.tracker.Handle(protocol.Austin, protocol.ActivityAgentStart)
	m.tracker.Handle(protocol.Austin, protocol.ActivityToolStart)
	m.tracker.Handle(protocol.Austin, protocol.ActivityToolEnd)
	m.tracker.Handle(protocol.Austin, protocol.ActivityAgentSettled)

	m.maybeWake(ctx) // observes real activity → reset, no nudge
	m.maybeWake(ctx) // nudge 1
	m.maybeWake(ctx) // nudge 2 → a new diagnosis for the new episode
	if got := len(sentTo(sender, protocol.Tony)); got != 2 {
		t.Fatalf("diagnoses after a fresh episode = %d, want 2", got)
	}
}

// TestFastMonitorDiagnosesReportedFailure: a concrete agent error is a
// deterministic reason to involve the copilot, without waiting for two idle
// nudges.
func TestFastMonitorDiagnosesReportedFailure(t *testing.T) {
	state := fastMonitorState(t)
	sender := &recordingSender{connected: true}
	m := newTestMonitor(state, sender)
	ctx := context.Background()

	m.tracker.RecordFailure(protocol.Austin)
	m.maybeWake(ctx)
	m.maybeWake(ctx)
	m.maybeWake(ctx)

	if got := len(sentTo(sender, protocol.Tony)); got != 1 {
		t.Fatalf("a reported failure must earn exactly one diagnosis, got %d", got)
	}
}
