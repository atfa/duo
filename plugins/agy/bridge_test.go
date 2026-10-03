package agy

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/transport"
)

// recordHandler is a transport handler that keeps what arrived. The tests here
// need the server's real view — connect, message, disconnect — because that is
// what Core branches on.
type recordHandler struct {
	mu          sync.Mutex
	connects    []protocol.AgentID
	disconnects []protocol.AgentID
	msgs        []protocol.Message
}

func (h *recordHandler) OnConnect(_ context.Context, c *transport.Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.connects = append(h.connects, c.Agent)
}

func (h *recordHandler) OnDisconnect(c *transport.Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.disconnects = append(h.disconnects, c.Agent)
}

func (h *recordHandler) OnMessage(_ context.Context, _ *transport.Client, m protocol.Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, m)
}

func (h *recordHandler) snapshot() (connects, disconnects int, msgs []protocol.Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.connects), len(h.disconnects), append([]protocol.Message(nil), h.msgs...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestBridgeSinkOwnsASingleRegisteredEndpoint pins the two things an
// out-of-process Agent Adapter must get right, because either failure is silent:
//
//   - The hello must not say "mcp". The server treats that value as a control
//     client and keeps it out of the agent's endpoint slot, so Core would see
//     agy as disconnected while it is in fact working. A value of "plugin"
//     registers, fires the connect event, and lets the eviction invariant be
//     enforced.
//   - One sink reuses one connection. Reconnecting per message would look to
//     Core like the agent flapping.
func TestBridgeSinkOwnsASingleRegisteredEndpoint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &recordHandler{}
	s := transport.NewServer("127.0.0.1:0", "session-1", "secret")
	s.SetHandler(h)
	go func() { _ = s.ListenAndServe(ctx) }()
	<-s.Ready()

	host, port, err := net.SplitHostPort(s.Addr())
	if err != nil {
		t.Fatalf("split %q: %v", s.Addr(), err)
	}
	sink := newBridgeSink(protocol.Austin, "session-1", "secret", host, port)

	// Two observations: both must arrive on one connection.
	sink.OnActivity(protocol.Austin, protocol.Message{Type: protocol.MsgActivity, Text: "one"})
	sink.OnActivity(protocol.Austin, protocol.Message{Type: protocol.MsgActivity, Text: "two"})

	waitFor(t, "the endpoint to register", func() bool { return s.IsConnected(protocol.Austin) })
	waitFor(t, "both messages to arrive", func() bool {
		_, _, msgs := h.snapshot()
		return len(msgs) >= 2
	})
	connects, disconnects, msgs := h.snapshot()
	if connects != 1 {
		t.Errorf("connections = %d, want 1: a sink must reuse its endpoint, not reconnect per message", connects)
	}
	if disconnects != 0 {
		t.Errorf("disconnects = %d, want 0", disconnects)
	}
	if msgs[0].Type != protocol.MsgActivity || msgs[0].Agent != protocol.Austin || msgs[0].Text != "one" {
		t.Errorf("message 0 = %+v, want the watcher's own fields intact", msgs[0])
	}
	if msgs[1].Text != "two" {
		t.Errorf("message 1 = %+v, want text %q", msgs[1], "two")
	}

	// Releasing must actually unregister, or the agent stays "connected" forever.
	// The handler's disconnect record lands asynchronously after the server drops
	// the client, so wait for both or a loaded machine snapshots in between.
	sink.Close()
	waitFor(t, "the endpoint to release", func() bool {
		_, disconnects, _ := h.snapshot()
		return !s.IsConnected(protocol.Austin) && disconnects >= 1
	})
	connects, disconnects, _ = h.snapshot()
	if connects != 1 || disconnects != 1 {
		t.Errorf("connects/disconnects = %d/%d, want 1/1", connects, disconnects)
	}

	// A later launch attaches again with a fresh sink.
	sink2 := newBridgeSink(protocol.Austin, "session-1", "secret", host, port)
	sink2.OnActivity(protocol.Austin, protocol.Message{Type: protocol.MsgActivity, Text: "three"})
	waitFor(t, "the endpoint to register again", func() bool { return s.IsConnected(protocol.Austin) })
	waitFor(t, "the third message", func() bool {
		_, _, msgs := h.snapshot()
		return len(msgs) >= 3
	})
	connects, _, _ = h.snapshot()
	if connects != 2 {
		t.Errorf("connections = %d, want 2", connects)
	}
	sink2.Close()
}
