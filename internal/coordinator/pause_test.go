package coordinator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/agent/agenttest"
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

func TestPauseTracksSettledBoundariesAndLateStart(t *testing.T) {
	c := New(nil, project.NewState(), harness.NewTracker(), nil, events.NewBus())
	austin := newMockDriver(protocol.Austin, "observed", observedFromOutside())
	austin.running = true
	mgr := agent.NewManager()
	mgr.Add(austin)
	c.SetAgents(mgr)
	c.tracker.Handle(protocol.Austin, protocol.ActivityAgentStart)
	c.refreshPause(protocol.Austin, protocol.ActivityAgentStart)
	if err := c.SetPause(); err != nil {
		t.Fatal(err)
	}
	if got := c.PauseSnapshot(); got.State != "pausing" || got.Paused[protocol.Austin] || !got.Paused[protocol.Tony] {
		t.Fatalf("pause snapshot = %+v", got)
	}
	c.tracker.Handle(protocol.Austin, protocol.ActivityAgentSettled)
	c.refreshPause(protocol.Austin, protocol.ActivityAgentSettled)
	if got := c.PauseSnapshot(); got.State != "paused" || !got.Paused[protocol.Austin] || !got.Paused[protocol.Tony] {
		t.Fatalf("settled snapshot = %+v", got)
	}
	c.tracker.Handle(protocol.Austin, protocol.ActivityAgentStart)
	c.refreshPause(protocol.Austin, protocol.ActivityAgentStart)
	if got := c.PauseSnapshot(); got.State != "pausing" || got.Paused[protocol.Austin] {
		t.Fatalf("late start did not reopen pause: %+v", got)
	}
}

func TestContinueCombinesHeldWorkIntoSingleResumePrompt(t *testing.T) {
	c := promptCoordinator()
	c.project = project.NewStateFor(project.ModeGoal)
	c.tracker = harness.NewTracker()
	c.bus = events.NewBus()
	austin := newMockDriver(protocol.Austin, "observed", observedFromOutside())
	tony := newMockDriver(protocol.Tony, "observed", observedFromOutside())
	austin.running, tony.running = true, true
	mgr := agent.NewManager()
	mgr.Add(austin)
	mgr.Add(tony)
	c.SetAgents(mgr)
	c.SetResumeContext("/repo", "session-1")
	if err := c.SetPause(); err != nil {
		t.Fatal(err)
	}
	if got := c.PauseSnapshot().State; got != "paused" {
		t.Fatalf("pause state = %s", got)
	}
	if err := c.sendToAgent(context.Background(), protocol.Austin, protocol.Message{Type: protocol.MsgHumanPrompt, Text: "finish task"}); err != nil {
		t.Fatal(err)
	}
	if err := c.sendToAgent(context.Background(), protocol.Austin, protocol.Message{Type: protocol.MsgResumePrompt, Text: "stale"}); err != nil {
		t.Fatal(err)
	}
	if len(c.held) != 1 {
		t.Fatalf("held messages = %d, want 1", len(c.held))
	}
	if err := c.ContinuePause(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := c.PauseSnapshot().State; got != "running" {
		t.Fatalf("state after Continue = %s", got)
	}
	if len(c.held) != 0 {
		t.Fatalf("held messages remain: %d", len(c.held))
	}
	if len(austin.written) != 1 || !strings.Contains(austin.written[0], "Duo held these while paused") || !strings.Contains(austin.written[0], "finish task") {
		t.Fatalf("Austin should receive one combined prompt, got %#v", austin.written)
	}
	if len(tony.written) != 1 || !strings.Contains(tony.written[0], "/repo/.duo/logs/session-1.md") || !strings.Contains(tony.written[0], "/repo/.duo/logs/session-1-tony.md") {
		t.Fatalf("Tony should receive one resume prompt with transcript context, got %#v", tony.written)
	}
}

func TestUnpauseFlushesHeldMessagesWithoutResumingPausedSession(t *testing.T) {
	c := New(nil, project.NewState(), harness.NewTracker(), nil, events.NewBus())
	austin := newMockDriver(protocol.Austin, "observed", observedFromOutside())
	austin.running = true
	mgr := agent.NewManager()
	mgr.Add(austin)
	c.SetAgents(mgr)
	c.tracker.Handle(protocol.Austin, protocol.ActivityAgentStart)
	c.refreshPause(protocol.Austin, protocol.ActivityAgentStart)
	if err := c.SetPause(); err != nil {
		t.Fatal(err)
	}
	if err := c.sendToAgent(context.Background(), protocol.Austin, protocol.Message{Type: protocol.MsgDuoNotice, Text: "notice"}); err != nil {
		t.Fatal(err)
	}
	if err := c.CancelPause(); err != nil {
		t.Fatal(err)
	}
	if got := c.PauseSnapshot().State; got != "running" {
		t.Fatalf("state = %s", got)
	}
	if len(austin.written) != 1 || !strings.Contains(austin.written[0], "notice") {
		t.Fatalf("held notice not flushed: %#v", austin.written)
	}
	c.tracker.Handle(protocol.Austin, protocol.ActivityAgentSettled)
	c.refreshPause(protocol.Austin, protocol.ActivityAgentSettled)
	if err := c.SetPause(); err != nil {
		t.Fatal(err)
	}
	if err := c.CancelPause(); err == nil {
		t.Fatal("/unpause after completed pause should require /continue")
	}
}

func TestPausedRejectsHumanTaskButDoesNotHoldHarnessRelease(t *testing.T) {
	c := New(nil, project.NewState(), harness.NewTracker(), nil, events.NewBus())
	if err := c.SetPause(); err != nil {
		t.Fatal(err)
	}
	if err := c.SubmitUserTask(context.Background(), "new task"); err == nil {
		t.Fatal("task accepted while paused")
	}
	c.holdHarness()
	c.releaseHarness()
	if !c.HarnessHeld() {
		t.Fatal("quiet mode release must not clear pause hold")
	}
}

func TestDriverSwitchAllowsSynchronousLifecycleCallbacks(t *testing.T) {
	ctx := context.Background()
	stub := agenttest.New("stub").With(func(spec *agenttest.Spec) { spec.Command = "sh -c 'exec sleep 60'" })
	previous, err := agent.NewSession(ctx, agent.Config{Agent: protocol.Austin, Dir: t.TempDir(), Plugin: stub.Caller()})
	if err != nil {
		t.Fatal(err)
	}
	defer previous.Close()
	if err := previous.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer previous.Stop()

	manager := agent.NewManager()
	manager.Add(previous)
	c := New(nil, project.NewState(), harness.NewTracker(), nil, events.NewBus())
	c.SetAgents(manager)
	manager.SetObserver(func(event agent.LifecycleEvent) { c.RefreshPause(event.Agent) })
	if err := c.SetPause(); err != nil {
		t.Fatal(err)
	}
	if got := c.PauseSnapshot().State; got != "paused" {
		t.Fatalf("pause state = %s", got)
	}

	replacement, err := previous.WithDriver(ctx, stub.Caller(), "stub", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.Preflight(ctx); err != nil {
		t.Fatal(err)
	}
	release, err := c.BeginDriverSwitch(protocol.Austin)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- manager.Replace(ctx, replacement) }()
	select {
	case err := <-done:
		release()
		if err != nil {
			t.Fatalf("replace: %v", err)
		}
	case <-time.After(3 * time.Second):
		release()
		t.Fatal("Manager.Replace deadlocked in a pause lifecycle callback")
	}
	manager.StopAll()
}
