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
	mu         sync.Mutex
	tasks      []string
	newCount   int
	observer   int
	modes      []string
	modeErr    error
	drivers    []string
	driverErr  error
	catalogs   []string
	catalogErr error
	models     []string
	modelErr   error
	resume     []string
	resumeErr  error
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

func (s *fakeSink) SetMode(mode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.modeErr != nil {
		return s.modeErr
	}
	s.modes = append(s.modes, mode)
	return nil
}

func (s *fakeSink) setModeError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modeErr = err
}

func (s *fakeSink) modeSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.modes...)
}

func (s *fakeSink) SetDriver(agent, driver string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.driverErr != nil {
		return s.driverErr
	}
	s.drivers = append(s.drivers, agent+" "+driver)
	return nil
}

func (s *fakeSink) setDriverError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.driverErr = err
}

func (s *fakeSink) driverSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.drivers...)
}

func (s *fakeSink) RequestCatalog(agent string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.catalogErr != nil {
		return s.catalogErr
	}
	s.catalogs = append(s.catalogs, agent)
	return nil
}

func (s *fakeSink) SetModel(agent, reference string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.modelErr != nil {
		return s.modelErr
	}
	s.models = append(s.models, agent+" "+reference)
	return nil
}

func (s *fakeSink) setModelError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modelErr = err
}

func (s *fakeSink) catalogSnapshot() ([]string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.catalogs...), append([]string(nil), s.models...)
}

func (s *fakeSink) Pause() error      { return s.recordResume("pause") }
func (s *fakeSink) Unpause() error    { return s.recordResume("unpause") }
func (s *fakeSink) Continue() error   { return s.recordResume("continue") }
func (s *fakeSink) ResumeWork() error { return s.recordResume("resume_work") }

func (s *fakeSink) recordResume(verb string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resumeErr != nil {
		return s.resumeErr
	}
	s.resume = append(s.resume, verb)
	return nil
}

func (s *fakeSink) setResumeError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resumeErr = err
}

func (s *fakeSink) resumeSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.resume...)
}

func testPairing(t *testing.T, relay *fakeRelay) Pairing {
	t.Helper()
	p, err := NewPairing(relay.url(), time.Now(), time.Hour)
	if err != nil {
		t.Fatalf("NewPairing: %v", err)
	}
	return p
}

// testOptions is what every client test builds from. The shipped intervals are
// sized for a phone network; a suite that waits a full second per published
// state is why this package used to take 23s, so tests inject short ones.
//
// The push window stays a tenth of a second rather than a millisecond on
// purpose: publishing coalesces, so a test that reads "the next state" expects
// the changes it just made to arrive in one frame. With a millisecond window the
// ticker fires between a test's own steps and the phone reads the previous
// state. Production makes the same assumption with a second.
func testOptions(opts Options) Options {
	opts.PushInterval = 100 * time.Millisecond
	opts.KeepalivePeriod = 20 * time.Millisecond
	opts.ReadIdleTimeout = 5 * time.Second
	return opts
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
	client, err := New(testOptions(Options{Pairing: pairing, Sink: &fakeSink{}, Snapshot: staticState("register")}))
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
	c, err := New(testOptions(Options{
		Pairing:   p,
		Sink:      sink,
		Snapshot:  staticState("duo"),
		RetryBase: 20 * time.Millisecond,
		RetryMax:  100 * time.Millisecond,
		Logf:      t.Logf,
	}))
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
	c, err := New(testOptions(Options{
		Pairing:   p,
		Sink:      sink,
		Snapshot:  staticState("duo"),
		RetryBase: 20 * time.Millisecond,
		RetryMax:  100 * time.Millisecond,
		Logf:      t.Logf,
	}))
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
	c, err := New(testOptions(Options{
		Pairing:   p,
		Sink:      sink,
		Snapshot:  staticState("duo"),
		RetryBase: 20 * time.Millisecond,
		RetryMax:  100 * time.Millisecond,
		Logf:      t.Logf,
	}))
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
	c, err := New(testOptions(Options{
		Pairing:   p,
		Sink:      sink,
		Snapshot:  staticState("duo"),
		RetryBase: 20 * time.Millisecond,
		RetryMax:  100 * time.Millisecond,
		Logf:      t.Logf,
	}))
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
	alpha, err := New(testOptions(Options{Pairing: pAlpha, Sink: &fakeSink{}, Snapshot: staticState("alpha"),
		RetryBase: 20 * time.Millisecond, RetryMax: 100 * time.Millisecond, Logf: t.Logf}))
	if err != nil {
		t.Fatalf("New alpha: %v", err)
	}
	beta, err := New(testOptions(Options{Pairing: pBeta, Sink: &fakeSink{}, Snapshot: staticState("beta"),
		RetryBase: 20 * time.Millisecond, RetryMax: 100 * time.Millisecond, Logf: t.Logf}))
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
			c, err := New(testOptions(Options{Pairing: p, Sink: sink, Snapshot: staticState("duo"),
				RetryBase: 20 * time.Millisecond, RetryMax: 100 * time.Millisecond, Logf: t.Logf}))
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

// A mode switch is one more verb a human has, so it reaches the sink with the
// name the phone sent, and a host that refuses it says why instead of leaving
// the phone with a tap that did nothing.
func TestSwitchModeReachesTheSinkAndReportsRefusals(t *testing.T) {
	relay := newFakeRelay(t)
	p := testPairing(t, relay)
	sink := &fakeSink{}
	c, err := New(testOptions(Options{Pairing: p, Sink: sink, Snapshot: staticState("duo"),
		RetryBase: 20 * time.Millisecond, RetryMax: 100 * time.Millisecond, Logf: t.Logf}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	startClient(t, c)
	waitFor(t, "host connection", func() bool { return c.Status().Connected })
	guest := relay.guest(t, p)
	state := guest.nextState(t, p, 5*time.Second)

	sendCommand(t, guest, p, "cmd-mode", CmdSwitchMode, " step ", state.N)
	next := guest.nextState(t, p, 5*time.Second)
	if next.Command == nil || !next.Command.Accepted {
		t.Fatalf("the mode switch was not accepted: %+v", next.Command)
	}
	if got := sink.modeSnapshot(); len(got) != 1 || got[0] != "step" {
		t.Fatalf("sink saw modes %v, want [step] with the padding trimmed", got)
	}

	sendCommand(t, guest, p, "cmd-mode-empty", CmdSwitchMode, "   ", next.N)
	next = guest.nextState(t, p, 5*time.Second)
	if next.Command == nil || next.Command.Accepted {
		t.Fatalf("an empty mode was accepted: %+v", next.Command)
	}
	if !strings.Contains(next.Command.Note, "empty") {
		t.Errorf("note = %q, want it to mention the empty mode", next.Command.Note)
	}

	sink.setModeError(fmt.Errorf("mode is locked while a step is in review"))
	sendCommand(t, guest, p, "cmd-mode-refused", CmdSwitchMode, "goal", next.N)
	next = guest.nextState(t, p, 5*time.Second)
	if next.Command == nil || next.Command.Accepted {
		t.Fatalf("a refused mode switch was accepted: %+v", next.Command)
	}
	if !strings.Contains(next.Command.Note, "locked") {
		t.Errorf("note = %q, want the host's own reason", next.Command.Note)
	}
}

// The resume verbs are the human's own, so each reaches the sink as the verb the
// phone sent, and a refusal is reported rather than swallowed: a phone that
// believes it released a paused Duo is worse than one that shows the reason.
func TestPauseCommandReachesSink(t *testing.T) {
	relay := newFakeRelay(t)
	p := testPairing(t, relay)
	sink := &fakeSink{}
	c, err := New(testOptions(Options{Pairing: p, Sink: sink, Snapshot: staticState("duo"),
		RetryBase: 20 * time.Millisecond, RetryMax: 100 * time.Millisecond, Logf: t.Logf}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	startClient(t, c)
	waitFor(t, "host connection", func() bool { return c.Status().Connected })
	guest := relay.guest(t, p)
	state := guest.nextState(t, p, 5*time.Second)

	for i, kind := range []string{CmdPause, CmdUnpause, CmdContinue, CmdResumeWork} {
		sendCommand(t, guest, p, fmt.Sprintf("cmd-resume-%d", i), kind, "", state.N)
		state = guest.nextState(t, p, 5*time.Second)
		if state.Command == nil || !state.Command.Accepted {
			t.Fatalf("%s was not accepted: %+v", kind, state.Command)
		}
	}
	if got, want := strings.Join(sink.resumeSnapshot(), ","), "pause,unpause,continue,resume_work"; got != want {
		t.Fatalf("sink saw resume verbs %q, want %q", got, want)
	}

	sink.setResumeError(fmt.Errorf("session is running; continue requires both agents paused"))
	sendCommand(t, guest, p, "cmd-resume-refused", CmdContinue, "", state.N)
	state = guest.nextState(t, p, 5*time.Second)
	if state.Command == nil || state.Command.Accepted {
		t.Fatalf("a refused continue was accepted: %+v", state.Command)
	}
	if !strings.Contains(state.Command.Note, "requires both agents paused") {
		t.Errorf("note = %q, want the host's own reason", state.Command.Note)
	}
}

// A kind the host does not know is refused with a reason instead of being
// ignored, so a page built against a newer protocol cannot believe it paused a
// session that is still running.
func TestUnknownCommandRefused(t *testing.T) {
	relay := newFakeRelay(t)
	p := testPairing(t, relay)
	sink := &fakeSink{}
	c, err := New(testOptions(Options{Pairing: p, Sink: sink, Snapshot: staticState("duo"),
		RetryBase: 20 * time.Millisecond, RetryMax: 100 * time.Millisecond, Logf: t.Logf}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	startClient(t, c)
	waitFor(t, "host connection", func() bool { return c.Status().Connected })
	guest := relay.guest(t, p)
	state := guest.nextState(t, p, 5*time.Second)

	sendCommand(t, guest, p, "cmd-unknown", "abort", "", state.N)
	state = guest.nextState(t, p, 5*time.Second)
	if state.Command == nil || state.Command.Accepted {
		t.Fatalf("an unknown command kind was accepted: %+v", state.Command)
	}
	if !strings.Contains(state.Command.Note, "unsupported command") {
		t.Errorf("note = %q, want it to name the unsupported command", state.Command.Note)
	}
	if got := sink.resumeSnapshot(); len(got) != 0 {
		t.Errorf("an unknown command reached the sink as %v", got)
	}
}

// A driver command carries the agent and the driver in one text field. The split
// is at the first space, and the rest is kept verbatim: a value that contains
// spaces has to reach the host exactly as the phone meant it.
func TestParseAgentCommandKeepsTheValueVerbatim(t *testing.T) {
	for _, tc := range []struct{ name, text, agent, value string }{
		{name: "plain", text: "austin pi", agent: "austin", value: "pi"},
		{name: "padded", text: "  tony   opencode  ", agent: "tony", value: "opencode"},
		{name: "spaces in the value", text: "austin vendor/model with spaces", agent: "austin", value: "vendor/model with spaces"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent, value, err := ParseAgentCommand(tc.text)
			if err != nil {
				t.Fatalf("ParseAgentCommand(%q): %v", tc.text, err)
			}
			if agent != tc.agent || value != tc.value {
				t.Errorf("ParseAgentCommand(%q) = %q, %q; want %q, %q", tc.text, agent, value, tc.agent, tc.value)
			}
		})
	}
	for _, text := range []string{"", "   ", "austin", "austin   "} {
		if _, _, err := ParseAgentCommand(text); err == nil {
			t.Errorf("ParseAgentCommand(%q) accepted a command with no value", text)
		}
	}
}

// A driver change is one more verb a human has, so it reaches the sink with the
// agent and the driver the phone sent, and text that names no driver is refused
// with a reason the phone shows.
func TestSetDriverReachesTheSinkAndRefusesBadText(t *testing.T) {
	relay := newFakeRelay(t)
	p := testPairing(t, relay)
	sink := &fakeSink{}
	c, err := New(testOptions(Options{Pairing: p, Sink: sink, Snapshot: staticState("duo"),
		RetryBase: 20 * time.Millisecond, RetryMax: 100 * time.Millisecond, Logf: t.Logf}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	startClient(t, c)
	waitFor(t, "host connection", func() bool { return c.Status().Connected })
	guest := relay.guest(t, p)
	state := guest.nextState(t, p, 5*time.Second)

	sendCommand(t, guest, p, "cmd-driver", CmdSetDriver, "austin pi", state.N)
	next := guest.nextState(t, p, 5*time.Second)
	if next.Command == nil || !next.Command.Accepted {
		t.Fatalf("the driver change was not accepted: %+v", next.Command)
	}
	if got := sink.driverSnapshot(); len(got) != 1 || got[0] != "austin pi" {
		t.Fatalf("sink saw drivers %v, want [austin pi]", got)
	}

	sendCommand(t, guest, p, "cmd-driver-bare", CmdSetDriver, "pi", next.N)
	next = guest.nextState(t, p, 5*time.Second)
	if next.Command == nil || next.Command.Accepted {
		t.Fatalf("a driver command with no agent was accepted: %+v", next.Command)
	}
	if !strings.Contains(next.Command.Note, "no value") {
		t.Errorf("note = %q, want it to say the command named no value", next.Command.Note)
	}

	sink.setDriverError(fmt.Errorf("driver can only be changed before the first task"))
	sendCommand(t, guest, p, "cmd-driver-refused", CmdSetDriver, "tony codex", next.N)
	next = guest.nextState(t, p, 5*time.Second)
	if next.Command == nil || next.Command.Accepted {
		t.Fatalf("a refused driver change was accepted: %+v", next.Command)
	}
	if !strings.Contains(next.Command.Note, "before the first task") {
		t.Errorf("note = %q, want the host's own reason", next.Command.Note)
	}
}

// A long model list must never stop a state from being published: the list is
// the one part of a snapshot the phone can ask for again, so it shrinks first and
// the true count survives so the page can say it was cut.
func TestFitStateShrinksALongCatalogBeforeFailing(t *testing.T) {
	catalog := &CatalogState{Agent: "austin", Total: 300}
	for i := 0; i < 300; i++ {
		catalog.Models = append(catalog.Models, fmt.Sprintf("provider/model-with-a-long-name-%03d", i))
	}
	data, err := fitState(State{Project: "duo", Session: "session-1", Catalog: catalog})
	if err != nil {
		t.Fatalf("fitState: %v", err)
	}
	if len(data) > MaxPlaintext {
		t.Errorf("fitState returned %d bytes, limit %d", len(data), MaxPlaintext)
	}
	var got State
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Catalog == nil {
		t.Fatal("the catalog disappeared instead of shrinking")
	}
	if got.Catalog.Total != 300 {
		t.Errorf("total = %d, want the whole catalog (300)", got.Catalog.Total)
	}
	if len(got.Catalog.Models) >= 300 {
		t.Errorf("the list was not trimmed: %d models", len(got.Catalog.Models))
	}
	// The published snapshot must not mutate the cache the next one is built from.
	if len(catalog.Models) != 300 {
		t.Errorf("fitState trimmed the caller's list to %d entries", len(catalog.Models))
	}
}

// A catalog request is a read and a model change is a write: both reach the sink
// with the agent the phone named, and both report a refusal.
func TestCatalogAndSetModelReachTheSink(t *testing.T) {
	relay := newFakeRelay(t)
	p := testPairing(t, relay)
	sink := &fakeSink{}
	c, err := New(testOptions(Options{Pairing: p, Sink: sink, Snapshot: staticState("duo"),
		RetryBase: 20 * time.Millisecond, RetryMax: 100 * time.Millisecond, Logf: t.Logf}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	startClient(t, c)
	waitFor(t, "host connection", func() bool { return c.Status().Connected })
	guest := relay.guest(t, p)
	state := guest.nextState(t, p, 5*time.Second)

	sendCommand(t, guest, p, "cmd-catalog", CmdCatalog, " austin ", state.N)
	next := guest.nextState(t, p, 5*time.Second)
	if next.Command == nil || !next.Command.Accepted {
		t.Fatalf("the catalog request was not accepted: %+v", next.Command)
	}
	catalogs, _ := sink.catalogSnapshot()
	if len(catalogs) != 1 || catalogs[0] != "austin" {
		t.Fatalf("sink saw catalogs %v, want [austin]", catalogs)
	}

	sendCommand(t, guest, p, "cmd-model", CmdSetModel, "austin openai/gpt-5", next.N)
	next = guest.nextState(t, p, 5*time.Second)
	if next.Command == nil || !next.Command.Accepted {
		t.Fatalf("the model change was not accepted: %+v", next.Command)
	}
	_, models := sink.catalogSnapshot()
	if len(models) != 1 || models[0] != "austin openai/gpt-5" {
		t.Fatalf("sink saw models %v, want [austin openai/gpt-5]", models)
	}

	sendCommand(t, guest, p, "cmd-catalog-empty", CmdCatalog, "   ", next.N)
	next = guest.nextState(t, p, 5*time.Second)
	if next.Command == nil || next.Command.Accepted {
		t.Fatalf("a catalog request with no agent was accepted: %+v", next.Command)
	}
	if !strings.Contains(next.Command.Note, "no agent") {
		t.Errorf("note = %q, want it to say the request named no agent", next.Command.Note)
	}

	sink.setModelError(fmt.Errorf("\"openai/ghost\" is not in austin's model list"))
	sendCommand(t, guest, p, "cmd-model-unknown", CmdSetModel, "austin openai/ghost", next.N)
	next = guest.nextState(t, p, 5*time.Second)
	if next.Command == nil || next.Command.Accepted {
		t.Fatalf("an unknown model was accepted: %+v", next.Command)
	}
	if !strings.Contains(next.Command.Note, "model list") {
		t.Errorf("note = %q, want the host's own reason", next.Command.Note)
	}
}

// A repeated command is a retry after a lost ack, so it must not run twice and
// must say so on the phone.
func TestRepeatedCommandIsReportedAsAlreadyExecuted(t *testing.T) {
	relay := newFakeRelay(t)
	p := testPairing(t, relay)
	sink := &fakeSink{}
	c, err := New(testOptions(Options{Pairing: p, Sink: sink, Snapshot: staticState("duo"),
		RetryBase: 20 * time.Millisecond, RetryMax: 100 * time.Millisecond, Logf: t.Logf}))
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
	// States published before the refusal may still be in flight: the pacing is
	// injected short here, so the phone reads a stream. The refusal is what must
	// arrive, not the very next frame.
	var next State
	for i := 0; i < 5 && (next.Command == nil || next.Command.Accepted); i++ {
		next = guest.nextState(t, p, 5*time.Second)
	}
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
	c, err := New(testOptions(Options{Pairing: p, Sink: sink, Snapshot: staticState("duo"),
		RetryBase: 20 * time.Millisecond, RetryMax: 100 * time.Millisecond, Logf: t.Logf}))
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
	first, err := New(testOptions(Options{Pairing: p, Sink: firstSink, Snapshot: staticState("session-one"),
		RetryBase: 20 * time.Millisecond, RetryMax: 100 * time.Millisecond, Logf: t.Logf}))
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
	second, err := New(testOptions(Options{Pairing: p, Sink: secondSink, Snapshot: staticState("session-two"),
		RetryBase: 20 * time.Millisecond, RetryMax: 100 * time.Millisecond, Logf: t.Logf}))
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

// The intervals the phone sees are production's. The short ones above are a test
// fixture, so the shipped values are stated once here: a change to them is a
// change to the relay's daily frame budget and to how fast a dead socket is
// noticed, not a test detail.
func TestShippedIntervalsAreThePhoneFacingOnes(t *testing.T) {
	relay := newFakeRelay(t)
	c, err := New(Options{Pairing: testPairing(t, relay), Sink: &fakeSink{}, Snapshot: staticState("duo")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	if got := c.opts.PushInterval; got != time.Second {
		t.Errorf("push interval = %v, want 1s", got)
	}
	if got := c.opts.KeepalivePeriod; got != 30*time.Second {
		t.Errorf("keepalive period = %v, want 30s", got)
	}
	if got := c.opts.ReadIdleTimeout; got != 90*time.Second {
		t.Errorf("read idle timeout = %v, want 90s", got)
	}
}

// The keepalive is injected like the rest of the pacing, which is the only way a
// test can see it at all: waiting out the shipped 30s per ping is not a test.
func TestKeepalivePingsAtTheInjectedPeriod(t *testing.T) {
	relay := newFakeRelay(t)
	p := testPairing(t, relay)
	c, err := New(testOptions(Options{Pairing: p, Sink: &fakeSink{}, Snapshot: staticState("duo")}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	startClient(t, c)
	waitFor(t, "two keepalives", func() bool { return relay.pings(p.Room) >= 2 })
}

// Every host frame costs the room's daily relay budget, so a session nobody is
// watching must not publish at all: the state a phone needs is sent when it
// joins, not while it is away.
func TestNoStateIsPublishedWithoutAGuest(t *testing.T) {
	relay := newFakeRelay(t)
	p := testPairing(t, relay)
	c, err := New(testOptions(Options{Pairing: p, Sink: &fakeSink{}, Snapshot: staticState("duo"),
		RetryBase: 20 * time.Millisecond, RetryMax: 100 * time.Millisecond, Logf: t.Logf}))
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
	time.Sleep(3 * c.opts.PushInterval)
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
	c, err := New(testOptions(Options{Pairing: p, Sink: &fakeSink{}, Snapshot: staticState("duo"),
		RetryBase: 20 * time.Millisecond, RetryMax: 100 * time.Millisecond, Logf: logs.logf}))
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
