package gotui

import (
	"image"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v3"
	ui "github.com/metaspartan/gotui/v5"
)

// These tests pin the repaired behaviour of the @-mention palette: a query that
// matches nothing must keep the palette session alive, so Backspace narrows it
// back into results. They are written against the pre-fix API on purpose - they
// fail on a tree whose palette closes on an empty match set, and pass once the
// palette keeps the token.

// mentionAppWithComposer builds an app with a sized composer, so the visual
// layout does not wrap the token into one column per rune.
func mentionAppWithComposer(t *testing.T, files ...string) (*App, *int) {
	t.Helper()
	app, calls := mentionApp(t, files...)
	app.composer.SetRect(1, 18, 79, 22)
	return app, calls
}

// renderMentionMenu places the app's file palette above the composer and
// renders it, returning the buffer and the box the palette occupies.
func renderMentionMenu(t *testing.T, app *App) (*ui.Buffer, image.Rectangle) {
	t.Helper()
	l := layout{width: 80, height: 24, main: image.Rect(1, 3, 79, 18)}
	menu := app.fileMenu
	menu.place(l, app.composer)
	buf := ui.NewBuffer(menu.GetRect())
	menu.Draw(buf)
	return buf, menu.box
}

// bufferRows returns the text of every row of the buffer.
func bufferRows(buf *ui.Buffer) []string {
	rows := make([]string, 0, buf.Dy())
	for y := buf.Min.Y; y < buf.Max.Y; y++ {
		var b strings.Builder
		for x := buf.Min.X; x < buf.Max.X; x++ {
			r := buf.GetCell(image.Pt(x, y)).Rune
			if r == 0 {
				r = ' '
			}
			b.WriteRune(r)
		}
		rows = append(rows, b.String())
	}
	return rows
}

// A mistyped query that matches nothing must not end the palette session: the
// token stays alive so Backspace can narrow it back into results.
func TestMentionPaletteRendersNoFilesFound(t *testing.T) {
	app, _ := mentionAppWithComposer(t, "abc.py")
	pressText(t, app, "@acc")
	if app.mentionStart < 0 {
		t.Fatal("a query with no match closed the palette")
	}
	buf, box := renderMentionMenu(t, app)
	if box.Dy() < 4 {
		t.Fatalf("empty palette box height = %d, want room for the message and the hint", box.Dy())
	}
	found := false
	for _, row := range bufferRows(buf) {
		if strings.Contains(row, "no files found") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the empty palette did not render \"no files found\":\n%s", strings.Join(bufferRows(buf), "\n"))
	}
}

func TestMentionPaletteEmptyStateDoesNotSwallowKeys(t *testing.T) {
	t.Run("up and down stay with the composer", func(t *testing.T) {
		app, _ := mentionAppWithComposer(t, "abc.py")
		app.history = nil
		pressText(t, app, "@acc")

		app.handleKey(tcell.NewEventKey(tcell.KeyUp, "", tcell.ModNone))
		if got := app.composer.Text(); got != "@acc" {
			t.Fatalf("composer after Up = %q, want %q", got, "@acc")
		}
		app.handleKey(tcell.NewEventKey(tcell.KeyDown, "", tcell.ModNone))
		if got := app.composer.Text(); got != "@acc" {
			t.Fatalf("composer after Down = %q, want %q", got, "@acc")
		}
		if app.mentionStart < 0 {
			t.Fatal("Up/Down closed the empty palette")
		}
	})

	t.Run("tab falls through to the indent", func(t *testing.T) {
		app, _ := mentionAppWithComposer(t, "abc.py")
		pressText(t, app, "@acc")
		app.handleKey(tcell.NewEventKey(tcell.KeyTab, "", tcell.ModNone))
		if got := app.composer.Text(); got != "@acc    " {
			t.Fatalf("composer after Tab = %q, want %q", got, "@acc    ")
		}
	})

	t.Run("enter still submits", func(t *testing.T) {
		app, _ := mentionAppWithComposer(t, "abc.py")
		pressText(t, app, "@acc")
		app.handleKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
		if got := app.composer.Text(); got != "" {
			t.Fatalf("composer after Enter = %q, want the message to be submitted", got)
		}
	})
}
