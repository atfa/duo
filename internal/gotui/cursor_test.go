package gotui

import (
	"testing"

	"github.com/gdamore/tcell/v3"
	ui "github.com/metaspartan/gotui/v5"
)

type cursorPoint struct{ x, y int }

type drawProbeScreen struct {
	tcell.Screen
	width, height    int
	cursorX, cursorY int
	shown            []cursorPoint
}

func (s *drawProbeScreen) Size() (int, int) { return s.width, s.height }

func (s *drawProbeScreen) SetContent(int, int, rune, []rune, tcell.Style) {}

func (s *drawProbeScreen) Show() {
	s.shown = append(s.shown, cursorPoint{s.cursorX, s.cursorY})
}

func (s *drawProbeScreen) ShowCursor(x, y int) { s.cursorX, s.cursorY = x, y }

func (s *drawProbeScreen) HideCursor() { s.cursorX, s.cursorY = -1, -1 }

func TestDrawFlushesCursorWithText(t *testing.T) {
	app := New(newFakeSession())
	screen := &drawProbeScreen{width: 80, height: 24, cursorX: -1, cursorY: -1}
	app.screen = screen
	app.backend = &ui.Backend{Screen: screen}
	app.width, app.height = 80, 24

	app.draw()
	screen.shown = nil
	app.composer.SetText("A")
	app.draw()

	if len(screen.shown) == 0 {
		t.Fatal("draw did not flush the screen")
	}
	got := screen.shown[len(screen.shown)-1]
	wantX, wantY := app.composer.prepare()
	if got.x != wantX || got.y != wantY {
		t.Fatalf("cursor flushed at %d,%d, want %d,%d", got.x, got.y, wantX, wantY)
	}
}
