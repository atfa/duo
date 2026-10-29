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

func TestHeaderButtonsShareRightEdge(t *testing.T) {
	h := newHeader(newTheme())
	h.SetRect(0, 0, 120, 2)
	h.showReturn = true
	h.view = viewTimeline
	buf := ui.NewBuffer(h.GetRect())
	h.Draw(buf)
	for y := 0; y < 2; y++ {
		last := -1
		for x := 0; x < h.Max.X; x++ {
			if buf.GetCell(image.Pt(x, y)).Style.Bg == h.theme.Accent {
				last = x
			}
		}
		if last != h.Max.X-2 {
			t.Fatalf("row %d button ends at %d, want %d", y, last, h.Max.X-2)
		}
	}
	if h.returnButton.Dx() != rw.StringWidth("[ Back to xduo ]") {
		t.Fatal("button hitbox differs from visible width")
	}
}

func TestHeaderAndFooterStayInsideBounds(t *testing.T) {
	for _, width := range []int{8, 20, 40, 60, 80, 120} {
		h := newHeader(newTheme())
		h.SetRect(0, 0, width, 2)
		h.version = strings.Repeat("v", 50)
		h.showReturn = true
		h.mode, h.phase = "FAST", "RUNNING"
		h.pause = session.PausePaused
		buf := ui.NewBuffer(image.Rect(0, 0, width+100, 3))
		h.Draw(buf)
		f := newFooter(h.theme)
		f.SetRect(0, 2, width, 3)
		f.hint, f.right = strings.Repeat("hint ", 30), strings.Repeat("right ", 30)
		f.Draw(buf)
		for y := 0; y < 3; y++ {
			for x := width; x < width+100; x++ {
				if c := buf.GetCell(image.Pt(x, y)); c.Rune != 0 && c.Rune != ' ' {
					t.Fatalf("width %d: text outside widget at %d,%d", width, x, y)
				}
			}
		}
	}
}
