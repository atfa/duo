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

func TestTrackerToolErrorStillBalancesToolDepth(t *testing.T) {
	tr := NewTracker()
	tr.Handle(protocol.Tony, protocol.ActivityToolStart)
	tr.Handle(protocol.Tony, protocol.ActivityToolStart)
	tr.Handle(protocol.Tony, protocol.ActivityToolError)
	if got := tr.Snapshot(protocol.Tony).ToolDepth; got != 1 {
		t.Fatalf("tool depth = %d, want 1", got)
	}
}
