package gotui

import (
	"fmt"
	"image"

	"github.com/atfa/duo/internal/project"
	ui "github.com/metaspartan/gotui/v5"
)

// modePicker is the one place a human changes the workflow without remembering
// the slash spelling. It is a projection only: applying a choice funnels through
// requestModeSwitch, the same path /mode and /escalate use, so the panel cannot
// invent a different mode-change rule.
type modePicker struct {
	*ui.Block
	theme   theme
	current project.Mode
	cursor  int
	box     image.Rectangle
}

var pickerModes = []project.Mode{project.ModeFast, project.ModeGoal, project.ModeStep}

func newModePicker(t theme) *modePicker {
	p := &modePicker{Block: newModalBlock(t), theme: t}
	p.Title = " MODE "
	return p
}

func (p *modePicker) place(l layout) {
	p.box = centered(l, 68, 9)
	p.Block.SetRect(p.box.Min.X, p.box.Min.Y, p.box.Max.X, p.box.Max.Y)
	p.clamp()
}

// begin points the cursor at the mode in use, so Enter on an unchanged panel
// closes it with the existing "already in" answer instead of switching.
func (p *modePicker) begin(current project.Mode) {
	p.current = current
	p.cursor = 0
	for i, mode := range pickerModes {
		if mode == current {
			p.cursor = i
			return
		}
	}
}

func (p *modePicker) clamp() {
	if p.cursor < 0 {
		p.cursor = 0
	}
	if p.cursor >= len(pickerModes) {
		p.cursor = len(pickerModes) - 1
	}
}

func (p *modePicker) move(delta int) {
	p.cursor = (p.cursor + delta + len(pickerModes)) % len(pickerModes)
}

func (p *modePicker) selected() project.Mode {
	p.clamp()
	return pickerModes[p.cursor]
}

// modePurpose is the one-line explanation shown next to each mode. It is a
// translation key, not protocol text.
func modePurpose(mode project.Mode) string {
	switch mode {
	case project.ModeFast:
		return "Austin implements; Tony verifies before Duo lands"
	case project.ModeStep:
		return "Dual-approved queue; each step runs FAST or GOAL"
	default:
		return "Both agents plan, implement and cross-review"
	}
}

func (p *modePicker) Draw(buf *ui.Buffer) {
	fillBlock(buf, p.GetRect(), ui.Style{Fg: p.theme.Fg, Bg: p.theme.PanelHigh})
	p.Title = " " + p.theme.tr("MODE") + " "
	p.Block.Draw(buf)
	if p.Inner.Dx() <= 2 || p.Inner.Dy() < 5 {
		return
	}
	x := p.Inner.Min.X
	y := p.Inner.Min.Y
	line := func(text string, style ui.Style) {
		if y >= p.Inner.Max.Y {
			return
		}
		buf.SetString(fitText(text, p.Inner.Dx()), style, image.Pt(x, y))
		y++
	}

	current := p.current
	if current == "" {
		current = project.ModeFast
	}
	line(p.theme.tr("current")+"  "+current.Display(), p.theme.Subtle)
	if y < p.Inner.Max.Y {
		for xx := p.Inner.Min.X; xx < p.Inner.Max.X; xx++ {
			buf.SetCell(ui.NewCell('─', ui.Style{Fg: p.theme.Border, Bg: p.theme.Panel}), image.Pt(xx, y))
		}
		y++
	}

	p.clamp()
	for i, mode := range pickerModes {
		mark := "  "
		if mode == p.current {
			mark = "● "
		}
		style := p.theme.Body
		switch {
		case i == p.cursor:
			style = ui.Style{Fg: p.theme.Bg, Bg: p.theme.Accent, Modifier: ui.ModifierBold}
			fillBlock(buf, image.Rect(x, y, p.Inner.Max.X, y+1), style)
		case mode == p.current:
			style = p.theme.Success
		}
		text := mark + fmt.Sprintf("%-6s", mode.Display()) + "  " + p.theme.tr(modePurpose(mode))
		line(text, style)
	}

	hint := p.theme.tr("↑/↓ choose · Enter switch · Esc cancel")
	buf.SetString(fitText(hint, p.Inner.Dx()), p.theme.Timestamp, image.Pt(p.Inner.Min.X, p.Inner.Max.Y-1))
}
