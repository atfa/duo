package gotui

import (
	"testing"
	"time"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/protocol"
)

// countLines reports how many conversation entries carry exactly this text.
func countLines(app *App, text string) int {
	n := 0
	for _, e := range app.entries {
		if e.text == text {
			n++
		}
	}
	return n
}

// A bridge that attached while the session was still starting produced its
// "connected" line before the interface existed, so the line is announced from the
// session view instead — once, and only once even though the event for that same
// connection is still in the stream the interface subscribes to.
func TestBridgesThatAttachedBeforeTheInterfaceAreAnnouncedOnce(t *testing.T) {
	fake := newFakeSession()
	app := New(fake)
	app.live = fake.view

	app.announceRunningAgents()

	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		want := string(id) + " connected"
		if got := countLines(app, want); got != 1 {
			t.Fatalf("%q appears %d times after startup, want 1", want, got)
		}
	}

	// The event the coordinator emitted for that same connection is delivered now.
	app.route(events.Event{Time: time.Now(), Kind: events.KindSystem, Agent: protocol.Austin,
		Text: "Austin connected", Connection: events.ConnectionUp})
	if got := countLines(app, "Austin connected"); got != 1 {
		t.Fatalf("the connection was announced twice: %d lines", got)
	}
}

// A bridge that attaches while the interface is already running is still announced
// by its event: the view said nothing was connected, so nothing was said from it.
func TestConnectAfterTheInterfaceIsAnnouncedByItsEvent(t *testing.T) {
	fake := newFakeSession()
	av := fake.view.Agents[protocol.Austin]
	av.Connected = false
	av.Process = agent.ProcessRunning
	av.Capabilities = driver.Capabilities{SelfReports: true}
	fake.view.Agents[protocol.Austin] = av

	app := New(fake)
	app.live = fake.view
	app.announceRunningAgents()
	if got := countLines(app, "Austin connected"); got != 0 {
		t.Fatalf("Austin was announced while the view said he was not connected: %d lines", got)
	}

	app.route(events.Event{Time: time.Now(), Kind: events.KindSystem, Agent: protocol.Austin,
		Text: "Austin connected", Connection: events.ConnectionUp})
	if got := countLines(app, "Austin connected"); got != 1 {
		t.Fatalf("a late connection was not announced: %d lines", got)
	}
}

// A disconnect is news, and so is the reconnect after it: the marker only
// suppresses the duplicate of a connection the interface already reported.
func TestReconnectAfterDisconnectIsAnnouncedAgain(t *testing.T) {
	fake := newFakeSession()
	app := New(fake)
	app.live = fake.view
	app.announceRunningAgents()

	app.route(events.Event{Time: time.Now(), Kind: events.KindSystem, Agent: protocol.Austin,
		Text: "Austin disconnected", Connection: events.ConnectionDown})
	if got := countLines(app, "Austin disconnected"); got != 1 {
		t.Fatalf("the disconnect was not shown: %d lines", got)
	}

	app.route(events.Event{Time: time.Now(), Kind: events.KindSystem, Agent: protocol.Austin,
		Text: "Austin connected", Connection: events.ConnectionUp})
	if got := countLines(app, "Austin connected"); got != 2 {
		t.Fatalf("the reconnect was not announced: %d lines, want 2", got)
	}
}

// A connection the view no longer reports is over even when its disconnect event
// never reached the interface, so the next attach must be announced again.
func TestConnectionTheViewDroppedIsAnnouncedAgain(t *testing.T) {
	fake := newFakeSession()
	app := New(fake)
	app.live = fake.view
	app.announceRunningAgents()

	av := fake.view.Agents[protocol.Austin]
	av.Connected = false
	av.Capabilities = driver.Capabilities{SelfReports: true}
	fake.view.Agents[protocol.Austin] = av
	app.refreshView()

	app.route(events.Event{Time: time.Now(), Kind: events.KindSystem, Agent: protocol.Austin,
		Text: "Austin connected", Connection: events.ConnectionUp})
	if got := countLines(app, "Austin connected"); got != 2 {
		t.Fatalf("a connection the view had dropped was swallowed: %d lines, want 2", got)
	}
}

// A driver that cannot attach a bridge until its agent speaks is announced when its
// process is up, which is what the view reports — not from a connection event that
// such a driver never sends.
func TestDriverWithoutABridgeIsAnnouncedFromItsProcess(t *testing.T) {
	fake := newFakeSession()
	av := fake.view.Agents[protocol.Tony]
	av.Connected = false
	av.Process = agent.ProcessRunning
	av.Capabilities = driver.Capabilities{SelfReports: false}
	fake.view.Agents[protocol.Tony] = av

	app := New(fake)
	app.live = fake.view
	app.announceRunningAgents()

	if got := countLines(app, "Tony connected"); got != 1 {
		t.Fatalf("a running driver without a bridge was announced %d times, want 1", got)
	}
}
