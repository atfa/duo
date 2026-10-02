package agent

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/protocol"
)

func TestExtractConversationID(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{
			input:    "I0930 08:40:41.075572       1 server.go:1248] Created conversation e5e9d0a4-89ef-4605-90ee-e5fbcfef8d3c\n",
			expected: "e5e9d0a4-89ef-4605-90ee-e5fbcfef8d3c",
		},
		{
			input:    "I0930 08:49:15.680585       1 server.go:3175] GetConversationDetail: found conversation 061bdff2-a98c-4a7a-8353-409ca6fa375b (active=false)\n",
			expected: "061bdff2-a98c-4a7a-8353-409ca6fa375b",
		},
		{
			input:    "I0930 08:49:15.681141       1 printmode.go:181] Print mode: starting (promptLength=15, model=\"\", conversationID=\"77a99eec-2991-4e73-9d78-5fd6614b5afe\")\n",
			expected: "77a99eec-2991-4e73-9d78-5fd6614b5afe",
		},
		{
			input:    "no conversation here\njust some other log\n",
			expected: "",
		},
	}

	for i, tc := range cases {
		id, err := ExtractConversationID(strings.NewReader(tc.input))
		if err != nil && tc.expected != "" {
			t.Errorf("case %d unexpected error: %v", i, err)
		}
		if id != tc.expected {
			t.Errorf("case %d got %q, want %q", i, id, tc.expected)
		}
	}
}

func TestSummarizeToolArgs(t *testing.T) {
	// 1. duo_send
	s := SummarizeToolArgs("duo_send", map[string]any{"message": "proposal for auth"})
	if s != "proposal for auth" {
		t.Errorf("duo_send got %q", s)
	}

	// 2. duo_set_status
	s = SummarizeToolArgs("duo_set_status", map[string]any{"ready": true})
	if s != "ready=true" {
		t.Errorf("duo_set_status got %q", s)
	}

	// 3. duo_set_verification. The bridge extensions send "result"; "verdict" is
	// the MCP spelling for a different transport.
	s = SummarizeToolArgs("duo_set_verification", map[string]any{"result": "passed"})
	if s != "passed" {
		t.Errorf("duo_set_verification got %q", s)
	}

	// 4. run_command
	s = SummarizeToolArgs("run_command", map[string]any{
		"toolSummary": "\"Git status check\"",
		"CommandLine": "\"git status\"",
	})
	if s != "Git status check" {
		t.Errorf("run_command got %q", s)
	}

	// 5. view_file
	s = SummarizeToolArgs("view_file", map[string]any{
		"AbsolutePath": "/Users/test/projects/duo/cmd/duo/main.go",
	})
	if s != "main.go" {
		t.Errorf("view_file got %q", s)
	}
}

func TestAgyWatcherProcessLine(t *testing.T) {
	var events []protocol.Message
	sink := FuncActivitySink(func(agent protocol.AgentID, msg protocol.Message) {
		events = append(events, msg)
	})

	w := NewAgyWatcher(protocol.Austin, "/dummy/path", sink)

	// Step 0: USER_INPUT
	line0 := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-09-30T00:00:00Z","content":"fix bug"}`
	w.ProcessLine([]byte(line0))
	if len(events) != 1 || events[0].Activity != protocol.ActivityAgentStart {
		t.Fatalf("expected ActivityAgentStart, got: %#v", events)
	}

	// Step 1: PLANNER_RESPONSE with thinking and tool call
	events = nil
	line1 := `{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-09-30T00:00:01Z","thinking":"I should check git status","tool_calls":[{"name":"run_command","args":{"toolSummary":"\"Check git status\"","CommandLine":"\"git status\""}}]}`
	w.ProcessLine([]byte(line1))
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Activity != protocol.ActivityStream || !strings.Contains(events[0].Detail, "I should check git status") {
		t.Errorf("expected ActivityStream thinking, got: %#v", events[0])
	}
	if events[1].Activity != protocol.ActivityToolStart || events[1].Tool != "run_command" || events[1].Detail != "Check git status" {
		t.Errorf("expected ActivityToolStart run_command, got: %#v", events[1])
	}

	// Step 2: GENERIC with DONE
	events = nil
	line2 := `{"step_index":2,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-09-30T00:00:02Z","content":"Output:\nclean"}`
	w.ProcessLine([]byte(line2))
	if len(events) != 1 || events[0].Activity != protocol.ActivityToolEnd || events[0].Tool != "run_command" {
		t.Fatalf("expected ActivityToolEnd for run_command, got: %#v", events)
	}

	// Step 3: PLANNER_RESPONSE with answer (no tools) -> Assistant message + Stream + Settled
	events = nil
	line3 := `{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-09-30T00:00:03Z","content":"Everything is done."}`
	w.ProcessLine([]byte(line3))
	if len(events) != 3 {
		t.Fatalf("expected 3 events (assistant + stream + settled), got %d", len(events))
	}
	if events[0].Type != protocol.MsgAssistantMessage || events[0].Text != "Everything is done." {
		t.Errorf("expected MsgAssistantMessage, got: %#v", events[0])
	}
	if events[1].Activity != protocol.ActivityStream || events[1].Detail != "Everything is done." {
		t.Errorf("expected ActivityStream, got: %#v", events[1])
	}
	if events[2].Activity != protocol.ActivityAgentSettled {
		t.Errorf("expected ActivityAgentSettled, got: %#v", events[2])
	}
}

func TestAgyWatcherTailLoop(t *testing.T) {
	tmpDir := t.TempDir()
	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")

	msgCh := make(chan protocol.Message, 10)
	sink := FuncActivitySink(func(_ protocol.AgentID, msg protocol.Message) {
		msgCh <- msg
	})

	w := NewAgyWatcher(protocol.Austin, transcriptPath, sink)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w.Start(ctx)
	defer w.Stop()

	// Append first line after a slight delay
	time.Sleep(100 * time.Millisecond)
	f, err := os.OpenFile(transcriptPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = f.WriteString(`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-09-30T00:00:00Z","content":"start"}` + "\n")
	_ = f.Sync()

	select {
	case msg := <-msgCh:
		if msg.Activity != protocol.ActivityAgentStart {
			t.Fatalf("expected ActivityAgentStart, got %v", msg.Activity)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ActivityAgentStart")
	}

	// Append tool call
	_, _ = f.WriteString(`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-09-30T00:00:01Z","tool_calls":[{"name":"duo_set_status","args":{"ready":true}}]}` + "\n")
	_ = f.Sync()

	select {
	case msg := <-msgCh:
		if msg.Activity != protocol.ActivityToolStart || msg.Tool != "duo_set_status" || msg.Detail != "ready=true" {
			t.Fatalf("unexpected message: %#v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ActivityToolStart")
	}

	_ = f.Close()
}

// A tool outside the switch falls back to "first string arg wins". Go
// randomizes map iteration, so the old unsorted walk could pick a different
// argument for the very same call on each render, making the activity line
// flicker. The summary must be stable and ordered.
func TestSummarizeToolArgsFallbackIsDeterministic(t *testing.T) {
	args := map[string]any{
		"result": "passed",
		"note":   "checked the cache path",
		"head":   "abc1234",
	}
	// A tool with no case in the switch, so the fallback runs.
	const tool = "some_unhandled_tool"
	first := SummarizeToolArgs(tool, args)
	if !strings.HasPrefix(first, "head=") {
		t.Fatalf("fallback picked %q, want the first key in sorted order (head=)", first)
	}
	for i := 0; i < 200; i++ {
		if got := SummarizeToolArgs(tool, args); got != first {
			t.Fatalf("summary changed between renders: %q then %q", first, got)
		}
	}
}

func TestParseAgyGenMetadata(t *testing.T) {
	// Build nested protobuf manually:
	// f2: field 2 (varint 5000), field 3 (varint 250), field 5 (varint 45000)
	var f2 []byte
	f2 = binary.AppendUvarint(f2, (2<<3)|0)
	f2 = binary.AppendUvarint(f2, 5000)
	f2 = binary.AppendUvarint(f2, (3<<3)|0)
	f2 = binary.AppendUvarint(f2, 250)
	f2 = binary.AppendUvarint(f2, (5<<3)|0)
	f2 = binary.AppendUvarint(f2, 45000)

	// f17: field 2 (length-delimited f2)
	var f17 []byte
	f17 = binary.AppendUvarint(f17, (2<<3)|2)
	f17 = binary.AppendUvarint(f17, uint64(len(f2)))
	f17 = append(f17, f2...)

	// f1: field 17 (length-delimited f17), field 19 (length-delimited "gemini-3.8-flash")
	modelName := "gemini-3.8-flash"
	var f1 []byte
	f1 = binary.AppendUvarint(f1, (17<<3)|2)
	f1 = binary.AppendUvarint(f1, uint64(len(f17)))
	f1 = append(f1, f17...)
	f1 = binary.AppendUvarint(f1, (19<<3)|2)
	f1 = binary.AppendUvarint(f1, uint64(len(modelName)))
	f1 = append(f1, []byte(modelName)...)

	// top: field 1 (length-delimited f1)
	var top []byte
	top = binary.AppendUvarint(top, (1<<3)|2)
	top = binary.AppendUvarint(top, uint64(len(f1)))
	top = append(top, f1...)

	usage, ok := ParseAgyGenMetadata(top)
	if !ok {
		t.Fatal("ParseAgyGenMetadata returned ok=false")
	}
	if usage.PromptTokens != 5000 {
		t.Errorf("PromptTokens = %d, want 5000", usage.PromptTokens)
	}
	if usage.CandidatesTokens != 250 {
		t.Errorf("CandidatesTokens = %d, want 250", usage.CandidatesTokens)
	}
	if usage.CachedTokens != 45000 {
		t.Errorf("CachedTokens = %d, want 45000", usage.CachedTokens)
	}
	if usage.TotalInputTokens != 50000 {
		t.Errorf("TotalInputTokens = %d, want 50000", usage.TotalInputTokens)
	}
	if usage.Model != modelName {
		t.Errorf("Model = %q, want %q", usage.Model, modelName)
	}
	if usage.ContextWindow != 1_048_576 {
		t.Errorf("ContextWindow = %d, want 1048576", usage.ContextWindow)
	}
}

func TestAgyWatcherTokenUsageAndSpeed(t *testing.T) {
	var events []protocol.Message
	sink := FuncActivitySink(func(_ protocol.AgentID, msg protocol.Message) {
		events = append(events, msg)
	})

	w := NewAgyWatcher(protocol.Austin, "/tmp/test/brain/conv123/.system_generated/logs/transcript.jsonl", sink)
	w.SetUsage(45000, 1000000)

	// Step 0: USER_INPUT triggers agent_start
	w.ProcessLine([]byte(`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-09-30T00:00:00Z","content":"hello"}`))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Activity != protocol.ActivityAgentStart {
		t.Errorf("expected ActivityAgentStart, got %v", events[0].Activity)
	}
	if events[0].ContextTokens != 45000 || events[0].ContextWindow != 1000000 {
		t.Errorf("expected context usage 45000/1000000, got %d/%d", events[0].ContextTokens, events[0].ContextWindow)
	}

	// Step 1: PLANNER_RESPONSE with thinking triggers stream then tool_start
	events = nil
	// simulate a short delay to verify speed calculation
	time.Sleep(250 * time.Millisecond)
	w.ProcessLine([]byte(`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-09-30T00:00:01Z","thinking":"analyzing code carefully now...","tool_calls":[{"name":"run_command","args":{"toolSummary":"ls"}}]}`))
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d: %+v", len(events), events)
	}
	streamMsg := events[0]
	if streamMsg.Activity != protocol.ActivityStream {
		t.Fatalf("expected ActivityStream, got %v", streamMsg.Activity)
	}
	if streamMsg.ContextTokens != 45000 || streamMsg.ContextWindow != 1000000 {
		t.Errorf("expected stream context tokens 45000/1000000, got %d/%d", streamMsg.ContextTokens, streamMsg.ContextWindow)
	}
	if streamMsg.TokensPerSecond <= 0 {
		t.Errorf("expected positive TokensPerSecond, got %v", streamMsg.TokensPerSecond)
	}

	toolMsg := events[1]
	if toolMsg.Activity != protocol.ActivityToolStart {
		t.Fatalf("expected ActivityToolStart, got %v", toolMsg.Activity)
	}
	if toolMsg.ContextTokens != 45000 || toolMsg.ContextWindow != 1000000 {
		t.Errorf("expected tool context tokens 45000/1000000, got %d/%d", toolMsg.ContextTokens, toolMsg.ContextWindow)
	}
}

func TestResolveAgyContextWindow(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		{"gemini-2.5-pro", 2_097_152},
		{"gemini-3.8-flash", 1_048_576},
		{"claude-3-7-sonnet", 200_000},
		{"gpt-4o", 128_000},
		{"deepseek-r1", 128_000},
		{"unknown-model", 0},
		{"", 0},
	}
	for _, tc := range cases {
		got := resolveAgyContextWindow(tc.model)
		if got != tc.want {
			t.Errorf("resolveAgyContextWindow(%q) = %d, want %d", tc.model, got, tc.want)
		}
	}
}

func TestAgyWatcherDefaultContextWindowZeroWhenNoDB(t *testing.T) {
	w := NewAgyWatcher(protocol.Austin, "/nonexistent/transcript.jsonl", nil)
	if w.lastContextWindow != 0 {
		t.Errorf("expected lastContextWindow == 0 when DB is unreadable, got %d", w.lastContextWindow)
	}
}

func TestAgyWatcherSpeedResetAcrossTurns(t *testing.T) {
	var events []protocol.Message
	sink := FuncActivitySink(func(_ protocol.AgentID, msg protocol.Message) {
		events = append(events, msg)
	})

	w := NewAgyWatcher(protocol.Austin, "/nonexistent/transcript.jsonl", sink)
	w.SetUsage(1000, 100000)

	// Turn 1: USER_INPUT -> wait 250ms -> PLANNER_RESPONSE (speed > 0) -> settles
	w.ProcessLine([]byte(`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-09-30T00:00:00Z","content":"hello"}`))
	time.Sleep(250 * time.Millisecond)
	events = nil
	w.ProcessLine([]byte(`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-09-30T00:00:01Z","thinking":"answering...","content":"hi there"}`))

	// Expect stream (speed > 0) + assistant message + stream + settled
	var turn1Speed float64
	for _, ev := range events {
		if ev.Activity == protocol.ActivityStream && ev.TokensPerSecond > 0 {
			turn1Speed = ev.TokensPerSecond
		}
	}
	if turn1Speed <= 0 {
		t.Fatalf("expected turn 1 to compute speed > 0, got %v", turn1Speed)
	}

	// Turn 2: New USER_INPUT immediately followed by PLANNER_RESPONSE (< 200ms)
	events = nil
	w.ProcessLine([]byte(`{"step_index":2,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-09-30T00:00:02Z","content":"next"}`))
	w.ProcessLine([]byte(`{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-09-30T00:00:02Z","thinking":"instant...","content":"ok"}`))

	for _, ev := range events {
		if ev.Activity == protocol.ActivityStream {
			if ev.TokensPerSecond != 0 {
				t.Errorf("expected turn 2 stream TokensPerSecond to be 0 (not carried over), got %v", ev.TokensPerSecond)
			}
		}
	}
}
