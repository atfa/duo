package coordinator

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/transport"
)

func startWakeServer(t *testing.T, resume bool) (*transport.Server, *Coordinator, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	server := transport.NewServer("127.0.0.1:0", "wake-test", "wake-token")
	coord := New(server, project.NewState(), harness.NewTracker(), promptCoordinator().workspace, events.NewBus())
	if resume {
		coord.EnableResumeWake()
	}
	server.SetHandler(coord)
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe(ctx) }()
	select {
	case <-server.Ready():
	case err := <-errCh:
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("wake server did not stop")
		}
	})
	return server, coord, cancel
}

func connectWakeAgent(t *testing.T, server *transport.Server, agent protocol.AgentID) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", server.Addr())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(conn).Encode(protocol.Message{Version: protocol.Version, Type: protocol.MsgHello, Agent: agent, SessionID: "wake-test", Token: "wake-token"}); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	return conn
}

func readWake(t *testing.T, conn net.Conn) protocol.Message {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	defer conn.SetReadDeadline(time.Time{})
	var message protocol.Message
	if err := json.NewDecoder(conn).Decode(&message); err != nil {
		t.Fatal(err)
	}
	return message
}

func TestResumeWakeIsIndependentAndOnlyOnce(t *testing.T) {
	for _, order := range [][]protocol.AgentID{{protocol.Austin, protocol.Tony}, {protocol.Tony, protocol.Austin}} {
		t.Run(string(order[0])+" first", func(t *testing.T) {
			server, _, _ := startWakeServer(t, true)
			first := connectWakeAgent(t, server, order[0])
			defer first.Close()
			if got := readWake(t, first); got.Type != protocol.MsgResumePrompt || got.To != order[0] {
				t.Fatalf("first wake = %+v", got)
			}
			second := connectWakeAgent(t, server, order[1])
			defer second.Close()
			if got := readWake(t, second); got.Type != protocol.MsgResumePrompt || got.To != order[1] {
				t.Fatalf("second wake = %+v", got)
			}

			_ = second.Close()
			for deadline := time.Now().Add(time.Second); server.IsConnected(order[1]) && time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			}
			reconnected := connectWakeAgent(t, server, order[1])
			defer reconnected.Close()
			if err := reconnected.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			var duplicate protocol.Message
			err := json.NewDecoder(reconnected).Decode(&duplicate)
			if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
				t.Fatalf("reconnect unexpectedly received %+v (err=%v)", duplicate, err)
			}
		})
	}
}

func TestNewResumedRuntimeWakesAgain(t *testing.T) {
	for run := 0; run < 2; run++ {
		server, _, _ := startWakeServer(t, true)
		tony := connectWakeAgent(t, server, protocol.Tony)
		if got := readWake(t, tony); got.Type != protocol.MsgResumePrompt {
			t.Fatalf("resume runtime %d wake = %+v", run, got)
		}
		_ = tony.Close()
	}
}

func TestResumeWakeJournalsEachAgentOnce(t *testing.T) {
	server, coord, _ := startWakeServer(t, true)
	store, err := sessionstore.New(t.TempDir(), "repo", "session")
	if err != nil {
		t.Fatal(err)
	}
	coord.journal = store.OpenEvents()
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		conn := connectWakeAgent(t, server, agent)
		if got := readWake(t, conn); got.Type != protocol.MsgResumePrompt {
			t.Fatalf("%s wake = %+v", agent, got)
		}
		_ = conn.Close()
	}
	var data []byte
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		data, err = os.ReadFile(store.EventsPath())
		if err == nil && strings.Count(string(data), `"type":"resume_wake_sent"`) == 2 {
			return
		}
	}
	t.Fatalf("resume wake journal count = %d, want 2:\n%s", strings.Count(string(data), `"type":"resume_wake_sent"`), data)
}

func TestFreshConnectionDoesNotWake(t *testing.T) {
	server, _, _ := startWakeServer(t, false)
	conn := connectWakeAgent(t, server, protocol.Austin)
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var message protocol.Message
	err := json.NewDecoder(conn).Decode(&message)
	if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("fresh connection unexpectedly received %+v (err=%v)", message, err)
	}
}
