package coordinator

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

type mockDriver struct {
	agent.Driver
	mu      sync.Mutex
	id      protocol.AgentID
	drvType string
	running bool
	written []string
}

func (m *mockDriver) Agent() protocol.AgentID   { return m.id }
func (m *mockDriver) DriverType() string        { return m.drvType }
func (m *mockDriver) Running() bool             { m.mu.Lock(); defer m.mu.Unlock(); return m.running }
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
func (m *mockDriver) Resize(cols, rows int) error     { return nil }
func (m *mockDriver) Attach(w io.Writer) []byte       { return nil }
func (m *mockDriver) Detach()                         {}
func (m *mockDriver) SetOnExit(fn func(agent.ExitEvent)) {}

func TestSubmitUserTaskRoutesToAgyDriverViaPTY(t *testing.T) {
	ctx := context.Background()
	bus := events.NewBus()
	state := project.NewStateFor(project.ModeFast)
	coord := New(nil, state, harness.NewTracker(), nil, bus)

	austin := &mockDriver{
		id:      protocol.Austin,
		drvType: "agy",
		running: true,
	}
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
