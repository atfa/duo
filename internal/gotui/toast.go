package gotui

import (
	"image"
	"time"

	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui/v5"
)

// toastPanel is a short-lived corner notification. It is drawn after the
// transcript so it can sit over the top-right of the conversation without
// changing any pane geometry.
type toastPanel struct {
	*ui.Block
	theme theme
	text  string
	until time.Time
}

func newToast(t theme) *toastPanel {
	n := &toastPanel{Block: ui.NewBlock(), theme: t}
	n.BorderRounded = true
	n.BorderStyle = ui.Style{Fg: t.Accent, Bg: t.PanelHigh}
	n.BackgroundColor = t.PanelHigh
	n.TitleStyle = ui.Style{Fg: t.Accent, Bg: t.PanelHigh, Modifier: ui.ModifierBold}
	n.PaddingLeft, n.PaddingRight = 1, 1
	return n
}

func (n *toastPanel) show(text string, until time.Time) {
	n.text = text
	n.until = until
}

func (n *toastPanel) place(l layout) {
	width := max(16, rw.StringWidth(n.text)+6)
	if l.main.Dx() > 2 {
		width = min(width, max(16, l.main.Dx()-2))
	}
	height := 3
	if l.main.Dy() < height {
		height = max(1, l.main.Dy())
	}
	x := max(l.main.Min.X, l.main.Max.X-width-1)
	y := l.main.Min.Y + 1
	if y+height > l.main.Max.Y {
		y = max(l.main.Min.Y, l.main.Max.Y-height)
	}
	n.SetRect(x, y, x+width, y+height)
}

func (n *toastPanel) Draw(buf *ui.Buffer) {
	fillBlock(buf, n.GetRect(), ui.Style{Fg: n.theme.Fg, Bg: n.theme.PanelHigh})
	n.Title = " " + n.theme.tr("COPIED") + " "
	n.Block.Draw(buf)
	if n.Inner.Dy() <= 0 || n.Inner.Dx() <= 0 {
		return
	}
	buf.SetString(fitText(n.text, n.Inner.Dx()), ui.Style{Fg: n.theme.Fg, Bg: n.theme.PanelHigh}, image.Pt(n.Inner.Min.X, n.Inner.Min.Y))
}
