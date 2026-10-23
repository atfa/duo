package remote

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

type fakeSink struct {
	mu       sync.Mutex
	tasks    []string
	newCount int
	observer int
}

func (s *fakeSink) SubmitTask(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks = append(s.tasks, text)
	return nil
}

func (s *fakeSink) NewSession() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.newCount++
	return nil
}

func (s *fakeSink) RequestObserverReport() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observer++
}

func (s *fakeSink) snapshot() ([]string, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.tasks...), s.newCount, s.observer
}

func testPairing(t *testing.T, relay *fakeRelay) Pairing {
	t.Helper()
	p, err := NewPairing(relay.url(), time.Now(), time.Hour)
	if err != nil {
		t.Fatalf("NewPairing: %v", err)
	}
	return p
}

func staticState(project string) func() State {
	return func() State {
		return State{
			Project: project,
			Repo:    "atfa/duo",
			Session: "session-1",
			Mode:    "step",
			Phase:   "EXECUTE",
			Started: true,
			Step:    &StepState{Index: 1, Total: 3, Policy: "goal", Title: "relay"},
			Agents: map[string]Agent{
				"austin": {Connected: true, Running: true, Model: "sonnet"},
				"tony":   {Connected: true, Running: false},
			},
			Activity: []Activity{{At: 1, Kind: "assistant", Agent: "austin", Text: "hello"}},
		}
	}
}

func startClient(t *testing.T, c *Client) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		c.Close()
		<-done
	})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func sendCommand(t *testing.T, guest *fakePeer, p Pairing, id, kind, text string, n int64) {
	t.Helper()
	mac, err := p.SignCommand(id, kind, n, text)
	if err != nil {
		t.Fatalf("SignCommand: %v", err)
	}
	plaintext, err := json.Marshal(Command{V: ProtocolVersion, ID: id, Kind: kind, Text: text, N: n, MAC: mac})
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}
	iv, ct, err := p.SealCommand(id, plaintext)
	if err != nil {
		t.Fatalf("SealCommand: %v", err)
	}
	guest.send(frame{T: frameCmd, ID: id, IV: iv, CT: ct})
}

func TestClientPublishesStateAndRunsAnAuthenticatedCommand(t *testing.T) {
	relay := newFakeRelay(t)
	p := testPairing(t, relay)
	sink := &fakeSink{}
	c, err := New(Options{
		Pairing:   p,
		Sink:      sink,
		Snapshot:  staticState("duo"),
		RetryBase: 20 * time.Millisecond,
		RetryMax:  100 * time.Millisecond,
		Logf:      t.Logf,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	startClient(t, c)
	waitFor(t, "host connection", func() bool { return c.Status().Connected })

	guest := relay.guest(t, p)
	state := guest.nextState(t, p, 5*time.Second)
	if state.Project != "duo" || state.Phase != "EXECUTE" || state.Step == nil || state.Step.Total != 3 {
		t.Fatalf("guest state = %+v", state)
	}
	if state.N == 0 {
		t.Fatal("state carried no command nonce")
	}

	sendCommand(t, guest, p, "cmd-1", CmdTask, "run the tests", state.N)
	waitFor(t, "task dispatch", func() bool {
		tasks, _, _ := sink.snapshot()
		return len(tasks) == 1 && tasks[0] == "run the tests"
	})

	// A command makes the host republish, so the phone sees the effect.
	after := guest.nextState(t, p, 5*time.Second)
	if after.N == state.N {
		t.Error("the republished state reused the command nonce")
	}
}

func TestClientDropsForgedStaleAndRepeatedCommands(t *testing.T) {
	relay := newFakeRelay(t)
	p := testPairing(t, relay)
	sink := &fakeSink{}
	c, err := New(Options{
		Pairing:   p,
		Sink:      sink,
		Snapshot:  staticState("duo"),
		RetryBase: 20 * time.Millisecond,
		RetryMax:  100 * time.Millisecond,
		Logf:      t.Logf,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	startClient(t, c)
	waitFor(t, "host connection", func() bool { return c.Status().Connected })
	guest := relay.guest(t, p)
	state := guest.nextState(t, p, 5*time.Second)

	// A MAC over different text does not authenticate.
	mac, err := p.SignCommand("cmd-bad", CmdTask, state.N, "safe text")
	if err != nil {
		t.Fatalf("SignCommand: %v", err)
	}
	plaintext, _ := json.Marshal(Command{V: 1, ID: "cmd-bad", Kind: CmdTask, Text: "rm -rf /", N: state.N, MAC: mac})
	iv, ct, err := p.SealCommand("cmd-bad", plaintext)
	if err != nil {
		t.Fatalf("SealCommand: %v", err)
	}
	guest.send(frame{T: frameCmd, ID: "cmd-bad", IV: iv, CT: ct})

	// A valid MAC for a nonce this session never issued is a replay from before.
	sendCommand(t, guest, p, "cmd-stale", CmdTask, "stale", state.N+999)

	// One valid command runs exactly once, even if the relay is bypassed and the
	// client sees the same id twice.
	sendCommand(t, guest, p, "cmd-ok", CmdTask, "go", state.N)
	waitFor(t, "valid task", func() bool {
		tasks, _, _ := sink.snapshot()
		return len(tasks) == 1
	})
	if c.markSeen("cmd-ok") != true {
		t.Error("the client did not remember an executed command id")
	}

	tasks, _, _ := sink.snapshot()
	if len(tasks) != 1 || tasks[0] != "go" {
		t.Errorf("executed tasks = %v, want only [go]", tasks)
	}
}

func TestClientReconnectsAndRepublishes(t *testing.T) {
	relay := newFakeRelay(t)
	p := testPairing(t, relay)
	sink := &fakeSink{}
	c, err := New(Options{
		Pairing:   p,
		Sink:      sink,
		Snapshot:  staticState("duo"),
		RetryBase: 20 * time.Millisecond,
		RetryMax:  100 * time.Millisecond,
		Logf:      t.Logf,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	startClient(t, c)
	waitFor(t, "host connection", func() bool { return c.Status().Connected })
	guest := relay.guest(t, p)
	first := guest.nextState(t, p, 5*time.Second)
	firstSeq := guest.seqOf()

	relay.dropHost(t, p.Room)
	second := guest.nextState(t, p, 10*time.Second)
	if second.Project != first.Project {
		t.Errorf("republished state project = %q, want %q", second.Project, first.Project)
	}
	if guest.seqOf() <= firstSeq {
		t.Errorf("republished state seq = %d, want > %d", guest.seqOf(), firstSeq)
	}
	waitFor(t, "reconnected host", func() bool { return c.Status().Connected })
}

func TestClientStopsWhenTheRoomIsRevoked(t *testing.T) {
	relay := newFakeRelay(t)
	p := testPairing(t, relay)
	sink := &fakeSink{}
	c, err := New(Options{
		Pairing:   p,
		Sink:      sink,
		Snapshot:  staticState("duo"),
		RetryBase: 20 * time.Millisecond,
		RetryMax:  100 * time.Millisecond,
		Logf:      t.Logf,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	startClient(t, c)
	waitFor(t, "host connection", func() bool { return c.Status().Connected })
	relay.revokeRoom(t, p)
	waitFor(t, "client to stop", func() bool { return !c.Status().Active })
	if c.Status().Connected {
		t.Error("a revoked client reports itself connected")
	}
}

func TestRoomsAreIsolated(t *testing.T) {
	relay := newFakeRelay(t)
	pAlpha := testPairing(t, relay)
	pBeta := testPairing(t, relay)
	alpha, err := New(Options{Pairing: pAlpha, Sink: &fakeSink{}, Snapshot: staticState("alpha"),
		RetryBase: 20 * time.Millisecond, RetryMax: 100 * time.Millisecond, Logf: t.Logf})
	if err != nil {
		t.Fatalf("New alpha: %v", err)
	}
	beta, err := New(Options{Pairing: pBeta, Sink: &fakeSink{}, Snapshot: staticState("beta"),
		RetryBase: 20 * time.Millisecond, RetryMax: 100 * time.Millisecond, Logf: t.Logf})
	if err != nil {
		t.Fatalf("New beta: %v", err)
	}
	startClient(t, alpha)
	startClient(t, beta)
	waitFor(t, "both hosts", func() bool { return alpha.Status().Connected && beta.Status().Connected })

	guestAlpha := relay.guest(t, pAlpha)
	guestBeta := relay.guest(t, pBeta)
	if got := guestAlpha.nextState(t, pAlpha, 5*time.Second).Project; got != "alpha" {
		t.Errorf("room alpha saw %q", got)
	}
	if got := guestBeta.nextState(t, pBeta, 5*time.Second).Project; got != "beta" {
		t.Errorf("room beta saw %q", got)
	}
}

func TestWebSocketURLRefusesCleartextRemoteRelays(t *testing.T) {
	if _, err := webSocketURL("http://relay.example", "r"); err == nil {
		t.Error("a cleartext non-local relay was accepted")
	}
	got, err := webSocketURL("http://127.0.0.1:8787/", "room")
	if err != nil {
		t.Fatalf("webSocketURL(local): %v", err)
	}
	if got != "ws://127.0.0.1:8787/ws?room=room" {
		t.Errorf("webSocketURL = %q", got)
	}
	got, err = webSocketURL("https://remote.xduo.dev", "room")
	if err != nil {
		t.Fatalf("webSocketURL(https): %v", err)
	}
	if got != "wss://remote.xduo.dev/ws?room=room" {
		t.Errorf("webSocketURL = %q", got)
	}
}

// seqOf exposes the sequence number of a state the guest already decrypted. The
// wire state has no seq field (the envelope carries it), so the test keeps the
// last envelope's sequence on the peer instead.
func (p *fakePeer) seqOf() int64 { return p.lastSeq }
