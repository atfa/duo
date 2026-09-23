package harness

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

type recordingSender struct {
	mu        sync.Mutex
	connected bool
	sent      []protocol.Message
}

func (r *recordingSender) Send(_ context.Context, _ protocol.AgentID, message protocol.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, message)
	return nil
}

func (r *recordingSender) IsConnected(protocol.AgentID) bool { return r.connected }

func (r *recordingSender) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sent)
}

func newTestMonitor(state *project.State, sender *recordingSender) *Monitor {
	return NewMonitor(Config{
		IdleThreshold:  time.Nanosecond,
		StallThreshold: time.Nanosecond,
		Cooldown:       0,
		TickInterval:   time.Millisecond,
	}, state, NewTracker(), sender, events.NewBus())
}

func advanceTo(t *testing.T, state *project.State, target project.Phase) {
	t.Helper()
	if _, err := state.SetPlan(protocol.Austin, "plan"); err != nil {
		t.Fatal(err)
	}
	for state.Snapshot().Phase != target {
		for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
			if _, _, err := state.SetReady(agent, true, "", "evidence-"+string(agent)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestMonitorNudgesWhenWorkIsUnfinished is the positive control: the monitor
// really does nudge, so the INTEGRATE test below cannot pass vacuously.
func TestMonitorNudgesWhenWorkIsUnfinished(t *testing.T) {
	state := project.NewState()
	if _, err := state.SetPlan(protocol.Austin, "plan"); err != nil {
		t.Fatal(err)
	}

	sender := &recordingSender{connected: true}
	m := newTestMonitor(state, sender)
	m.maybeWake(context.Background())

	if sender.count() == 0 {
		t.Fatal("expected the harness to nudge Austin while the project is unfinished")
	}
}

// TestMonitorDoesNotNudgeAfterFinalApproval is the v0.4.1 harness rule: once
// both agents have signed INTEGRATE, agent work is over. Delivery is a Duo Core
// and human concern, so Austin must not be told to continue working.
func TestMonitorDoesNotNudgeAfterFinalApproval(t *testing.T) {
	state := project.NewState()
	advanceTo(t, state, project.PhaseIntegrate)

	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		if _, _, err := state.SetReady(agent, true, "final", "final-head"); err != nil {
			t.Fatal(err)
		}
	}
	snap := state.Snapshot()
	if snap.Phase != project.PhaseIntegrate || !snap.Ready[protocol.Austin] || !snap.Ready[protocol.Tony] {
		t.Fatalf("setup did not reach a dual-signed INTEGRATE: %+v", snap)
	}

	sender := &recordingSender{connected: true}
	m := newTestMonitor(state, sender)
	m.maybeWake(context.Background())

	if sender.count() != 0 {
		t.Fatalf("harness nudged Austin after final approval: %+v", sender.sent)
	}
}
