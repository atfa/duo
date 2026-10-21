package gotui

import (
	"testing"

	"github.com/atfa/duo/internal/session"
	"github.com/gdamore/tcell/v3"
)

// TestNewlineKeysDoNotSubmit pins the composer's split between Enter and
// newline: every supported newline spelling must keep the task in the composer,
// and only the final plain Enter sends it.
func TestNewlineKeysDoNotSubmit(t *testing.T) {
	app := New(newFakeSession())
	pressText(t, app, "one")
	app.handleKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModCtrl))
	pressText(t, app, "two")
	app.handleKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModShift))
	pressText(t, app, "three")
	app.handleKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModAlt))
	pressText(t, app, "four")
	app.handleKey(tcell.NewEventKey(tcell.KeyCtrlJ, "", tcell.ModNone))
	pressText(t, app, "five")

	want := "one\ntwo\nthree\nfour\nfive"
	if got := app.composer.Text(); got != want {
		t.Fatalf("composer after newline keys = %q, want %q", got, want)
	}
	if commands := app.svc.(*fakeSession).snapshotCommands(); len(commands) != 0 {
		t.Fatalf("a newline key submitted the task: %#v", commands)
	}

	app.handleKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
	commands := app.svc.(*fakeSession).snapshotCommands()
	if len(commands) != 1 {
		t.Fatalf("commands after plain Enter = %d, want 1", len(commands))
	}
	submit, ok := commands[0].(session.SubmitTask)
	if !ok || submit.Text != want {
		t.Fatalf("submitted command = %#v, want the multiline task", commands[0])
	}
}
