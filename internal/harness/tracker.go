package harness

import (
	"strings"
	"sync"
	"time"

	"github.com/atfa/duo/internal/protocol"
)

type AgentRuntime struct {
	Busy           bool
	ProviderActive bool
	ToolDepth      int
	HumanAttached  bool
	LastActivity   time.Time

	// Work detail for the native preview. Tool/ToolDetail describe the tool being
	// run (or the last one), ToolFailed marks it failed, StreamTail is the latest
	// streamed assistant text, and LastError keeps the current turn's failure
	// visible. None of this feeds the harness workflow.
	Tool       string
	ToolDetail string
	ToolFailed bool
	StreamTail string
	LastError  string

	// Failures counts agent-error events since the agent last started a turn.
	// The Fast harness uses it to ask the copilot for a diagnosis; Goal never
	// reads it, so recording it cannot change Goal behavior.
	Failures int
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
		rt.Failures = 0
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
	case protocol.ActivityToolEnd, protocol.ActivityToolError:
		if rt.ToolDepth > 0 {
			rt.ToolDepth--
		}
	case protocol.ActivityStream:
		rt.Busy = true
	}
}

// Note records the work detail that accompanies an activity transition. It is
// deliberately separate from Handle so the state machine above stays readable,
// and it never mutates Busy/ProviderActive/ToolDepth.
func (t *Tracker) Note(agent protocol.AgentID, activity protocol.ActivityType, tool, detail string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	rt := t.ensureLocked(agent)
	switch activity {
	case protocol.ActivityAgentStart:
		rt.Tool, rt.ToolDetail, rt.ToolFailed, rt.StreamTail, rt.LastError = "", "", false, "", ""
	case protocol.ActivityProviderStart, protocol.ActivityAgentSettled:
		rt.StreamTail = ""
	case protocol.ActivityToolStart:
		rt.Tool, rt.ToolDetail, rt.ToolFailed = tool, detail, false
		rt.StreamTail = ""
	case protocol.ActivityToolEnd:
		// tool_end keeps the arguments captured at tool_start; the result summary
		// is only worth showing when it is a failure.
		if tool != "" {
			rt.Tool = tool
		}
		rt.ToolFailed = false
	case protocol.ActivityToolError:
		if tool != "" {
			rt.Tool = tool
		}
		if detail != "" {
			rt.ToolDetail = detail
		}
		rt.ToolFailed = true
	case protocol.ActivityStream:
		if detail != "" {
			rt.StreamTail = detail
		}
	}
}

// NoteError keeps an agent's most recent failure visible in the preview until
// its next turn starts.
func (t *Tracker) NoteError(agent protocol.AgentID, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ensureLocked(agent).LastError = text
}

func (t *Tracker) SetHumanAttached(agent protocol.AgentID, attached bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	rt := t.ensureLocked(agent)
	rt.HumanAttached = attached
	rt.LastActivity = time.Now()
}

// RecordFailure notes a reported agent error so the harness can decide whether
// an independent diagnosis is warranted. A new agent turn clears the count.
func (t *Tracker) RecordFailure(agent protocol.AgentID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ensureLocked(agent).Failures++
}

func (t *Tracker) Reset(agent protocol.AgentID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.agents[agent] = &AgentRuntime{LastActivity: time.Now()}
}

func (t *Tracker) Snapshot(agent protocol.AgentID) AgentRuntime {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if rt := t.agents[agent]; rt != nil {
		return *rt
	}
	return AgentRuntime{}
}
