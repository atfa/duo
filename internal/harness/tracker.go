package harness

import (
	"sync"
	"time"

	"github.com/atfa/duo/internal/protocol"
)

type AgentRuntime struct {
	Busy           bool
	ProviderActive bool
	ToolDepth      int
	LastActivity   time.Time
}

type Tracker struct {
	mu     sync.RWMutex
	agents map[protocol.AgentID]*AgentRuntime
}

func NewTracker() *Tracker {
	now := time.Now()
	return &Tracker{agents: map[protocol.AgentID]*AgentRuntime{
		protocol.Austin: {LastActivity: now},
		protocol.Tony:   {LastActivity: now},
	}}
}

func (t *Tracker) ensureLocked(agent protocol.AgentID) *AgentRuntime {
	rt := t.agents[agent]
	if rt == nil {
		rt = &AgentRuntime{LastActivity: time.Now()}
		t.agents[agent] = rt
	}
	return rt
}

func (t *Tracker) Touch(agent protocol.AgentID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ensureLocked(agent).LastActivity = time.Now()
}

func (t *Tracker) Handle(agent protocol.AgentID, activity protocol.ActivityType) {
	t.mu.Lock()
	defer t.mu.Unlock()

	rt := t.ensureLocked(agent)
	rt.LastActivity = time.Now()

	switch activity {
	case protocol.ActivityAgentStart:
		rt.Busy = true
	case protocol.ActivityAgentSettled:
		rt.Busy = false
		rt.ProviderActive = false
		rt.ToolDepth = 0
	case protocol.ActivityProviderStart:
		rt.Busy = true
		rt.ProviderActive = true
	case protocol.ActivityProviderEnd:
		rt.ProviderActive = false
	case protocol.ActivityToolStart:
		rt.Busy = true
		rt.ToolDepth++
	case protocol.ActivityToolEnd:
		if rt.ToolDepth > 0 {
			rt.ToolDepth--
		}
	case protocol.ActivityStream:
		rt.Busy = true
	}
}

func (t *Tracker) Snapshot(agent protocol.AgentID) AgentRuntime {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if rt := t.agents[agent]; rt != nil {
		return *rt
	}
	return AgentRuntime{}
}
