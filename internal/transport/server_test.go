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

func (h *testHandler) OnConnect(_ context.Context, client *Client)        { h.connected <- client.Agent }
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
