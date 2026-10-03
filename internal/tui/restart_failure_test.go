package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/coordinator"
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// failingRestartDriver is a CLI driver whose model is a startup flag: applying
// a model has to restart the process, and the restart fails here the same way a
// dead PTY or a vanished worktree would.
type failingRestartDriver struct {
	modelRecordingDriver
	effort string
}

func (d *failingRestartDriver) SetEffort(effort string) { d.effort = effort }

func (d *failingRestartDriver) RestartRunning(ctx context.Context) error {
	d.restarts++
	return context.DeadlineExceeded
}

func restartFailureFixture(t *testing.T, driverType string) (*App, *failingRestartDriver) {
	t.Helper()
	a := pickerFixture()
	a.width, a.height = 80, 24
	bus := events.NewBus()
	state := project.NewStateFor(project.ModeFast)
	a.coord = coordinator.New(nil, state, harness.NewTracker(), nil, bus)
	// Drain on this goroutine: production routes events on the single event
	// loop, and a second goroutine would race these tests' App mutations.
	_, unsub := bus.Subscribe(20)
	defer unsub()

	drv := &failingRestartDriver{
		modelRecordingDriver: modelRecordingDriver{agentID: protocol.Austin, driverType: driverType},
	}
	mgr := agent.NewManager()
	mgr.Add(drv)
	a.agents = mgr
	return a, drv
}

// A failed restart leaves the agent dead, so the picker must not claim the new
// model took effect.
func TestApplySelectedModelReportsFailedRestart(t *testing.T) {
	a, drv := restartFailureFixture(t, "opencode")

	a.applySelectedModel(context.Background(), false)

	if drv.restarts != 1 {
		t.Fatalf("restarts = %d, want one attempt", drv.restarts)
	}
	if !a.statusError {
		t.Errorf("failed restart reported as success: status %q", a.status)
	}
	if !strings.Contains(a.status, "failed") {
		t.Errorf("status = %q, want it to report the failure", a.status)
	}
	if got := a.currentModel[protocol.Austin]; got != "" {
		t.Errorf("currentModel = %q, want unchanged: the agent never got the model", got)
	}
}

// A driver that declares a live model switch takes the model over the bridge,
// so no restart is needed and currentModel must still be recorded.
func TestApplySelectedModelRecordsModelForBridgeDriver(t *testing.T) {
	a, drv := restartFailureFixture(t, "pi")
	drv.liveModelSwitch = true

	a.applySelectedModel(context.Background(), false)

	if drv.restarts != 0 {
		t.Errorf("restarts = %d, want none for a bridge driver", drv.restarts)
	}
	if got := a.currentModel[protocol.Austin]; got != "cline/anthropic/claude-opus" {
		t.Errorf("currentModel = %q, want the selected model", got)
	}
}

// The same restart failure must not be reported as success when cycling the
// thinking level.
func TestCycleThinkingReportsFailedRestart(t *testing.T) {
	a, drv := restartFailureFixture(t, "opencode")
	bus := events.NewBus()
	state := project.NewStateFor(project.ModeFast)
	a.coord = coordinator.New(nil, state, harness.NewTracker(), nil, bus)

	if a.coord == nil {
		bus := events.NewBus()
		state := project.NewStateFor(project.ModeFast)
		a.coord = coordinator.New(nil, state, harness.NewTracker(), nil, bus)
	}
	quit := a.applyAction(context.Background(), inputAction{kind: actionCycleThinking})
	if quit {
		t.Error("a failed restart quit the TUI")
	}
	if drv.restarts != 1 {
		t.Fatalf("restarts = %d, want one attempt", drv.restarts)
	}
	if !strings.Contains(a.status, "failed") {
		t.Errorf("status = %q, want it to report the failure", a.status)
	}
	// The picker header renders currentThinking, so recording the level the
	// agent never received is the same defect as the status line.
	if got := a.currentThinking[protocol.Austin]; got != "" {
		t.Errorf("currentThinking = %q, want unchanged: the agent never got the level", got)
	}
}
