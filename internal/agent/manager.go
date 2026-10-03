package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/atfa/duo/internal/driver"
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

func (m *Manager) Add(d Driver) {
	d.SetOnExit(func(event ExitEvent) {
		m.observe(LifecycleEvent{Kind: "agent_exit", Agent: event.Agent, State: event.State, Err: event.Err, Output: event.Output})
	})
	m.mu.Lock()
	defer m.mu.Unlock()
	m.drivers[d.Agent()] = d
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

// CommandFor returns the operator's own command for an agent, if any. The model
// picker asks the plugin for the catalog, so it no longer reads the command.
func (m *Manager) CommandFor(agent protocol.AgentID) string {
	if m == nil {
		return ""
	}
	if d, ok := m.Driver(agent); ok {
		return d.Command()
	}
	return ""
}

// DriverTypeFor returns the plugin name behind an agent, for display only.
func (m *Manager) DriverTypeFor(agent protocol.AgentID) string {
	if m == nil {
		return ""
	}
	if d, ok := m.Driver(agent); ok {
		return d.DriverType()
	}
	return ""
}

// ManifestFor returns an agent's driver self-description, or nil when the agent has
// no driver yet. It is the only source Core reads for how that agent behaves.
func (m *Manager) ManifestFor(agent protocol.AgentID) *driver.Manifest {
	if m == nil {
		return nil
	}
	if d, ok := m.Driver(agent); ok {
		return d.Manifest()
	}
	return nil
}

// CapabilitiesFor returns an agent's declared capabilities. A missing manifest
// yields the zero value, which is every capability off: an agent Duo knows nothing
// about is treated as the most restricted driver, never the most capable.
func (m *Manager) CapabilitiesFor(agent protocol.AgentID) driver.Capabilities {
	if m == nil {
		return driver.Capabilities{}
	}
	if d, ok := m.Driver(agent); ok {
		return d.Capabilities()
	}
	return driver.Capabilities{}
}

// DriverStates returns every agent's opaque driver state, for the durable snapshot.
func (m *Manager) DriverStates() map[protocol.AgentID]json.RawMessage {
	out := make(map[protocol.AgentID]json.RawMessage, len(m.drivers))
	m.mu.RLock()
	defer m.mu.RUnlock()
	for agent, d := range m.drivers {
		if d == nil {
			continue
		}
		if state := d.DriverState(); len(state) > 0 {
			out[agent] = state
		}
	}
	return out
}

// CloseAll releases every driver plugin. A plugin may own background work — a
// transcript observer, for one — so it has to be told the session is over.
func (m *Manager) CloseAll() {
	m.mu.RLock()
	drivers := make([]Driver, 0, len(m.drivers))
	for _, d := range m.drivers {
		drivers = append(drivers, d)
	}
	m.mu.RUnlock()
	for _, d := range drivers {
		if d != nil {
			d.Close()
		}
	}
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
		if d != nil {
			d.Stop()
		}
	}
}
