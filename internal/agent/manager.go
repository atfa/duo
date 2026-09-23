package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/atfa/duo/internal/protocol"
)

type Manager struct {
	mu       sync.RWMutex
	sessions map[protocol.AgentID]*Session
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
}

type Observer func(LifecycleEvent)

func NewManager() *Manager { return &Manager{sessions: make(map[protocol.AgentID]*Session)} }

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

func (m *Manager) Add(session *Session) {
	session.cfg.OnExit = func(event ExitEvent) {
		m.observe(LifecycleEvent{Kind: "agent_exit", Agent: event.Agent, State: event.State, Err: event.Err})
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[session.cfg.Agent] = session
}

func (m *Manager) StartAll(ctx context.Context) error {
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		s, ok := m.Session(agent)
		if !ok {
			return fmt.Errorf("missing %s session", agent)
		}
		if err := s.Start(ctx); err != nil {
			m.observe(LifecycleEvent{Kind: "agent_start_failed", Agent: agent, State: s.State(), Err: err})
			m.StopAll()
			return fmt.Errorf("start %s: %w", agent, err)
		}
		m.observe(LifecycleEvent{Kind: "agent_start", Agent: agent, State: s.State()})
	}
	return nil
}

func (m *Manager) Session(agent protocol.AgentID) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[agent]
	return s, ok
}

func (m *Manager) ResizeAll(cols, rows int) error {
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		if s, ok := m.Session(agent); ok {
			if err := s.Resize(cols, rows); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Manager) Restart(ctx context.Context, agent protocol.AgentID) error {
	s, ok := m.Session(agent)
	if !ok {
		return fmt.Errorf("missing %s session", agent)
	}
	if s.Running() {
		return fmt.Errorf("%s is still running; restart refused", agent)
	}
	err := s.Start(ctx)
	m.observe(LifecycleEvent{Kind: "agent_restart", Agent: agent, State: s.State(), Err: err})
	return err
}

func (m *Manager) StopAll() {
	m.mu.RLock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.RUnlock()
	for _, s := range sessions {
		s.Stop()
	}
}
