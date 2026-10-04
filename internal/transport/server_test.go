package transport

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/atfa/duo/internal/protocol"
)

type testHandler struct{ connected chan protocol.AgentID }

func (h *testHandler) OnConnect(_ context.Context, client *Client)        { h.connected <- client.Identity() }
func (*testHandler) OnDisconnect(*Client)                                 {}
func (*testHandler) OnMessage(context.Context, *Client, protocol.Message) {}

func TestServerAuthenticatesHelloOnDynamicPort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &testHandler{connected: make(chan protocol.AgentID, 1)}
	s := NewServer("127.0.0.1:0", "session-1", "secret")
	s.SetHandler(h)
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServe(ctx) }()
	<-s.Ready()

	_, port, err := net.SplitHostPort(s.Addr())
	if err != nil || port == "0" {
		t.Fatalf("dynamic address = %q, err=%v", s.Addr(), err)
	}

	sendHello := func(message protocol.Message) net.Conn {
		conn, err := net.Dial("tcp", s.Addr())
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(conn).Encode(message); err != nil {
			t.Fatal(err)
		}
		return conn
	}

	bad := sendHello(protocol.Message{Version: 1, Type: protocol.MsgHello, Agent: protocol.Austin, SessionID: "session-1", Token: "wrong"})
	defer bad.Close()
	if err := bad.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := bad.Read(make([]byte, 1)); err == nil {
		t.Fatal("unauthorized connection remained open")
	}
	if s.IsConnected(protocol.Austin) {
		t.Fatal("unauthorized client connected")
	}

	good := sendHello(protocol.Message{Version: 1, Type: protocol.MsgHello, Agent: protocol.Austin, SessionID: "session-1", Token: "secret"})
	defer good.Close()
	select {
	case agent := <-h.connected:
		if agent != protocol.Austin {
			t.Fatalf("connected agent = %q", agent)
		}
	case <-time.After(time.Second):
		t.Fatal("authorized client did not connect")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type mcpTestHandler struct {
	connected    chan protocol.AgentID
	disconnected chan protocol.AgentID
	messages     chan protocol.Message
}

func (h *mcpTestHandler) OnConnect(_ context.Context, client *Client) {
	h.connected <- client.Identity()
}
func (h *mcpTestHandler) OnDisconnect(client *Client) {
	h.disconnected <- client.Identity()
}
func (h *mcpTestHandler) OnMessage(_ context.Context, _ *Client, msg protocol.Message) {
	h.messages <- msg
}

func TestServerIgnoresMCPClientRegistrationAndDisconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &mcpTestHandler{
		connected:    make(chan protocol.AgentID, 2),
		disconnected: make(chan protocol.AgentID, 2),
		messages:     make(chan protocol.Message, 2),
	}
	s := NewServer("127.0.0.1:0", "session-1", "secret")
	s.SetHandler(h)
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServe(ctx) }()
	<-s.Ready()

	conn, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatal(err)
	}

	// 1. Send Hello with ClientType: "mcp"
	hello := protocol.Message{
		Version:    1,
		Type:       protocol.MsgHello,
		ClientType: "mcp",
		Agent:      protocol.Austin,
		SessionID:  "session-1",
		Token:      "secret",
	}
	if err := json.NewEncoder(conn).Encode(hello); err != nil {
		t.Fatal(err)
	}

	// Should NOT emit OnConnect and s.IsConnected should remain false
	select {
	case ag := <-h.connected:
		t.Fatalf("unexpected OnConnect for MCP client: %v", ag)
	case <-time.After(50 * time.Millisecond):
	}

	if s.IsConnected(protocol.Austin) {
		t.Fatal("s.IsConnected(protocol.Austin) = true for MCP client, want false")
	}

	// 2. Tool message from MCP client should still carry client.Identity()
	toolMsg := protocol.Message{
		Version:   1,
		Type:      protocol.MsgGetStatus,
		RequestID: "req-1",
	}
	if err := json.NewEncoder(conn).Encode(toolMsg); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-h.messages:
		if got.Agent != protocol.Austin {
			t.Fatalf("tool message Agent = %q, want Austin", got.Agent)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for MCP tool message")
	}

	// 3. Close MCP connection: should NOT emit OnDisconnect
	_ = conn.Close()
	select {
	case ag := <-h.disconnected:
		t.Fatalf("unexpected OnDisconnect for MCP client: %v", ag)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	_ = <-done
}
