package agent

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atfa/duo/internal/agent/agenttest"
	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/protocol"
)

// TestDriverLifecycleAndExitObserver covers the process lifecycle Core owns for
// every agent: start, the exit event a durable log is written from, and the fact
// that a clean exit and a failure stay distinguishable.
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

	austin := mustSession(t, "pi", Config{
		Agent: protocol.Austin, Dir: t.TempDir(),
		BaseCommand: "sh -c 'exit 0'",
	})
	tony := mustSession(t, "agy", Config{
		Agent: protocol.Tony, Dir: t.TempDir(),
		BaseCommand: "sh -c 'exit 3'",
	})
	m.Add(austin)
	m.Add(tony)

	if err := m.StartAll(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.StopAll()
	defer m.CloseAll()

	deadline := time.Now().Add(3 * time.Second)
	var got []LifecycleEvent
	for time.Now().Before(deadline) {
		mu.Lock()
		got = append([]LifecycleEvent(nil), events...)
		mu.Unlock()
		if countKind(got, "agent_exit") >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := countKind(got, "agent_exit"); n < 2 {
		t.Fatalf("got %d exit events, want one per agent: %+v", n, got)
	}
}

// TestSessionPreparesThroughThePlugin is the boundary this refactor exists to
// create: what Core launches comes from the driver, and Core hands the driver back
// the state it was given rather than interpreting it.
func TestSessionPreparesThroughThePlugin(t *testing.T) {
	stub := agenttest.New("stub").With(func(s *agenttest.Spec) {
		s.Command = "sh -c 'exit 0'"
		s.State = []byte(`{"sessionId":"abc"}`)
	})
	s, err := NewSession(context.Background(), Config{
		Agent:       protocol.Austin,
		Dir:         t.TempDir(),
		Plugin:      stub.Caller(),
		PluginState: []byte(`{"sessionId":"abc"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if len(stub.Prepares()) != 0 {
		t.Fatal("NewSession must not launch anything")
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	prepares := stub.Prepares()
	if len(prepares) != 1 {
		t.Fatalf("prepare called %d times, want once per launch", len(prepares))
	}
	if string(prepares[0].State) != `{"sessionId":"abc"}` {
		t.Errorf("prepare received state %q, want the blob Core was given", prepares[0].State)
	}
	if prepares[0].Agent != driver.AgentID(protocol.Austin) {
		t.Errorf("prepare received agent %q", prepares[0].Agent)
	}
	if string(s.DriverState()) != `{"sessionId":"abc"}` {
		t.Errorf("DriverState() = %s, want the plugin's blob verbatim", s.DriverState())
	}
	if s.SessionID() != "test-session" {
		t.Errorf("SessionID() = %q, want the label the plugin reported", s.SessionID())
	}
}

// TestSessionClearsWhatThePluginSaysToClear covers the generic replacement for the
// per-driver log and session files Core used to delete by name: a stale value from
// an earlier run must never be read as this run's.
func TestSessionClearsWhatThePluginSaysToClear(t *testing.T) {
	stale := t.TempDir() + "/stale.session"
	if err := os.WriteFile(stale, []byte("ses_old"), 0o600); err != nil {
		t.Fatal(err)
	}
	var notices []string
	stub := agenttest.New("stub").With(func(s *agenttest.Spec) {
		s.Command = "sh -c 'exit 0'"
		s.Cleanup = []string{stale}
		s.Notices = []string{"a helper tool is missing"}
	})
	s, err := NewSession(context.Background(), Config{
		Agent:   protocol.Austin,
		Dir:     t.TempDir(),
		Plugin:  stub.Caller(),
		Notices: func(got []string) { notices = append(notices, got...) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("the path the plugin asked Core to clear is still there")
	}
	if len(notices) != 1 || notices[0] != "a helper tool is missing" {
		t.Errorf("notices = %v, want the plugin's own words", notices)
	}
}

// TestSessionRefusesADriverThatCannotDescribeItself: an agent must not start
// against a driver that could not say what it is, because everything Core does
// afterwards would be a guess.
func TestSessionRefusesADriverThatCannotDescribeItself(t *testing.T) {
	if _, err := NewSession(context.Background(), Config{Agent: protocol.Austin, Dir: t.TempDir()}); err == nil {
		t.Fatal("a session with no driver must be refused")
	}
}

func TestReplacedDriverLateExitIsIgnored(t *testing.T) {
	m := NewManager()
	var got []LifecycleEvent
	m.SetObserver(func(event LifecycleEvent) { got = append(got, event) })
	old := mustSession(t, "old", Config{Agent: protocol.Austin, Dir: t.TempDir()})
	next := mustSession(t, "new", Config{Agent: protocol.Austin, Dir: t.TempDir()})
	m.Add(old)
	lateExit := old.cfg.OnExit
	m.Add(next)
	lateExit(ExitEvent{Agent: protocol.Austin, State: ProcessExited})
	if len(got) != 0 {
		t.Fatalf("stale old-driver exit reached observer: %+v", got)
	}
	// The current driver's callback still reaches the observer.
	next.cfg.OnExit(ExitEvent{Agent: protocol.Austin, State: ProcessExited})
	if len(got) != 1 || got[0].Kind != "agent_exit" {
		t.Fatalf("current-driver exit events = %+v", got)
	}
	old.Close()
	next.Close()
}

func TestReplaceFailedCandidateRestartsPreviousDriver(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m := NewManager()
	old := mustSession(t, "old", Config{Agent: protocol.Austin, Dir: t.TempDir(), BaseCommand: "sh -c 'exec sleep 30'"})
	m.Add(old)
	if err := old.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.StopAll()
	defer m.CloseAll()

	newPlugin := agenttest.New("new")
	replacement, err := NewSession(ctx, Config{Agent: protocol.Austin, Dir: t.TempDir() + "/missing", Plugin: newPlugin.Caller()})
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.Preflight(ctx); err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if err := m.Replace(ctx, replacement); err == nil {
		t.Fatal("replacement with a missing working directory unexpectedly started")
	}
	current, ok := m.Driver(protocol.Austin)
	if !ok || current != old || !old.Running() {
		t.Fatalf("failed switch did not restore running old driver: current=%T running=%t", current, old.Running())
	}
}

func TestFailedPreflightLeavesPreviousDriverRunning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	old := mustSession(t, "old", Config{Agent: protocol.Austin, Dir: t.TempDir(), BaseCommand: "sh -c 'exec sleep 30'"})
	if err := old.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	defer old.Stop()

	plugin := agenttest.New("new")
	candidate, err := NewSession(ctx, Config{Agent: protocol.Austin, Dir: t.TempDir(), Plugin: plugin.Caller()})
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.Close()
	preflightCtx, stopPreflight := context.WithCancel(ctx)
	stopPreflight()
	if err := candidate.Preflight(preflightCtx); err == nil {
		t.Fatal("preflight unexpectedly succeeded with a canceled context")
	}
	if !old.Running() {
		t.Fatal("failed candidate preflight stopped the previous driver")
	}
}

// TestExportedEnvironmentIsCoreOnly pins the environment contract. A driver that
// needs its own variable supplies it through prepare; Core sets only what every
// agent and every plugin needs, and in particular nothing that names one agent.
func TestExportedEnvironmentIsCoreOnly(t *testing.T) {
	stub := agenttest.New("stub").With(func(s *agenttest.Spec) {
		s.Command = "sh -c 'exit 0'"
		s.Env = map[string]string{"DUO_STUB_ONLY": "mine"}
	})
	s, err := NewSession(context.Background(), Config{
		Agent: protocol.Austin, Dir: t.TempDir(),
		Plugin: stub.Caller(), Session: "sess", Token: "secret",
		RepositoryRoot: "/repo", ScopePath: "internal/x", Mode: "goal",
		Language: "en",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	want := map[string]string{
		"DUO_ACTIVE":          "1",
		"DUO_DRIVER":          "stub",
		"DUO_AGENT":           "Austin",
		"DUO_MODE":            "goal",
		"DUO_LANGUAGE":        "en",
		"DUO_SESSION":         "sess",
		"DUO_TOKEN":           "secret",
		"DUO_REPOSITORY_ROOT": "/repo",
		"DUO_SCOPE_PATH":      "internal/x",
	}
	seen := map[string]string{}
	for _, entry := range coreEnv(s) {
		name, value, _ := strings.Cut(entry, "=")
		seen[name] = value
	}
	for name, value := range want {
		if seen[name] != value {
			t.Errorf("%s = %q, want %q", name, seen[name], value)
		}
	}
	for name := range seen {
		switch name {
		case "DUO_ACTIVE", "DUO_DRIVER", "DUO_AGENT", "DUO_MODE", "DUO_LANGUAGE", "DUO_HOST", "DUO_PORT",
			"DUO_SESSION", "DUO_TOKEN", "DUO_REPOSITORY_ROOT", "DUO_SCOPE_PATH":
		default:
			t.Errorf("Core exports %s, which is not part of its contract", name)
		}
	}
}

func mustSession(t *testing.T, driverName string, cfg Config) *Session {
	t.Helper()
	cfg.Plugin = agenttest.New(driverName).Caller()
	s, err := NewSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func countKind(events []LifecycleEvent, kind string) int {
	var n int
	for _, e := range events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}
