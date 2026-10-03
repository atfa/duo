package coordinator

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"

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
		Activity: true, PTYFallback: true, SelfReports: false,
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
