package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atfa/duo/internal/protocol"
)

func TestDriverInterfaceAndPiAgyCommandLines(t *testing.T) {
	// 1. Pi driver session
	piCfg := Config{
		Agent:       protocol.Austin,
		DriverType:  "pi",
		Dir:         t.TempDir(),
		Command:     "pi",
		Session:     "sess-123",
		PiSessionID: "pi-austin-1",
	}
	piSession := NewPiSession(piCfg)

	// Verify Driver interface implementation
	var driver Driver = piSession
	if driver.Agent() != protocol.Austin {
		t.Fatalf("driver.Agent() = %v, want Austin", driver.Agent())
	}
	if driver.DriverType() != "pi" {
		t.Fatalf("driver.DriverType() = %q, want \"pi\"", driver.DriverType())
	}
	if driver.SessionID() != "pi-austin-1" {
		t.Fatalf("driver.SessionID() = %q, want \"pi-austin-1\"", driver.SessionID())
	}
	if cmd := driver.EffectiveCommand(); !strings.Contains(cmd, `pi --session-id "$DUO_PI_SESSION_ID"`) {
		t.Fatalf("pi commandLine = %q, want --session-id", cmd)
	}

	// 2. Agy driver session
	agyCfg := Config{
		Agent:             protocol.Tony,
		DriverType:        "agy",
		Dir:               t.TempDir(),
		Command:           "agy",
		Session:           "sess-123",
		AgyConversationID: "agy-tony-conv",
	}
	agySession := NewAgySession(agyCfg)

	var agyDriver Driver = agySession
	if agyDriver.Agent() != protocol.Tony {
		t.Fatalf("agyDriver.Agent() = %v, want Tony", agyDriver.Agent())
	}
	if agyDriver.DriverType() != "agy" {
		t.Fatalf("agyDriver.DriverType() = %q, want \"agy\"", agyDriver.DriverType())
	}
	if agyDriver.SessionID() != "agy-tony-conv" {
		t.Fatalf("agyDriver.SessionID() = %q, want \"agy-tony-conv\"", agyDriver.SessionID())
	}
	if cmd := agyDriver.EffectiveCommand(); !strings.Contains(cmd, `agy --conversation "$DUO_AGY_CONVERSATION_ID" --dangerously-skip-permissions`) {
		t.Fatalf("agy commandLine = %q, want --conversation and --dangerously-skip-permissions", cmd)
	}
	agySession.SetModel("google/gemini-3.8-flash-low")
	if cmd := agyDriver.EffectiveCommand(); !strings.Contains(cmd, `--model "gemini-3.8-flash-low"`) {
		t.Fatalf("agy commandLine = %q, want --model \"gemini-3.8-flash-low\"", cmd)
	}
	agySession.SetModel("")

	// 3. Manager holding heterogeneous drivers
	m := NewManager()
	var events []LifecycleEvent
	m.SetObserver(func(e LifecycleEvent) {
		events = append(events, e)
	})

	m.Add(driver)
	m.Add(agyDriver)

	d1, ok1 := m.Driver(protocol.Austin)
	if !ok1 || d1.DriverType() != "pi" {
		t.Fatalf("m.Driver(Austin) = %v, %v, want pi driver", d1, ok1)
	}
	d2, ok2 := m.Session(protocol.Tony) // tests backward-compatible Session() method
	if !ok2 || d2.DriverType() != "agy" {
		t.Fatalf("m.Session(Tony) = %v, %v, want agy driver", d2, ok2)
	}
	if cmd := m.Command(); cmd != "pi" {
		t.Fatalf("m.Command() = %q, want \"pi\"", cmd)
	}
}

func TestDriverLifecycleAndExitObserver(t *testing.T) {
	ctx := context.Background()
	m := NewManager()
	var mu sync.Mutex
	var events []LifecycleEvent
	m.SetObserver(func(e LifecycleEvent) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})

	austin := NewSession(Config{
		Agent:   protocol.Austin,
		Dir:     t.TempDir(),
		Command: "sh -c 'exit 0'",
	})
	tony := NewAgySession(Config{
		Agent:   protocol.Tony,
		Dir:     t.TempDir(),
		Command: "sh -c 'exit 0'",
	})

	m.Add(austin)
	m.Add(tony)

	if err := m.StartAll(ctx); err != nil {
		t.Fatal(err)
	}

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		var exitCount int
		for _, e := range events {
			if e.Kind == "agent_exit" {
				exitCount++
			}
		}
		return exitCount == 2
	})

	mu.Lock()
	defer mu.Unlock()
	var startCount, exitCount int
	for _, e := range events {
		switch e.Kind {
		case "agent_start":
			startCount++
		case "agent_exit":
			exitCount++
		}
	}
	if startCount != 2 {
		t.Fatalf("expected 2 agent_start events, got %d", startCount)
	}
	if exitCount != 2 {
		t.Fatalf("expected 2 agent_exit events, got %d", exitCount)
	}
}

func TestAgyDriverSessionWithActivitySink(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "agy.log")
	convID := "test-conv-1234-5678-90ab"

	// Mock brain directory
	brainDir := filepath.Join(tmpDir, "brain", convID, ".system_generated", "logs")
	if err := os.MkdirAll(brainDir, 0755); err != nil {
		t.Fatal(err)
	}
	transcriptFile := filepath.Join(brainDir, "transcript.jsonl")

	// Set env to redirect agyAppDataDir
	t.Setenv("ANTIGRAVITY_APP_DATA_DIR", tmpDir)

	// Write log file with conversation ID
	if err := os.WriteFile(logFile, []byte("I0930 08:40:41.075572 1 server.go:1248] Created conversation "+convID+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Write initial transcript
	if err := os.WriteFile(transcriptFile, []byte("{\"step_index\":0,\"source\":\"USER_EXPLICIT\",\"type\":\"USER_INPUT\",\"status\":\"DONE\",\"created_at\":\"2026-09-30T00:00:00Z\",\"content\":\"test\"}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	activityCh := make(chan protocol.Message, 10)
	sink := FuncActivitySink(func(_ protocol.AgentID, msg protocol.Message) {
		activityCh <- msg
	})

	agySession := NewAgySession(Config{
		Agent:             protocol.Austin,
		DriverType:        "agy",
		Dir:               tmpDir,
		LogFile:           logFile,
		AgyConversationID: convID,
		ActivitySink:      sink,
		Command:           "sh -c 'sleep 2'",
	})

	if err := agySession.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer agySession.Stop()

	// Verify activity was captured
	select {
	case msg := <-activityCh:
		if msg.Activity != protocol.ActivityAgentStart {
			t.Fatalf("expected ActivityAgentStart, got %v", msg.Activity)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for activity from agy watcher")
	}

	// Now append a tool call
	f, err := os.OpenFile(transcriptFile, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("{\"step_index\":1,\"source\":\"MODEL\",\"type\":\"PLANNER_RESPONSE\",\"status\":\"DONE\",\"created_at\":\"2026-09-30T00:00:01Z\",\"tool_calls\":[{\"name\":\"duo_send\",\"args\":{\"message\":\"hello peer\"}}]}\n")
	_ = f.Close()

	select {
	case msg := <-activityCh:
		if msg.Activity != protocol.ActivityToolStart || msg.Tool != "duo_send" || msg.Detail != "hello peer" {
			t.Fatalf("expected ActivityToolStart duo_send with 'hello peer', got: %#v", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for duo_send activity")
	}
}

func TestAgyDriverSessionWithDelayedConversationCreation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "agy.log")
	actualConvID := "a1b2c3d4-e5f6-4890-abcd-ef1234567890"

	t.Setenv("ANTIGRAVITY_APP_DATA_DIR", tmpDir)

	activityCh := make(chan protocol.Message, 10)
	sink := FuncActivitySink(func(_ protocol.AgentID, msg protocol.Message) {
		activityCh <- msg
	})

	agySession := NewAgySession(Config{
		Agent:             protocol.Austin,
		DriverType:        "agy",
		Dir:               tmpDir,
		LogFile:           logFile,
		AgyConversationID: "initial-dummy-id-not-found",
		ActivitySink:      sink,
		Command:           "sh -c 'sleep 3'",
	})

	if err := agySession.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer agySession.Stop()

	// Wait 250ms to ensure watcher is polling and hasn't crashed or given up
	time.Sleep(250 * time.Millisecond)

	// Now simulate agy creating a new conversation upon user prompt
	brainDir := filepath.Join(tmpDir, "brain", actualConvID, ".system_generated", "logs")
	if err := os.MkdirAll(brainDir, 0755); err != nil {
		t.Fatal(err)
	}
	transcriptFile := filepath.Join(brainDir, "transcript.jsonl")

	// 1. Write the log entry indicating conversation creation
	if err := os.WriteFile(logFile, []byte("I1001 06:33:40.857869 1092 server.go:1248] Created conversation "+actualConvID+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// 2. Write the transcript steps
	transcriptContent := "{\"step_index\":0,\"source\":\"USER_EXPLICIT\",\"type\":\"USER_INPUT\",\"status\":\"DONE\",\"created_at\":\"2026-10-01T06:33:40Z\",\"content\":\"create readme\"}\n" +
		"{\"step_index\":1,\"source\":\"MODEL\",\"type\":\"PLANNER_RESPONSE\",\"status\":\"DONE\",\"created_at\":\"2026-10-01T06:33:41Z\",\"tool_calls\":[{\"name\":\"write_to_file\",\"args\":{\"TargetFile\":\"\\\"README.md\\\"\"}}]}\n" +
		"{\"step_index\":2,\"source\":\"MODEL\",\"type\":\"PLANNER_RESPONSE\",\"status\":\"DONE\",\"created_at\":\"2026-10-01T06:33:42Z\",\"content\":\"README created successfully.\"}\n"
	if err := os.WriteFile(transcriptFile, []byte(transcriptContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Verify events are captured in sequence:
	// Event 1: ActivityAgentStart (from USER_INPUT)
	select {
	case msg := <-activityCh:
		if msg.Activity != protocol.ActivityAgentStart {
			t.Fatalf("expected ActivityAgentStart, got %#v", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for ActivityAgentStart")
	}

	// Event 2: ActivityToolStart (from write_to_file)
	select {
	case msg := <-activityCh:
		if msg.Activity != protocol.ActivityToolStart || msg.Tool != "write_to_file" || msg.Detail != "README.md" {
			t.Fatalf("expected ActivityToolStart write_to_file with 'README.md', got %#v", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for ActivityToolStart")
	}

	// Event 3: MsgAssistantMessage (from final PLANNER_RESPONSE content)
	select {
	case msg := <-activityCh:
		if msg.Type != protocol.MsgAssistantMessage || msg.Text != "README created successfully." {
			t.Fatalf("expected MsgAssistantMessage with 'README created successfully.', got %#v", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for MsgAssistantMessage")
	}

	// Event 4: ActivityStream (from final content)
	select {
	case msg := <-activityCh:
		if msg.Activity != protocol.ActivityStream || msg.Detail != "README created successfully." {
			t.Fatalf("expected ActivityStream, got %#v", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for ActivityStream")
	}

	// Event 5: ActivityAgentSettled
	select {
	case msg := <-activityCh:
		if msg.Activity != protocol.ActivityAgentSettled {
			t.Fatalf("expected ActivityAgentSettled, got %#v", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for ActivityAgentSettled")
	}

	// Verify conversation ID on session was updated to the actual one
	if agySession.SessionID() != actualConvID {
		t.Errorf("agySession.SessionID() = %q, want %q", agySession.SessionID(), actualConvID)
	}
}


