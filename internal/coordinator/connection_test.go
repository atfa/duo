package coordinator

import (
	"context"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// connectionEvents drains the bus and returns the connection-marked events, which
// is the part of the stream a frontend uses to tell a connection line apart from
// every other system line.
func connectionEvents(ch <-chan events.Event) []events.Event {
	var out []events.Event
	for {
		select {
		case e := <-ch:
			if e.Connection != events.ConnectionNone {
				out = append(out, e)
			}
			continue
		default:
		}
		return out
	}
}

// A bridge attach and detach must be marked as connection events: the text is the
// same line the timeline has always shown, and the mark is what lets an interface
// that attached after the event recognise it as the connection it already
// announced from the session view.
func TestBridgeAttachAndDetachAreMarkedAsConnectionEvents(t *testing.T) {
	bus := events.NewBus()
	ch, unsub := bus.Subscribe(64)
	defer unsub()

	coord := New(nil, project.NewStateFor(project.ModeFast), harness.NewTracker(), nil, bus)
	mgr := agent.NewManager()
	mgr.Add(newMockDriver(protocol.Austin, "eager", steeringOnly()))
	coord.SetAgents(mgr)

	coord.onConnect(context.Background(), protocol.Austin)
	up := connectionEvents(ch)
	if len(up) != 1 {
		t.Fatalf("connection events after a bridge attach = %d, want 1: %+v", len(up), up)
	}
	if up[0].Connection != events.ConnectionUp || up[0].Agent != protocol.Austin {
		t.Fatalf("attach event = %+v, want an Austin connection-up event", up[0])
	}
	if !strings.Contains(up[0].Text, "Austin connected") {
		t.Fatalf("attach text = %q, want the line the timeline shows", up[0].Text)
	}

	coord.onDisconnect(protocol.Austin)
	down := connectionEvents(ch)
	if len(down) != 1 {
		t.Fatalf("connection events after a bridge detach = %d, want 1: %+v", len(down), down)
	}
	if down[0].Connection != events.ConnectionDown || down[0].Agent != protocol.Austin {
		t.Fatalf("detach event = %+v, want an Austin connection-down event", down[0])
	}
	if !strings.Contains(down[0].Text, "Austin disconnected") {
		t.Fatalf("detach text = %q, want the line the timeline shows", down[0].Text)
	}
}

// Every other system line stays unmarked: only a bridge attach or detach may be
// deduplicated against the view, and a marker on anything else would let a
// frontend swallow a line it has not shown.
func TestOtherSystemLinesAreNotConnectionEvents(t *testing.T) {
	bus := events.NewBus()
	ch, unsub := bus.Subscribe(16)
	defer unsub()

	coord := New(nil, project.NewStateFor(project.ModeFast), harness.NewTracker(), nil, bus)
	coord.emit(events.KindSystem, protocol.Duo, "", "pause: paused")

	if got := connectionEvents(ch); len(got) != 0 {
		t.Fatalf("an ordinary system line was marked as a connection event: %+v", got)
	}
}
