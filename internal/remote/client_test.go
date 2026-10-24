package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

func TestRegisterCompletesBeforePairingCanBeShown(t *testing.T) {
	relay := newFakeRelay(t)
	pairing := testPairing(t, relay)
	client, err := New(Options{Pairing: pairing, Sink: &fakeSink{}, Snapshot: staticState("register")})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.RegisterRoom(context.Background()); err != nil {
		t.Fatalf("RegisterRoom: %v", err)
	}
	relay.mu.Lock()
	_, registered := relay.rooms[pairing.Room]
	relay.mu.Unlock()
	if !registered {
		t.Fatal("RegisterRoom returned before the room was created")
	}
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

// A command the session refuses has to be visible on the phone: it is the only
// place the human is looking, and a silent local log reads as a lost message.
func TestPublishedStateCarriesTheCommandResult(t *testing.T) {
	for _, tc := range []struct {
		name     string
		id, kind string
		text     string
		mutate   func(t *testing.T, guest *fakePeer, p Pairing, n int64)
		accepted bool
		notes    string
	}{
		{
			name: "accepted", id: "cmd-a", kind: CmdTask, text: "run the tests",
			accepted: true,
		},
		{
			name: "empty task", id: "cmd-e", kind: CmdTask, text: "   ",
			accepted: false, notes: "empty",
		},
		{
			name: "stale nonce", id: "cmd-s", kind: CmdTask, text: "go",
			mutate: func(t *testing.T, guest *fakePeer, p Pairing, n int64) {
				sendCommand(t, guest, p, "cmd-s", CmdTask, "go", n+500)
			},
			accepted: false, notes: "older state",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			relay := newFakeRelay(t)
			p := testPairing(t, relay)
			sink := &fakeSink{}
			c, err := New(Options{Pairing: p, Sink: sink, Snapshot: staticState("duo"),
				RetryBase: 20 * time.Millisecond, RetryMax: 100 * time.Millisecond, Logf: t.Logf})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			startClient(t, c)
			waitFor(t, "host connection", func() bool { return c.Status().Connected })
			guest := relay.guest(t, p)
			state := guest.nextState(t, p, 5*time.Second)

			if tc.mutate != nil {
				tc.mutate(t, guest, p, state.N)
			} else {
				sendCommand(t, guest, p, tc.id, tc.kind, tc.text, state.N)
			}
			next := guest.nextState(t, p, 5*time.Second)
			if next.Command == nil {
				t.Fatal("the republished state carried no command result")
			}
			if next.Command.ID != tc.id {
				t.Errorf("result id = %q, want %q", next.Command.ID, tc.id)
			}
			if next.Command.Accepted != tc.accepted {
				t.Errorf("accepted = %v, want %v (note %q)", next.Command.Accepted, tc.accepted, next.Command.Note)
			}
			if tc.notes != "" && !strings.Contains(next.Command.Note, tc.notes) {
				t.Errorf("note = %q, want it to mention %q", next.Command.Note, tc.notes)
			}
		})
	}
}

// A repeated command is a retry after a lost ack, so it must not run twice and
// must say so on the phone.
func TestRepeatedCommandIsReportedAsAlreadyExecuted(t *testing.T) {
	relay := newFakeRelay(t)
	p := testPairing(t, relay)
	sink := &fakeSink{}
	c, err := New(Options{Pairing: p, Sink: sink, Snapshot: staticState("duo"),
		RetryBase: 20 * time.Millisecond, RetryMax: 100 * time.Millisecond, Logf: t.Logf})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	startClient(t, c)
	waitFor(t, "host connection", func() bool { return c.Status().Connected })
	guest := relay.guest(t, p)
	state := guest.nextState(t, p, 5*time.Second)

	sendCommand(t, guest, p, "cmd-dup", CmdTask, "go", state.N)
	waitFor(t, "first execution", func() bool {
		tasks, _, _ := sink.snapshot()
		return len(tasks) == 1
	})
	// The relay dedupes by id, so a retry has to reach the host directly.
	if err := c.PublishEvent(Event{Kind: EventActivity, Agent: "austin", Text: "working"}); err != nil {
		t.Fatalf("PublishEvent: %v", err)
	}
	plaintext, err := json.Marshal(Command{V: ProtocolVersion, ID: "cmd-dup", Kind: CmdTask, Text: "go",
		N: state.N, MAC: mustMAC(t, p, "cmd-dup", CmdTask, state.N, "go")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	iv, ct, err := p.SealCommand("cmd-dup", plaintext)
	if err != nil {
		t.Fatalf("SealCommand: %v", err)
	}
	c.handleCommand(frame{T: frameCmd, ID: "cmd-dup", IV: iv, CT: ct})
	if tasks, _, _ := sink.snapshot(); len(tasks) != 1 {
		t.Errorf("the repeated command ran %d times, want 1", len(tasks))
	}
	c.PublishState()
	next := guest.nextState(t, p, 5*time.Second)
	if next.Command == nil || next.Command.Accepted {
		t.Fatalf("the repeated command was not reported as refused: %+v", next.Command)
	}
	if !strings.Contains(next.Command.Note, "already") {
		t.Errorf("note = %q, want it to mention that the command already ran", next.Command.Note)
	}
}

func TestEventPushReachesTheGuest(t *testing.T) {
	relay := newFakeRelay(t)
	p := testPairing(t, relay)
	sink := &fakeSink{}
	c, err := New(Options{Pairing: p, Sink: sink, Snapshot: staticState("duo"),
		RetryBase: 20 * time.Millisecond, RetryMax: 100 * time.Millisecond, Logf: t.Logf})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	startClient(t, c)
	waitFor(t, "host connection", func() bool { return c.Status().Connected })
	guest := relay.guest(t, p)
	guest.nextState(t, p, 5*time.Second)

	if err := c.PublishEvent(Event{Kind: EventActivity, Agent: "tony", Text: "editing room.js"}); err != nil {
		t.Fatalf("PublishEvent: %v", err)
	}
	plaintext := guest.nextMessage(t, p, KindEvent, 5*time.Second)
	var ev Event
	if err := json.Unmarshal(plaintext, &ev); err != nil {
		t.Fatalf("parse event: %v", err)
	}
	if ev.Kind != EventActivity || ev.Agent != "tony" || ev.Text != "editing room.js" {
		t.Errorf("event = %+v", ev)
	}
	if ev.N == 0 {
		t.Error("the event carried no nonce, so a command could not be tied to it")
	}
}

func mustMAC(t *testing.T, p Pairing, id, kind string, n int64, text string) string {
	t.Helper()
	mac, err := p.SignCommand(id, kind, n, text)
	if err != nil {
		t.Fatalf("SignCommand: %v", err)
	}
	return mac
}

// /new keeps the phone: the pairing file survives, the relaunched process takes
// the same room over, and the guest is never told to scan again. The old host is
// the only socket that learns it lost the room.
func TestHostTakeoverKeepsTheGuestConnected(t *testing.T) {
	relay := newFakeRelay(t)
	p := testPairing(t, relay)
	firstSink := &fakeSink{}
	first, err := New(Options{Pairing: p, Sink: firstSink, Snapshot: staticState("session-one"),
		RetryBase: 20 * time.Millisecond, RetryMax: 100 * time.Millisecond, Logf: t.Logf})
	if err != nil {
		t.Fatalf("New first: %v", err)
	}
	startClient(t, first)
	waitFor(t, "first host", func() bool { return first.Status().Connected })

	guest := relay.guest(t, p)
	if got := guest.nextState(t, p, 5*time.Second).Project; got != "session-one" {
		t.Fatalf("guest saw project %q", got)
	}

	// The relaunched process: same pairing, new session id, new socket.
	secondSink := &fakeSink{}
	second, err := New(Options{Pairing: p, Sink: secondSink, Snapshot: staticState("session-two"),
		RetryBase: 20 * time.Millisecond, RetryMax: 100 * time.Millisecond, Logf: t.Logf})
	if err != nil {
		t.Fatalf("New second: %v", err)
	}
	startClient(t, second)
	waitFor(t, "second host", func() bool { return second.Status().Connected })
	waitFor(t, "first host replaced", func() bool { return !first.Status().Connected })

	// The guest keeps the same socket and sees the new session's state, so it
	// does not have to rescan.
	next := guest.nextState(t, p, 10*time.Second)
	if next.Project != "session-two" {
		t.Fatalf("guest saw project %q after takeover, want session-two", next.Project)
	}

	// A command sent after takeover reaches the new process, not the old one.
	sendCommand(t, guest, p, "cmd-after-takeover", CmdTask, "continue", next.N)
	waitFor(t, "task on the new host", func() bool {
		tasks, _, _ := secondSink.snapshot()
		return len(tasks) == 1
	})
	if tasks, _, _ := firstSink.snapshot(); len(tasks) != 0 {
		t.Errorf("the replaced host executed %d task(s), want 0", len(tasks))
	}
}

// logRecorder keeps the client's log lines so a test can assert what an operator
// would find in the session log afterwards.
type logRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (l *logRecorder) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logRecorder) contains(substr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, substr) {
			return true
		}
	}
	return false
}

// Every host frame costs the room's daily relay budget, so a session nobody is
// watching must not publish at all: the state a phone needs is sent when it
// joins, not while it is away.
func TestNoStateIsPublishedWithoutAGuest(t *testing.T) {
	relay := newFakeRelay(t)
	p := testPairing(t, relay)
	c, err := New(Options{Pairing: p, Sink: &fakeSink{}, Snapshot: staticState("duo"),
		RetryBase: 20 * time.Millisecond, RetryMax: 100 * time.Millisecond, Logf: t.Logf})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	startClient(t, c)
	waitFor(t, "host connection", func() bool { return c.Status().Connected })
	waitFor(t, "the initial state", func() bool { return relay.published(p.Room) >= 1 })

	initial := relay.published(p.Room)
	for i := 0; i < 5; i++ {
		c.PublishState()
		if err := c.PublishEvent(Event{Kind: EventActivity, Agent: "austin", Text: "nobody is reading"}); err != nil {
			t.Fatalf("PublishEvent: %v", err)
		}
	}
	time.Sleep(pushInterval + 300*time.Millisecond)
	if got := relay.published(p.Room); got != initial {
		t.Fatalf("published %d frame(s) with no phone attached, want %d", got, initial)
	}

	// A phone joins: it gets the current state without the host publishing on a
	// timer, and publishing works again from then on.
	guest := relay.guest(t, p)
	state := guest.nextState(t, p, 5*time.Second)
	if state.Project != "duo" {
		t.Fatalf("guest state project = %q", state.Project)
	}
	c.PublishState()
	guest.nextState(t, p, 5*time.Second)
	if got := relay.published(p.Room); got <= initial {
		t.Fatalf("published = %d after a guest joined, want > %d", got, initial)
	}
}

// A room the relay ends for good used to stop the client in silence: the phone
// said offline and nothing on the computer explained why.
func TestTerminalStopIsLogged(t *testing.T) {
	relay := newFakeRelay(t)
	p := testPairing(t, relay)
	logs := &logRecorder{}
	c, err := New(Options{Pairing: p, Sink: &fakeSink{}, Snapshot: staticState("duo"),
		RetryBase: 20 * time.Millisecond, RetryMax: 100 * time.Millisecond, Logf: logs.logf})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	startClient(t, c)
	waitFor(t, "host connection", func() bool { return c.Status().Connected })
	relay.endRoom(t, p, CodeOverBudget)
	waitFor(t, "client to stop", func() bool { return !c.Status().Active })
	if !strings.Contains(c.Status().LastError, CodeOverBudget) {
		t.Errorf("LastError = %q, want it to name %q", c.Status().LastError, CodeOverBudget)
	}
	if !logs.contains(CodeOverBudget) {
		t.Errorf("log lines = %v, want the terminal reason", logs.lines)
	}
}
