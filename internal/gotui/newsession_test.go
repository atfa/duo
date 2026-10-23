package gotui

import (
	"errors"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/session"
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

// /new is Ctrl+Q plus a fresh session with nothing at stake — the old session
// is preserved — so it acts at once: no dialog, one NewSession, app stops so
// the CLI can start the next session. It behaves the same mid-work and at DONE.
func TestNewSessionActsWithoutConfirmation(t *testing.T) {
	for _, phase := range []project.Phase{project.PhaseExecute, project.PhaseDone} {
		fake, app := newModeSwitchApp(t, project.Snapshot{Mode: project.ModeFast, Phase: phase, Started: true})

		app.runSlash("/new")
		if app.overlay != overlayNone {
			t.Fatalf("%s: /new opened an overlay (%v), want none", phase, app.overlay)
		}
		if got := len(newSessionCommands(fake)); got != 1 {
			t.Fatalf("%s: /new sent %d NewSession command(s), want 1", phase, got)
		}
		if !app.isStopped() {
			t.Fatalf("%s: /new must stop the app so the CLI can start the next session", phase)
		}
	}
}

// A refused request must not stop the app: the session is still the live one.
func TestNewSessionFailureKeepsTheSession(t *testing.T) {
	fake, app := newModeSwitchApp(t, project.Snapshot{Mode: project.ModeFast, Phase: project.PhaseExecute, Started: true})
	fake.doErr = errors.New("boom")

	app.runSlash("/new")
	if app.isStopped() {
		t.Fatal("a failed /new must not stop the app")
	}
}
