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
}

func NewManager() *Manager { return &Manager{sessions: make(map[protocol.AgentID]*Session)} }

func (m *Manager) Add(session *Session) {
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
			m.StopAll()
			return fmt.Errorf("start %s: %w", agent, err)
		}
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
	return s.Start(ctx)
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
