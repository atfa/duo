package gotui

import (
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/gdamore/tcell/v3"
)

// TestSessionsOverlayResumesSelected covers the whole path the picker promises:
// the list is read from the session seam, a row carries mode/phase/duration/cost,
// and Enter asks the session to resume the highlighted id — which is what makes
// the outer CLI take the `duo resume <id>` path after this session is preserved.
func TestSessionsOverlayResumesSelected(t *testing.T) {
	fake := newFakeSession()
	now := time.Now()
	fake.sessions = []sessionstore.Summary{
		{
			SessionID: "old-step",
			Snapshot: sessionstore.Snapshot{
				SessionID: "old-step",
				Mode:      project.ModeStep,
				Phase:     "REVIEW",
				CreatedAt: now.Add(-70 * time.Minute),
				UpdatedAt: now.Add(-60 * time.Minute),
				Usage: &sessionstore.UsageLedger{Agents: map[protocol.AgentID]sessionstore.UsageTotals{
					protocol.Austin: {CostUSD: 1.5},
				}},
			},
		},
		{
			SessionID: "old-goal",
			Snapshot: sessionstore.Snapshot{
				SessionID: "old-goal",
				Mode:      project.ModeGoal,
				Phase:     "PLAN",
				CreatedAt: now.Add(-2 * time.Hour),
				UpdatedAt: now.Add(-90 * time.Minute),
			},
		},
	}
	app := New(fake)
	placeApp(t, app, 120, 40)

	if !app.runSlash("/sessions") {
		t.Fatal("/sessions was not recognised")
	}
	if app.overlay != overlaySessions {
		t.Fatalf("overlay = %v, want the session picker", app.overlay)
	}

	first := app.sessions.row(fake.sessions[0])
	for _, want := range []string{"STEP", "REVIEW", "10m0s", "$1.50", "old-step"} {
		if !strings.Contains(first, want) {
			t.Fatalf("row %q is missing %q", first, want)
		}
	}
	second := app.sessions.row(fake.sessions[1])
	for _, want := range []string{"GOAL", "PLAN", "30m0s", "—", "old-goal"} {
		if !strings.Contains(second, want) {
			t.Fatalf("row %q is missing %q", second, want)
		}
	}

	// A session the observer has named is shown by its title alone; a session
	// without a title keeps its id.
	titled := fake.sessions[0]
	titled.Snapshot.Title = "Fix scheduler race"
	if row := app.sessions.row(titled); !strings.Contains(row, "Fix scheduler race") || strings.Contains(row, "old-step") {
		t.Fatalf("titled row %q should show the title and not the id", row)
	}
	if row := app.sessions.row(fake.sessions[1]); !strings.Contains(row, "old-goal") {
		t.Fatalf("untitled row %q should fall back to the id", row)
	}

	app.handleKey(tcell.NewEventKey(tcell.KeyDown, "", tcell.ModNone))
	if item, ok := app.sessions.selected(); !ok || item.SessionID != "old-goal" {
		t.Fatalf("selected = %q, want the second session", item.SessionID)
	}
	app.handleKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))

	var resumed []string
	for _, cmd := range fake.snapshotCommands() {
		if switchCmd, ok := cmd.(session.ResumeSession); ok {
			resumed = append(resumed, switchCmd.SessionID)
		}
	}
	if len(resumed) != 1 || resumed[0] != "old-goal" {
		t.Fatalf("resume commands = %v, want exactly old-goal", resumed)
	}
	if app.overlay != overlayNone {
		t.Fatalf("overlay = %v after Enter, want it closed", app.overlay)
	}

	// The session reports the request through the view; the interface then stops
	// and the outer CLI resumes the chosen session.
	fake.view.ResumeSession = "old-goal"
	app.refreshView()
	select {
	case <-app.stop:
	default:
		t.Fatal("the interface did not stop after the session reported the switch")
	}
}

func TestSessionsOverlayEscapesAndHandlesAnEmptyList(t *testing.T) {
	fake := newFakeSession()
	app := New(fake)
	placeApp(t, app, 100, 30)
	app.handleKey(tcell.NewEventKey(tcell.KeyCtrlF, "", tcell.ModNone))
	app.closeSearch(true)

	app.runSlash("/sessions")
	if app.overlay != overlaySessions {
		t.Fatalf("overlay = %v, want the session picker", app.overlay)
	}
	// No candidates: Enter must not send a command for an empty list.
	app.handleKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
	if cmds := fake.snapshotCommands(); len(cmds) != 0 {
		t.Fatalf("commands = %v, want none for an empty list", cmds)
	}
	app.handleKey(tcell.NewEventKey(tcell.KeyEsc, "", tcell.ModNone))
	if app.overlay != overlayNone {
		t.Fatalf("Esc left the overlay open")
	}
}
