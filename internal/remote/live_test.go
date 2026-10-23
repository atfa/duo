package remote

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestLiveRelayRoundTrip drives this client against a real relay — the shipped
// Worker, its Durable Object and the WebSocket Hibernation handlers — rather
// than the in-process fake.
//
// It is opt-in because it needs a Worker runtime:
//
//	(cd remote && wrangler dev --local --port 8787) &
//	DUO_RELAY_BASE=http://127.0.0.1:8787 go test ./internal/remote -run Live
//
// The hermetic gate stays `go test ./...`; this test skips without the variable.
func TestLiveRelayRoundTrip(t *testing.T) {
	base := strings.TrimSpace(os.Getenv("DUO_RELAY_BASE"))
	if base == "" {
		t.Skip("DUO_RELAY_BASE is not set; see scripts/remote-e2e.sh")
	}
	pairing, err := NewPairing(base, time.Now(), 10*time.Minute)
	if err != nil {
		t.Fatalf("NewPairing: %v", err)
	}
	sink := &fakeSink{}
	client, err := New(Options{
		Pairing:   pairing,
		Sink:      sink,
		Snapshot:  staticState("duo"),
		RetryBase: 50 * time.Millisecond,
		RetryMax:  300 * time.Millisecond,
		Logf:      t.Logf,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	startClient(t, client)
	waitFor(t, "host connection", func() bool { return client.Status().Connected })

	guest := liveGuest(t, pairing)
	state := guest.nextState(t, pairing, 10*time.Second)
	if state.Project != "duo" || state.Session != "session-1" {
		t.Fatalf("guest state = %+v", state)
	}
	if state.N == 0 {
		t.Fatal("the state carried no command nonce")
	}

	sendCommand(t, guest, pairing, "cmd-live-1", CmdTask, "run the live tests", state.N)
	waitFor(t, "task dispatch", func() bool {
		tasks, _, _ := sink.snapshot()
		return len(tasks) == 1 && tasks[0] == "run the live tests"
	})

	// The relay's own rate limit and caps are covered by its tests; what this
	// checks is that revocation ends the connection for good.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Revoke(ctx); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	waitFor(t, "client to stop after revoke", func() bool { return !client.Status().Active })
}

// liveGuest is the page's role, in Go: it proves the relay accepts a guest that
// only holds the pairing secret.
func liveGuest(t *testing.T, pairing Pairing) *fakePeer {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(pairing.Server, "http") + "/ws?room=" + pairing.Room
	dialer := &websocket.Dialer{Subprotocols: []string{Subprotocol}, HandshakeTimeout: 10 * time.Second}
	conn, resp, err := dialer.Dial(wsURL, nil)
	if err != nil {
		status := ""
		if resp != nil {
			status = resp.Status
		}
		t.Fatalf("guest dial: %v (%s)", err, status)
	}
	t.Cleanup(func() { _ = conn.Close() })
	peer := &fakePeer{room: pairing.Room, role: RoleGuest, conn: conn}
	proof, err := pairing.Proof()
	if err != nil {
		t.Fatalf("proof: %v", err)
	}
	peer.send(frame{T: frameHello, V: ProtocolVersion, Role: RoleGuest, Room: pairing.Room, Proof: proof})
	ready := peer.read(t, 10*time.Second)
	if ready.T != frameReady {
		t.Fatalf("guest handshake: got %+v", ready)
	}
	return peer
}

// TestLivePageCommandRoundTrip is the cross-implementation check: the phone's
// own modules (remote/public/console.js and crypto.js, DOM-free by design) act
// as the guest against the real Worker, and this Go client is the host. A task
// signed by the page has to reach the Go sink, and the Go answer has to come
// back to the page.
func TestLivePageCommandRoundTrip(t *testing.T) {
	base := strings.TrimSpace(os.Getenv("DUO_RELAY_BASE"))
	if base == "" {
		t.Skip("DUO_RELAY_BASE is not set; see scripts/remote-e2e.sh")
	}
	// A test runs in its package directory, so the checkout root is two levels up.
	script, err := filepath.Abs(filepath.Join("..", "..", "remote", "e2e", "page.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Skipf("the page guest is not in the checkout: %v", err)
	}
	pairing, err := NewPairing(base, time.Now(), 10*time.Minute)
	if err != nil {
		t.Fatalf("NewPairing: %v", err)
	}
	sink := &fakeSink{}
	client, err := New(Options{
		Pairing:  pairing,
		Sink:     sink,
		Snapshot: staticState("duo"),
		Logf:     t.Logf,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	startClient(t, client)
	waitFor(t, "host connection", func() bool { return client.Status().Connected })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	page := exec.CommandContext(ctx, "node", script, base, pairing.Room, pairing.Key, "from the page")
	page.Stdout = os.Stderr
	page.Stderr = os.Stderr
	if err := page.Start(); err != nil {
		t.Fatalf("start the page guest: %v", err)
	}
	waitFor(t, "the page's task", func() bool {
		tasks, _, _ := sink.snapshot()
		return len(tasks) > 0
	})
	if err := page.Wait(); err != nil {
		t.Fatalf("the page guest failed: %v", err)
	}
	tasks, _, _ := sink.snapshot()
	if tasks[0] != "from the page" {
		t.Fatalf("the host received %q", tasks[0])
	}
}
