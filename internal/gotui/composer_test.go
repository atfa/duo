package gotui

import (
	"strings"
	"testing"
)

func TestComposerCursorAtVisualWrap(t *testing.T) {
	c := newComposer(newTheme())
	c.SetRect(1, 10, 79, 20)
	width := c.textWidth()
	x0, y0 := c.Inner.Min.X+2, c.Inner.Min.Y

	c.SetText(strings.Repeat("a", width+1))
	c.col = width
	x, y := c.prepare()
	if x != x0 || y != y0+1 {
		t.Fatalf("cursor at wrap = %d,%d, want %d,%d", x, y, x0, y0+1)
	}

	c.col = width + 1
	if !c.Backspace() {
		t.Fatal("backspace at end did nothing")
	}
	x, y = c.prepare()
	if x != x0 || y != y0+1 {
		t.Fatalf("cursor after backspace = %d,%d, want %d,%d", x, y, x0, y0+1)
	}
}

func TestComposerCursorAfterFullLastLine(t *testing.T) {
	c := newComposer(newTheme())
	c.SetRect(1, 10, 79, 20)
	width := c.textWidth()

	c.SetText(strings.Repeat("a", width))
	x, y := c.prepare()
	if x != c.Inner.Min.X+2 || y != c.Inner.Min.Y+1 {
		t.Fatalf("cursor after full line = %d,%d, want %d,%d",
			x, y, c.Inner.Min.X+2, c.Inner.Min.Y+1)
	}
}
