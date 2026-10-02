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

	// Context usage and approximate output rate for the native preview. Drivers
	// without a bridge may leave these unset.
	ContextTokens   int
	ContextWindow   int
	TokensPerSecond float64
	TokenSpeedAt    time.Time

	// TurnStarted, ToolStarted, Tools and Recent describe the current turn's
	// shape: when it began, how long the running tool has been going, how many
	// tools it has used, and how the last few ended.
	TurnStarted time.Time
	ToolStarted time.Time
	Tools       int
	Recent      []ToolNote

	// Failures counts agent-error events since the agent last started a turn.
	// The Fast harness uses it to ask the copilot for a diagnosis; Goal never
	// reads it, so recording it cannot change Goal behavior.
	Failures int
}

// ToolNote is one completed tool call of the current turn. The preview shows the
// last few as a progress trail: the ✓/✗ rhythm is what separates a long turn
// that is still moving from one stuck on the same failure.
type ToolNote struct {
	Name     string
	Detail   string
	OK       bool
	Duration time.Duration
}

// recentTools is how many completed tool calls the preview keeps per turn.
const recentTools = 3

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
		rt.TokensPerSecond = 0
		rt.TokenSpeedAt = time.Time{}
	case protocol.ActivityProviderStart:
		rt.Busy = true
		rt.ProviderActive = true
		rt.TokensPerSecond = 0
		rt.TokenSpeedAt = time.Time{}
	case protocol.ActivityProviderEnd:
		rt.ProviderActive = false
		rt.TokensPerSecond = 0
		rt.TokenSpeedAt = time.Time{}
	case protocol.ActivityToolStart:
		rt.Busy = true
		rt.ProviderActive = false
		rt.TokensPerSecond = 0
		rt.TokenSpeedAt = time.Time{}
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
		rt.TurnStarted, rt.ToolStarted = time.Now(), time.Time{}
		rt.Tools, rt.Recent = 0, nil
	case protocol.ActivityProviderStart, protocol.ActivityAgentSettled:
		rt.StreamTail = ""
	case protocol.ActivityToolStart:
		rt.Tool, rt.ToolDetail, rt.ToolFailed = tool, detail, false
		rt.StreamTail = ""
		rt.ToolStarted = time.Now()
		rt.Tools++
	case protocol.ActivityToolEnd:
		// tool_end keeps the arguments captured at tool_start; the result summary
		// is only worth showing when it is a failure.
		if tool != "" {
			rt.Tool = tool
		}
		rt.ToolFailed = false
		rt.recordToolLocked(tool, "", true)
	case protocol.ActivityToolError:
		if tool != "" {
			rt.Tool = tool
		}
		if detail != "" {
			rt.ToolDetail = detail
		}
		rt.ToolFailed = true
		rt.recordToolLocked(tool, detail, false)
	case protocol.ActivityStream:
		if detail != "" {
			rt.StreamTail = detail
		}
	}
}

// recordToolLocked moves a finished tool into the recent trail. The name comes
// from the event, or from the tool it is closing when the event omits it, so a
// bare tool_end still records the call.
func (rt *AgentRuntime) recordToolLocked(tool, detail string, ok bool) {
	if tool == "" {
		tool = rt.Tool
	}
	if tool == "" {
		return
	}
	duration := time.Duration(0)
	if !rt.ToolStarted.IsZero() {
		duration = time.Since(rt.ToolStarted)
	}
	rt.ToolStarted = time.Time{}
	rt.Recent = append(rt.Recent, ToolNote{Name: tool, Detail: detail, OK: ok, Duration: duration})
	if len(rt.Recent) > recentTools {
		rt.Recent = rt.Recent[len(rt.Recent)-recentTools:]
	}
}

// UpdateUsage records optional token metrics reported by a driver's bridge.
func (t *Tracker) UpdateUsage(agent protocol.AgentID, tokens, window int, tokensPerSecond float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	rt := t.ensureLocked(agent)
	if window > 0 && tokens >= 0 {
		rt.ContextTokens, rt.ContextWindow = tokens, window
	}
	if tokensPerSecond > 0 {
		rt.TokensPerSecond = tokensPerSecond
		rt.TokenSpeedAt = time.Now()
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
