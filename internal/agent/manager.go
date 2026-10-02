package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/atfa/duo/internal/protocol"
)

type Manager struct {
	mu       sync.RWMutex
	drivers  map[protocol.AgentID]Driver
	observer Observer
}

// LifecycleEvent describes an agent process lifecycle transition. It exists so
// the durable log can record what happened to each agent without the agent
// package knowing about persistence.
type LifecycleEvent struct {
	Kind  string // agent_start, agent_start_failed, agent_restart, agent_exit
	Agent protocol.AgentID
	State ProcessState
	Err   error
	// Output carries what a failed process printed before it ended, so the
	// session log and the interface can show the driver's own reason rather than
	// only the exit status. Empty unless the process failed.
	Output string
}

type Observer func(LifecycleEvent)

func NewManager() *Manager { return &Manager{drivers: make(map[protocol.AgentID]Driver)} }

// SetObserver installs a listener for agent lifecycle events.
func (m *Manager) SetObserver(observer Observer) {
	m.mu.Lock()
	m.observer = observer
	m.mu.Unlock()
}

func (m *Manager) observe(event LifecycleEvent) {
	m.mu.RLock()
	observer := m.observer
	m.mu.RUnlock()
	if observer != nil {
		observer(event)
	}
}

func (m *Manager) Add(driver Driver) {
	driver.SetOnExit(func(event ExitEvent) {
		m.observe(LifecycleEvent{Kind: "agent_exit", Agent: event.Agent, State: event.State, Err: event.Err, Output: event.Output})
	})
	m.mu.Lock()
	defer m.mu.Unlock()
	m.drivers[driver.Agent()] = driver
}

func (m *Manager) StartAll(ctx context.Context) error {
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		d, ok := m.Driver(agent)
		if !ok {
			return fmt.Errorf("missing %s session", agent)
		}
		if err := d.Start(ctx); err != nil {
			m.observe(LifecycleEvent{Kind: "agent_start_failed", Agent: agent, State: d.State(), Err: err})
			m.StopAll()
			return fmt.Errorf("start %s: %w", agent, err)
		}
		m.observe(LifecycleEvent{Kind: "agent_start", Agent: agent, State: d.State()})
	}
	return nil
}

func (m *Manager) Driver(agent protocol.AgentID) (Driver, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.drivers[agent]
	return d, ok
}

// Session returns the driver for the specified agent, matching the previous API.
func (m *Manager) Session(agent protocol.AgentID) (Driver, bool) {
	return m.Driver(agent)
}

// Command returns the base agent command Duo launches agents with, so the model
// catalog is read from the same installation (and flags) the agents use.
func (m *Manager) Command() string {
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		if d, ok := m.Driver(agent); ok {
			return d.Command()
		}
	}
	return ""
}

// CommandFor returns the command used by the specified agent.
func (m *Manager) CommandFor(agent protocol.AgentID) string {
	if m == nil {
		return ""
	}
	if d, ok := m.Driver(agent); ok {
		return d.Command()
	}
	return ""
}

// DriverTypeFor returns the driver type name (e.g. "pi", "agy") used by the specified agent.
func (m *Manager) DriverTypeFor(agent protocol.AgentID) string {
	if m == nil {
		return "pi"
	}
	if d, ok := m.Driver(agent); ok {
		return d.DriverType()
	}
	return "pi"
}

func (m *Manager) ResizeAll(cols, rows int) error {
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		if d, ok := m.Driver(agent); ok {
			if err := d.Resize(cols, rows); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Manager) Restart(ctx context.Context, agent protocol.AgentID) error {
	d, ok := m.Driver(agent)
	if !ok {
		return fmt.Errorf("missing %s session", agent)
	}
	err := d.Restart(ctx)
	m.observe(LifecycleEvent{Kind: "agent_restart", Agent: agent, State: d.State(), Err: err})
	return err
}

func (m *Manager) StopAll() {
	m.mu.RLock()
	drivers := make([]Driver, 0, len(m.drivers))
	for _, d := range m.drivers {
		drivers = append(drivers, d)
	}
	m.mu.RUnlock()
	for _, d := range drivers {
		d.Stop()
	}
}
