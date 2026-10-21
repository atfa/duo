package gotui

import (
	"image"
	"testing"

	ui "github.com/metaspartan/gotui/v5"
)

func TestConfirmButtonsRightAligned(t *testing.T) {
	theme := newTheme()
	c := &confirmOverlay{
		Block:   newModalBlock(theme),
		theme:   theme,
		title:   "Quit Duo?",
		text:    "The session is not finished.",
		choice2: "Cancel",
		choice1: "Confirm",
		active:  1,
	}
	c.place(layout{width: 80, height: 24, main: image.Rect(1, 3, 79, 23)})
	buf := ui.NewBuffer(c.GetRect())
	c.Draw(buf)

	y := c.Inner.Max.Y - 1
	last := c.Inner.Min.X - 1
	for x := c.Inner.Min.X; x < c.Inner.Max.X; x++ {
		if r := buf.GetCell(image.Pt(x, y)).Rune; r != ' ' && r != 0 {
			last = x
		}
	}
	// Both button labels end with two padding spaces, so the last glyph sits
	// three cells inside the content edge.
	if want := c.Inner.Max.X - 3; last != want {
		t.Fatalf("last button glyph at x=%d, want %d (buttons should be right-aligned)", last, want)
	}
}
