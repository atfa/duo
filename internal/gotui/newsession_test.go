package gotui

import (
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/session"
	"github.com/gdamore/tcell/v3"
)

func newSessionCommands(fake *fakeSession) []session.NewSession {
	var out []session.NewSession
	for _, command := range fake.snapshotCommands() {
		if c, ok := command.(session.NewSession); ok {
			out = append(out, c)
		}
	}
	return out
}

// TestNewSessionCommandQuitsThenRelaunches is the /new flow: an unfinished
// session confirms, and confirming sends NewSession (which the CLI answers by
// re-running duo) and stops the app so the close path runs.
func TestNewSessionCommandQuitsThenRelaunches(t *testing.T) {
	fake, app := newModeSwitchApp(t, project.Snapshot{
		Mode:    project.ModeFast,
		Phase:   project.PhaseExecute,
		Started: true,
	})

	app.runSlash("/new")
	if app.overlay != overlayConfirm {
		t.Fatalf("overlay = %v, want the new-session confirmation", app.overlay)
	}
	if app.confirm.choice1 != "Confirm" || app.confirm.choice2 != "Cancel" {
		t.Fatalf("buttons = %q / %q, want Confirm / Cancel", app.confirm.choice1, app.confirm.choice2)
	}

	// Esc cancels: no command, the app keeps running.
	app.handleConfirmKey(tcell.NewEventKey(tcell.KeyEsc, "", tcell.ModNone))
	if got := len(newSessionCommands(fake)); got != 0 {
		t.Fatalf("Esc sent %d NewSession command(s), want 0", got)
	}
	if app.isStopped() {
		t.Fatal("a cancelled /new must not stop the app")
	}

	// Confirming sends exactly one NewSession and stops the app.
	app.runSlash("/new")
	app.handleConfirmKey(tcell.NewEventKey(tcell.KeyRight, "", tcell.ModNone))
	app.handleConfirmKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
	commands := newSessionCommands(fake)
	if len(commands) != 1 {
		t.Fatalf("confirmed /new sent %d NewSession command(s), want 1", len(commands))
	}
	if !app.isStopped() {
		t.Fatal("a confirmed /new must stop the app so the CLI can re-run duo")
	}
}

// A finished session has nothing to preserve, so /new acts without a dialog —
// the same rule /quit follows.
func TestNewSessionAtDoneNeedsNoConfirmation(t *testing.T) {
	fake, app := newModeSwitchApp(t, project.Snapshot{
		Mode:    project.ModeFast,
		Phase:   project.PhaseDone,
		Started: true,
	})

	app.runSlash("/new")
	if app.overlay != overlayNone {
		t.Fatalf("overlay = %v, want none at DONE", app.overlay)
	}
	if got := len(newSessionCommands(fake)); got != 1 {
		t.Fatalf("DONE /new sent %d NewSession command(s), want 1", got)
	}
	if !app.isStopped() {
		t.Fatal("a confirmed /new must stop the app")
	}
}
