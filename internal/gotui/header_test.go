package gotui

import (
	"github.com/gdamore/tcell/v3"
	"image"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/session"
	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui/v5"
)

// A paused badge is much wider than the old fixed 18-column reservation. The
// path must be truncated before the badge instead of being drawn underneath it.
func TestHeaderReservesThePausedBadgeWidth(t *testing.T) {
	h := newHeader(newTheme())
	h.SetRect(0, 0, 120, 2)
	h.repo = "repo"
	h.branch = strings.Repeat("b", 80)
	h.mode = "FAST"
	h.phase = "RUNNING"
	h.pause = session.PausePaused

	buf := ui.NewBuffer(h.GetRect())
	h.Draw(buf)

	badge := "FAST  ·  RUNNING  ·  PAUSED"
	if got := h.modeBadge.Dx(); got != rw.StringWidth(badge) {
		t.Fatalf("badge width = %d, want %d", got, rw.StringWidth(badge))
	}
	// With a long branch the path is truncated before the badge. The old fixed
	// reservation let the badge start 11 columns earlier and overwrite the
	// ellipsis, so scanning left of the badge proves the reservation grew.
	found := false
	for x := 0; x < h.modeBadge.Min.X; x++ {
		if buf.GetCell(image.Pt(x, 0)).Rune == '…' {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("path was not truncated before the paused badge")
	}
}

func TestManagerReturnButtonDoesNotQuit(t *testing.T) {
	a := New(&fakeSession{})
	called := 0
	a.SetReturnToManager(func() error { called++; return nil })
	a.header.SetRect(0, 0, 120, 2)
	a.header.Draw(ui.NewBuffer(a.header.GetRect()))
	r := a.header.returnButton
	if r.Empty() || r.Overlaps(a.header.modeBadge) {
		t.Fatal("return button missing or overlaps mode badge")
	}
	a.handleMouse(tcell.NewEventMouse(r.Min.X, r.Min.Y, tcell.Button1, tcell.ModNone))
	if called != 1 {
		t.Fatal(called)
	}
	select {
	case <-a.stop:
		t.Fatal("return stopped Duo")
	default:
	}
	a.SetReturnToManager(nil)
	a.header.Draw(ui.NewBuffer(a.header.GetRect()))
	if !a.header.returnButton.Empty() {
		t.Fatal("standalone Duo shows return button")
	}
}
