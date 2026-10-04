package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/protocol"
)

// Whether a model switch needs a process restart, and how a failed restart is
// reported, is the session's to decide (see internal/session). What the interface
// owes the human is an honest status line, so these tests drive it with a session
// that refuses the command.

func restartFailureFixture(t *testing.T, err error) (*App, *fakeSession) {
	t.Helper()
	a := pickerFixture()
	a.width, a.height = 80, 24
	fake := &fakeSession{err: err}
	a.svc = fake
	a.setDriver(protocol.Austin, &modelRecordingDriver{agentID: protocol.Austin, driverType: "opencode"})
	return a, fake
}

// A refused model switch leaves the agent on its old model, so the picker must not
// claim the new one took effect.
func TestApplySelectedModelReportsFailedRestart(t *testing.T) {
	a, _ := restartFailureFixture(t, errors.New("opencode restart failed"))

	a.applySelectedModel(context.Background(), false)

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

// When the session accepts the switch, the interface records the model it asked
// for so the header and picker show it.
func TestApplySelectedModelRecordsModel(t *testing.T) {
	a, fake := restartFailureFixture(t, nil)

	a.applySelectedModel(context.Background(), false)

	if !fake.issued("set_model") {
		t.Fatal("the switch never reached the session")
	}
	if got := a.currentModel[protocol.Austin]; got != "cline/anthropic/claude-opus" {
		t.Errorf("currentModel = %q, want the selected model", got)
	}
}

// The same refusal must not be reported as success when cycling the thinking level.
func TestCycleThinkingReportsFailedRestart(t *testing.T) {
	a, _ := restartFailureFixture(t, errors.New("opencode restart failed"))

	quit := a.applyAction(context.Background(), inputAction{kind: actionCycleThinking})
	if quit {
		t.Error("a failed restart quit the TUI")
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
