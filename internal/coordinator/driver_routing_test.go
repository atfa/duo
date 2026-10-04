package coordinator

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/transport"
)

// mockDriver declares capabilities rather than a name. That is the point: a test
// that hard-coded "agy" would be testing the driver-name conditionals this
// refactor removes, and would keep passing if the behaviour were wrong.
type mockDriver struct {
	agent.Driver
	mu   sync.Mutex
	id   protocol.AgentID
	caps driver.Capabilities
	man  *driver.Manifest

	running bool
	written []string
}

// newMockDriver builds a driver that declares the given capabilities. A name is only
// kept for log readability.
func newMockDriver(id protocol.AgentID, name string, caps driver.Capabilities) *mockDriver {
	man := &driver.Manifest{
		Protocol:       driver.ProtocolVersion,
		Name:           name,
		ModelReference: driver.ModelQualified,
		Capabilities:   caps,
	}
	return &mockDriver{id: id, caps: caps, man: man}
}

// steeringOnly is a driver whose bridge lives inside the agent, so prompts can be
// delivered over it.
func steeringOnly() driver.Capabilities {
	return driver.Capabilities{
		Resume: driver.ResumeClient, Bridge: driver.BridgeAgent,
		LiveSteering: true, PTYFallback: true, SelfReports: true,
	}
}

// observedFromOutside is a driver whose bridge endpoint is the plugin itself: it
// watches the agent and reports upstream, but nothing can write to the terminal
// Duo Core owns.
func observedFromOutside() driver.Capabilities {
	return driver.Capabilities{
		Resume: driver.ResumeServer, Bridge: driver.BridgePlugin,
		PTYFallback: true, SelfReports: false,
	}
}

// liveSwitching is a driver whose bridge lives inside the agent and can carry
// every request Duo may send: prompts, model changes, thinking cycles.
func liveSwitching() driver.Capabilities {
	return driver.Capabilities{
		Resume: driver.ResumeClient, Bridge: driver.BridgeAgent,
		LiveSteering: true, LiveModelSwitch: true, LiveThinkingSwitch: true,
		PTYFallback: true, SelfReports: true,
	}
}

func (m *mockDriver) Agent() protocol.AgentID { return m.id }
func (m *mockDriver) DriverType() string {
	if m.man != nil {
		return m.man.Name
	}
	return ""
}
func (m *mockDriver) Manifest() *driver.Manifest        { return m.man }
func (m *mockDriver) Capabilities() driver.Capabilities { return m.caps }
func (m *mockDriver) Running() bool                     { m.mu.Lock(); defer m.mu.Unlock(); return m.running }
func (m *mockDriver) State() agent.ProcessState {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return agent.ProcessRunning
	}
	return agent.ProcessFailed
}
func (m *mockDriver) Write(p []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.written = append(m.written, string(p))
	return nil
}
func (m *mockDriver) Resize(cols, rows int) error        { return nil }
func (m *mockDriver) Attach(w io.Writer) []byte          { return nil }
func (m *mockDriver) Detach()                            {}
func (m *mockDriver) SetOnExit(fn func(agent.ExitEvent)) {}

func TestSubmitUserTaskRoutesToAgyDriverViaPTY(t *testing.T) {
	ctx := context.Background()
	bus := events.NewBus()
	state := project.NewStateFor(project.ModeFast)
	coord := New(nil, state, harness.NewTracker(), nil, bus)

	// A driver whose bridge endpoint belongs to the plugin: it reports what the
	// agent is doing, but it cannot receive an injected prompt, so the task must go
	// to the terminal.
	austin := newMockDriver(protocol.Austin, "observed", observedFromOutside())
	austin.running = true
	mgr := agent.NewManager()
	mgr.Add(austin)
	coord.SetAgents(mgr)

	// Austin should be recognized as connected via agent manager
	if !coord.IsAgentConnected(protocol.Austin) {
		t.Fatal("expected Austin to be connected via agent manager")
	}
	// Tony is not added/running, should not be connected
	if coord.IsAgentConnected(protocol.Tony) {
		t.Fatal("expected Tony to not be connected")
	}

	task := "implement voxel terrain generation"
	if err := coord.SubmitUserTask(ctx, task); err != nil {
		t.Fatalf("SubmitUserTask failed: %v", err)
	}

	austin.mu.Lock()
	defer austin.mu.Unlock()
	if len(austin.written) == 0 {
		t.Fatal("expected task to be written to Austin PTY, got nothing")
	}
	fullWritten := strings.Join(austin.written, "")
	if !strings.Contains(fullWritten, task) {
		t.Fatalf("written = %q, want it to contain %q", fullWritten, task)
	}
	if !strings.Contains(fullWritten, "[Human task from Duo]") {
		t.Fatalf("written = %q, want it to contain '[Human task from Duo]'", fullWritten)
	}
}

// endpoint is the client side of one bridge connection. A delivery message is
// written server-to-client, so what the bridge received can only be observed by
// reading the client socket: the server's OnMessage fires for the opposite
// direction and would never see a prompt.
type endpoint struct {
	conn net.Conn
	msgs chan protocol.Message
}

// startBridge brings up a real transport server, because IsConnected against a
// nil server is false for every driver and cannot exercise a gate at all.
func startBridge(t *testing.T) *transport.Server {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := transport.NewServer("127.0.0.1:0", "session-1", "secret")
	go func() { _ = s.ListenAndServe(ctx) }()
	<-s.Ready()
	t.Cleanup(cancel)
	return s
}

// attachAgent opens the endpoint an Agent Adapter would open: a hello with no
// clientType, which the server registers as the agent's endpoint. Everything
// the coordinator writes toward the agent lands in the returned channel.
func attachAgent(t *testing.T, s *transport.Server, agent protocol.AgentID) *endpoint {
	t.Helper()
	conn, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	hello := protocol.Message{
		Version: protocol.Version, Type: protocol.MsgHello, Agent: agent,
		SessionID: "session-1", Token: "secret", Timestamp: time.Now().UnixMilli(),
	}
	if err := json.NewEncoder(conn).Encode(hello); err != nil {
		t.Fatalf("hello: %v", err)
	}
	e := &endpoint{conn: conn, msgs: make(chan protocol.Message, 64)}
	go func() {
		sc := bufio.NewScanner(conn)
		for sc.Scan() {
			var m protocol.Message
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				continue
			}
			select {
			case e.msgs <- m:
			default:
			}
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.IsConnected(agent) {
			return e
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("endpoint for %s did not register", agent)
	return e
}

// collect waits until one message of each wanted type has arrived, in any
// order, and returns everything of those types seen so far.
func (e *endpoint) collect(t *testing.T, want ...protocol.MessageType) []protocol.Message {
	t.Helper()
	var got []protocol.Message
	deadline := time.Now().Add(2 * time.Second)
	remaining := len(want)
	for remaining > 0 && time.Now().Before(deadline) {
		select {
		case m := <-e.msgs:
			for _, wantType := range want {
				if m.Type == wantType {
					got = append(got, m)
					remaining = countMissing(want, got)
					break
				}
			}
		case <-time.After(time.Until(deadline)):
		}
	}
	if remaining > 0 {
		t.Fatalf("timed out waiting for %d of %d delivery types; got: %+v", remaining, len(want), got)
	}
	return got
}

// drain returns everything that has arrived so far, waiting only long enough
// for localhost delivery of anything already sent.
func (e *endpoint) drain() []protocol.Message {
	time.Sleep(150 * time.Millisecond)
	var got []protocol.Message
	for {
		select {
		case m := <-e.msgs:
			got = append(got, m)
		default:
			return got
		}
	}
}

func countMissing(want []protocol.MessageType, got []protocol.Message) int {
	missing := 0
	for _, w := range want {
		found := false
		for _, g := range got {
			if g.Type == w {
				found = true
				break
			}
		}
		if !found {
			missing++
		}
	}
	return missing
}

// The regression M4b exists to prevent: the moment an observing plugin owns the
// endpoint, IsConnected is true for a driver that cannot receive anything, and
// all three delivery sites used to branch on exactly that. Every prompt would be
// written into the plugin's read loop, every set_model would vanish, and agy
// would be broken with no error anywhere.
func TestConnectedObservingEndpointKeepsDeliveryOffTheBridge(t *testing.T) {
	ctx := context.Background()
	bus := events.NewBus()
	ch, unsub := bus.Subscribe(16)
	defer unsub()

	s := startBridge(t)
	coord := New(s, project.NewStateFor(project.ModeFast), harness.NewTracker(), nil, bus)

	austin := newMockDriver(protocol.Austin, "observed", observedFromOutside())
	austin.running = true
	mgr := agent.NewManager()
	mgr.Add(austin)
	coord.SetAgents(mgr)
	ep := attachAgent(t, s, protocol.Austin)

	task := "implement voxel terrain generation"
	if err := coord.SubmitUserTask(ctx, task); err != nil {
		t.Fatalf("SubmitUserTask failed: %v", err)
	}
	if err := coord.SetModel(ctx, protocol.Austin, "prov", "model-id"); err != nil {
		t.Fatalf("SetModel failed: %v", err)
	}
	if err := coord.CycleThinking(ctx, protocol.Austin); err != nil {
		t.Fatalf("CycleThinking failed: %v", err)
	}

	// The local model path confirms by event that SetModel took it rather than
	// the bridge; the endpoint must be silent on all three request types.
	waitForEvent(t, ch, events.KindModel)
	if got := ep.drain(); len(got) != 0 {
		t.Errorf("observing endpoint received %d messages, want 0: %+v", len(got), got)
	}
	austin.mu.Lock()
	written := strings.Join(austin.written, "")
	austin.mu.Unlock()
	if !strings.Contains(written, task) {
		t.Errorf("PTY written = %q, want the task", written)
	}
}

// The other half, so the gate does not over-correct: a connected endpoint whose
// driver declared the capability still gets the request, model change included.
func TestConnectedSteeringEndpointStillReceivesDelivery(t *testing.T) {
	ctx := context.Background()
	bus := events.NewBus()
	s := startBridge(t)
	coord := New(s, project.NewStateFor(project.ModeFast), harness.NewTracker(), nil, bus)

	austin := newMockDriver(protocol.Austin, "steering", liveSwitching())
	austin.running = true
	mgr := agent.NewManager()
	mgr.Add(austin)
	coord.SetAgents(mgr)
	ep := attachAgent(t, s, protocol.Austin)

	task := "ship it"
	if err := coord.SubmitUserTask(ctx, task); err != nil {
		t.Fatalf("SubmitUserTask failed: %v", err)
	}
	if err := coord.SetModel(ctx, protocol.Austin, "prov", "model-id"); err != nil {
		t.Fatalf("SetModel failed: %v", err)
	}
	if err := coord.CycleThinking(ctx, protocol.Austin); err != nil {
		t.Fatalf("CycleThinking failed: %v", err)
	}

	got := ep.collect(t, protocol.MsgHumanPrompt, protocol.MsgSetModel, protocol.MsgCycleThinking)
	if len(got) < 3 {
		t.Fatalf("bridge received %+v, want one of each request", got)
	}
	austin.mu.Lock()
	written := strings.Join(austin.written, "")
	austin.mu.Unlock()
	if written != "" {
		t.Errorf("PTY written = %q, want nothing: the bridge took the delivery", written)
	}
}

func waitForEvent(t *testing.T, ch <-chan events.Event, kind events.Kind) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case e := <-ch:
			if e.Kind == kind {
				return
			}
		case <-time.After(time.Until(deadline)):
		}
	}
	t.Fatalf("timed out waiting for a %s event", kind)
}

// A driver whose bridge can only attach once its agent has a session is announced
// when the process starts instead. When its bridge does attach later, the
// connection must not be announced a second time.
func TestLateBridgeIsNotAnnouncedTwice(t *testing.T) {
	bus := events.NewBus()
	ch, unsub := bus.Subscribe(16)
	defer unsub()

	coord := New(nil, project.NewStateFor(project.ModeFast), harness.NewTracker(), nil, bus)
	mgr := agent.NewManager()
	// A driver that cannot attach its bridge until its agent has something to say
	// must be announced when the process starts; one that attaches immediately must
	// announce itself, or the human is told about it twice.
	late := newMockDriver(protocol.Austin, "late", driver.Capabilities{
		Resume: driver.ResumeServer, Bridge: driver.BridgeAgent,
		LiveSteering: true, PTYFallback: true, SelfReports: false,
	})
	late.running = true
	eager := newMockDriver(protocol.Tony, "eager", steeringOnly())
	eager.running = true
	mgr.Add(late)
	mgr.Add(eager)
	coord.SetAgents(mgr)

	if late.Capabilities().SelfReports {
		t.Fatal("a driver that cannot attach on launch must not claim it does")
	}
	if !eager.Capabilities().SelfReports {
		t.Fatal("a driver whose bridge attaches on launch must claim it does")
	}
	coord.OnConnect(context.Background(), &transport.Client{Agent: protocol.Tony})

	var texts []string
	for {
		select {
		case e := <-ch:
			if e.Text != "" {
				texts = append(texts, e.Text)
			}
			continue
		default:
		}
		break
	}
	for _, text := range texts {
		if strings.Contains(text, "Austin connected") {
			t.Fatalf("unexpected announcement for Austin: %q", text)
		}
	}
	if len(texts) != 1 || !strings.Contains(texts[0], "Tony connected") {
		t.Fatalf("announcements = %v, want only Tony's", texts)
	}
}
