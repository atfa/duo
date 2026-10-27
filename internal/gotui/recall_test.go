package gotui

import (
	"context"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/session"
	"github.com/gdamore/tcell/v3"
)

func recallApp(t *testing.T, preview session.RecallPreview, err error) (*fakeSession, *App) {
	t.Helper()
	fake := newFakeSession()
	fake.recallPreview = preview
	fake.recallPreviewErr = err
	return fake, New(fake)
}

func recallCommands(fake *fakeSession) []session.Recall {
	var out []session.Recall
	for _, command := range fake.snapshotCommands() {
		if recall, ok := command.(session.Recall); ok {
			out = append(out, recall)
		}
	}
	return out
}

func TestRecallConfirmDefaultsToCancelAndSendsOneRecall(t *testing.T) {
	fake, app := recallApp(t, session.RecallPreview{
		Allowed:    true,
		BaseCommit: "abc1234ffffffff",
		Austin:     session.RecallWork{Branch: "duo/x/austin", Commits: 3, Dirty: true},
		Tony:       session.RecallWork{Branch: "duo/x/tony", Commits: 1},
	}, nil)

	app.runSlash("/recall")
	if app.overlay != overlayConfirm {
		t.Fatalf("overlay = %v, want the recall confirmation", app.overlay)
	}
	text := confirmText(app)
	for _, want := range []string{
		"abc1234",
		"duo/x/austin",
		"duo/x/tony",
		"cannot be undone",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("confirmation missing %q:\n%s", want, text)
		}
	}
	if app.confirm.choice2 != "Cancel" {
		t.Fatalf("default button = %q, want Cancel", app.confirm.choice2)
	}

	// Enter on the default (left) button cancels: no command at all.
	app.handleConfirmKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
	if app.overlay != overlayNone {
		t.Fatalf("overlay after default Enter = %v, want none", app.overlay)
	}
	if got := len(recallCommands(fake)); got != 0 {
		t.Fatalf("default Enter sent %d Recall command(s)", got)
	}

	// Confirming sends exactly one Recall.
	app.runSlash("/recall")
	app.handleConfirmKey(tcell.NewEventKey(tcell.KeyRight, "", tcell.ModNone))
	app.handleConfirmKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
	sent := recallCommands(fake)
	if len(sent) != 1 {
		t.Fatalf("confirmed recall sent %d Recall command(s), want 1", len(sent))
	}
}

func TestRecallRefusedShowsReasonWithoutPanel(t *testing.T) {
	fake, app := recallApp(t, session.RecallPreview{Allowed: false, Reason: "already delivered"}, nil)

	app.runSlash("/recall")
	if app.overlay != overlayNone {
		t.Fatalf("overlay = %v, want none for a refused recall", app.overlay)
	}
	if status := app.status; !strings.Contains(status, "already delivered") {
		t.Fatalf("status = %q, want the refusal reason", status)
	}
	if got := len(recallCommands(fake)); got != 0 {
		t.Fatalf("refused recall sent %d Recall command(s)", got)
	}
}

func TestRecallPreviewErrorKeepsPanelClosed(t *testing.T) {
	fake, app := recallApp(t, session.RecallPreview{}, context.DeadlineExceeded)

	app.runSlash("/recall")
	if app.overlay != overlayNone {
		t.Fatalf("overlay = %v, want none when the preview cannot be read", app.overlay)
	}
	if status := app.status; !strings.Contains(status, "recall") {
		t.Fatalf("status = %q, want a recall error", status)
	}
	if got := len(recallCommands(fake)); got != 0 {
		t.Fatalf("failed preview sent %d Recall command(s)", got)
	}
}
