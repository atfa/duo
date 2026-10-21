package coordinator

import (
	"context"
	"testing"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

func TestThinkingObserverReceivesConfirmedLevel(t *testing.T) {
	coord := New(nil, project.NewStateFor(project.ModeFast), harness.NewTracker(), nil, events.NewBus())
	var gotAgent protocol.AgentID
	var gotLevel string
	coord.SetThinkingObserver(func(agent protocol.AgentID, level string) {
		gotAgent, gotLevel = agent, level
	})
	coord.handleThinkingState(protocol.Tony, protocol.Message{Thinking: "xhigh"})
	if gotAgent != protocol.Tony || gotLevel != "xhigh" {
		t.Fatalf("observer saw %s/%q, want Tony/xhigh", gotAgent, gotLevel)
	}
	if level := coord.Thinking(protocol.Tony); level != "xhigh" {
		t.Fatalf("coordinator level = %q, want xhigh", level)
	}
}

func TestCoordinatorSetModelAndCycleThinkingOffline(t *testing.T) {
	ctx := context.Background()
	bus := events.NewBus()
	state := project.NewStateFor(project.ModeFast)
	eventsCh, unsub := bus.Subscribe(20)
	defer unsub()

	coord := New(nil, state, harness.NewTracker(), nil, bus)

	// SetModel for offline agent (no TCP transport server)
	if err := coord.SetModel(ctx, protocol.Austin, "google", "gemini-3.8-flash-high"); err != nil {
		t.Fatalf("SetModel failed: %v", err)
	}

	select {
	case e := <-eventsCh:
		if e.Kind != events.KindModel || e.Agent != protocol.Austin || e.Provider != "google" || e.Model != "gemini-3.8-flash-high" {
			t.Fatalf("unexpected model event: %#v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for KindModel event")
	}

	// CycleThinking for offline agent (first cycle -> low)
	if err := coord.CycleThinking(ctx, protocol.Austin); err != nil {
		t.Fatalf("CycleThinking failed: %v", err)
	}

	select {
	case e := <-eventsCh:
		if e.Kind != events.KindThinking || e.Thinking != "low" {
			t.Fatalf("unexpected thinking event: %#v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for KindThinking event")
	}

	// Cycle again to medium
	if err := coord.CycleThinking(ctx, protocol.Austin); err != nil {
		t.Fatalf("second CycleThinking failed: %v", err)
	}

	select {
	case e := <-eventsCh:
		if e.Kind != events.KindThinking || e.Thinking != "medium" {
			t.Fatalf("unexpected second thinking event: %#v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for second KindThinking event")
	}
}
