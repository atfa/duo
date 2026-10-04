package transport

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/atfa/duo/internal/protocol"
)

// identityHandler records what the transport said a client is, next to what the
// message itself claimed. The two are not the same thing, and only the first one
// authorizes anything.
type identityHandler struct {
	connected chan protocol.AgentID
	messages  chan identityMessage
}

type identityMessage struct {
	identity protocol.AgentID // the transport's answer
	claimed  protocol.AgentID // what the message said about itself
}

func (h *identityHandler) OnConnect(_ context.Context, client *Client) {
	h.connected <- client.Identity()
}
func (*identityHandler) OnDisconnect(*Client) {}
func (h *identityHandler) OnMessage(_ context.Context, client *Client, m protocol.Message) {
	h.messages <- identityMessage{identity: client.Identity(), claimed: m.Agent}
}

// Identity is set in exactly one place: the validated hello. It is what the gate
// checks read, so this pins the two properties that make that safe — both agents
// are recognized, and nothing a client sends afterwards can change who it is.
func TestClientIdentityComesOnlyFromTheValidatedHello(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &identityHandler{
		connected: make(chan protocol.AgentID, 4),
		messages:  make(chan identityMessage, 4),
	}
	s := NewServer("127.0.0.1:0", "session-1", "secret")
	s.SetHandler(h)
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServe(ctx) }()
	<-s.Ready()

	for _, tc := range []struct {
		declared protocol.AgentID
		want     protocol.AgentID
	}{
		{protocol.Austin, protocol.Austin},
		{protocol.Tony, protocol.Tony},
	} {
		other := protocol.Austin
		if tc.want == protocol.Austin {
			other = protocol.Tony
		}

		conn, err := net.Dial("tcp", s.Addr())
		if err != nil {
			t.Fatal(err)
		}
		encode := json.NewEncoder(conn)
		if err := encode.Encode(protocol.Message{
			Version: protocol.Version, Type: protocol.MsgHello,
			Agent: tc.declared, SessionID: "session-1", Token: "secret",
		}); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-h.connected:
			if got != tc.want {
				t.Fatalf("declaring %q connected as %q", tc.declared, got)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("declaring %q did not connect", tc.declared)
		}

		// A later message may name the other agent. That is a claim about the
		// message, not an identity, and it must not become one.
		if err := encode.Encode(protocol.Message{Type: protocol.MsgPeerMessage, Agent: other}); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-h.messages:
			if got.identity != tc.want {
				t.Errorf("after claiming %q, the client's identity became %q, want %q", other, got.identity, tc.want)
			}
			if got.claimed != other {
				t.Errorf("message claim = %q, want it passed through as %q", got.claimed, other)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("message was not delivered")
		}
		_ = conn.Close()
	}

	// An agent Duo has never heard of is refused outright, and never becomes an
	// identity — which is also what makes the field unexported worth having.
	conn, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(protocol.Message{
		Version: protocol.Version, Type: protocol.MsgHello,
		Agent: "carol", SessionID: "session-1", Token: "secret",
	}); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("a connection declaring an unknown agent was kept open")
	}
	select {
	case got := <-h.connected:
		t.Fatalf("an unknown agent connected as %q", got)
	default:
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
