package harness

import (
	"testing"

	"github.com/atfa/duo/internal/protocol"
)

// The preview reads work detail from the same AgentRuntime the harness uses, so
// this pins the detail transitions without touching the workflow state machine.
func TestTrackerNoteTracksWorkDetailForThePreview(t *testing.T) {
	tr := NewTracker()
	agent := protocol.Austin
	note := func(activity protocol.ActivityType, tool, detail string) {
		tr.Handle(agent, activity)
		tr.Note(agent, activity, tool, detail)
	}

	note(protocol.ActivityAgentStart, "", "")
	note(protocol.ActivityToolStart, "bash", "go test ./...")
	if rt := tr.Snapshot(agent); rt.Tool != "bash" || rt.ToolDetail != "go test ./..." || rt.ToolFailed || rt.ToolDepth != 1 {
		t.Fatalf("after tool_start: %+v", rt)
	}

	// A successful end keeps the arguments; only a failure replaces them.
	note(protocol.ActivityToolEnd, "bash", "")
	if rt := tr.Snapshot(agent); rt.ToolFailed || rt.ToolDetail != "go test ./..." || rt.ToolDepth != 0 {
		t.Fatalf("tool_end must keep the arguments: %+v", rt)
	}
	note(protocol.ActivityToolError, "bash", "exit status 1")
	if rt := tr.Snapshot(agent); !rt.ToolFailed || rt.ToolDetail != "exit status 1" || rt.ToolDepth != 0 {
		t.Fatalf("after tool_error: %+v", rt)
	}

	note(protocol.ActivityStream, "", "writing the patch")
	if got := tr.Snapshot(agent).StreamTail; got != "writing the patch" {
		t.Fatalf("stream tail = %q", got)
	}
	tr.NoteError(agent, "provider 429")
	if got := tr.Snapshot(agent).LastError; got != "provider 429" {
		t.Fatalf("last error = %q", got)
	}

	// A new turn clears the previous turn's detail so a stale error cannot look
	// current, while the harness failure count is reset by Handle.
	note(protocol.ActivityAgentStart, "", "")
	rt := tr.Snapshot(agent)
	if rt.Tool != "" || rt.ToolFailed || rt.StreamTail != "" || rt.LastError != "" {
		t.Fatalf("agent_start must clear the previous turn: %+v", rt)
	}
	if !rt.Busy || rt.Failures != 0 {
		t.Fatalf("agent_start must start a fresh turn: %+v", rt)
	}
}

func TestTrackerUsageTracksContextAndClearsInactiveSpeed(t *testing.T) {
	tr := NewTracker()
	agent := protocol.Austin
	tr.Handle(agent, protocol.ActivityProviderStart)
	tr.UpdateContext(agent, 45000, 200000)
	tr.UpdateRate(agent, 32.5)

	rt := tr.Snapshot(agent)
	if rt.ContextTokens != 45000 || rt.ContextWindow != 200000 {
		t.Fatalf("context usage = %d/%d, want 45000/200000", rt.ContextTokens, rt.ContextWindow)
	}
	if rt.TokensPerSecond != 32.5 || rt.TokenSpeedAt.IsZero() {
		t.Fatalf("token speed = %v at %v, want fresh 32.5", rt.TokensPerSecond, rt.TokenSpeedAt)
	}

	tr.Handle(agent, protocol.ActivityProviderEnd)
	rt = tr.Snapshot(agent)
	if rt.TokensPerSecond != 0 || !rt.TokenSpeedAt.IsZero() {
		t.Fatalf("provider_end must clear token speed: %+v", rt)
	}
	if rt.ContextTokens != 45000 || rt.ContextWindow != 200000 {
		t.Fatalf("provider_end must retain latest context usage: %d/%d", rt.ContextTokens, rt.ContextWindow)
	}
}

func TestTrackerToolErrorStillBalancesToolDepth(t *testing.T) {
	tr := NewTracker()
	tr.Handle(protocol.Tony, protocol.ActivityToolStart)
	tr.Handle(protocol.Tony, protocol.ActivityToolStart)
	tr.Handle(protocol.Tony, protocol.ActivityToolError)
	if got := tr.Snapshot(protocol.Tony).ToolDepth; got != 1 {
		t.Fatalf("tool depth = %d, want 1", got)
	}
}

// The preview shows the turn's shape from these fields: when it started, how
// many tools it has used, and how the last few ended.
func TestTrackerNoteRecordsTurnShape(t *testing.T) {
	tr := NewTracker()
	agent := protocol.Austin
	note := func(activity protocol.ActivityType, tool, detail string) {
		tr.Handle(agent, activity)
		tr.Note(agent, activity, tool, detail)
	}

	note(protocol.ActivityAgentStart, "", "")
	if rt := tr.Snapshot(agent); rt.TurnStarted.IsZero() || rt.Tools != 0 || len(rt.Recent) != 0 {
		t.Fatalf("agent_start must open a clean turn: %+v", rt)
	}

	for i, tool := range []string{"read", "edit", "bash", "write"} {
		note(protocol.ActivityToolStart, tool, "arg")
		if i == 2 {
			note(protocol.ActivityToolError, tool, "exit status 1")
		} else {
			note(protocol.ActivityToolEnd, tool, "")
		}
	}
	rt := tr.Snapshot(agent)
	if rt.Tools != 4 {
		t.Fatalf("tool count = %d, want 4", rt.Tools)
	}
	// Only the most recent calls are kept, and the failing one keeps its reason.
	if len(rt.Recent) != 3 || rt.Recent[2].Name != "write" || !rt.Recent[2].OK {
		t.Fatalf("recent trail = %+v", rt.Recent)
	}
	if rt.Recent[0].Name != "edit" || !rt.Recent[0].OK {
		t.Fatalf("oldest kept note = %+v, want edit", rt.Recent[0])
	}
	if rt.Recent[1].Name != "bash" || rt.Recent[1].OK || rt.Recent[1].Detail != "exit status 1" {
		t.Fatalf("failed note = %+v", rt.Recent[1])
	}
	if !rt.ToolStarted.IsZero() {
		t.Fatal("a finished tool must clear the start time")
	}

	note(protocol.ActivityToolStart, "bash", "go test ./...")
	if rt := tr.Snapshot(agent); rt.ToolStarted.IsZero() {
		t.Fatal("a running tool must keep its start time so the preview can show its age")
	}
	note(protocol.ActivityToolEnd, "bash", "")
	if rt := tr.Snapshot(agent); !rt.ToolStarted.IsZero() {
		t.Fatal("a finished tool must clear the start time")
	}
}
