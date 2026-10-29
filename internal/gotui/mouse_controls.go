package gotui

import (
	"image"
	"strings"

	"github.com/atfa/duo/internal/protocol"
	"github.com/gdamore/tcell/v3"
	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui/v5"
)

func modalCloseRect(b *ui.Block) image.Rectangle {
	r := b.GetRect()
	if r.Dx() < 6 || r.Dy() < 3 {
		return image.Rectangle{}
	}
	return image.Rect(r.Max.X-4, r.Min.Y, r.Max.X-1, r.Min.Y+1)
}
func drawModalClose(buf *ui.Buffer, b *ui.Block, t theme) {
	r := modalCloseRect(b)
	if !r.Empty() {
		buf.SetString("[×]", t.Subtle, r.Min)
	}
}
func (a *App) modalBlock() *ui.Block {
	switch a.overlay {
	case overlayModePicker:
		return a.modePicker.Block
	case overlayConfirm:
		return a.confirm.Block
	case overlayPicker:
		return a.picker.Block
	case overlayDrivers:
		return a.drivers.Block
	case overlaySessions:
		return a.sessions.Block
	case overlayObserver:
		return a.observer.Block
	case overlayHelp:
		return a.help.Block
	case overlayDetails:
		return a.details.Block
	case overlayPlan:
		return a.plan.Block
	case overlayCost:
		return a.cost.Block
	case overlayQrcode:
		return a.qr.Block
	case overlayObserverReports:
		return a.observerReports.Block
	}
	return nil
}

// mouseOverlay routes visible choices through the same handlers as keyboard input.
func (a *App) mouseOverlay(x, y int) {
	point := image.Pt(x, y)
	if b := a.modalBlock(); b != nil && point.In(modalCloseRect(b)) {
		if a.overlay == overlayObserverReports {
			a.closeOverlay()
			return
		}
		a.handleKey(tcell.NewEventKey(tcell.KeyEsc, "", tcell.ModNone))
		if a.overlay == overlayCost {
			a.closeOverlay()
		}
		return
	}
	enter := tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone)
	tab := tcell.NewEventKey(tcell.KeyTab, "", tcell.ModNone)
	switch a.overlay {
	case overlayModePicker:
		p := a.modePicker
		row := y - (p.Inner.Min.Y + 2)
		if point.In(p.Inner) && row >= 0 && row < len(pickerModes) {
			p.cursor = row
			a.handleModePickerKey(enter)
		}
	case overlayConfirm:
		c := a.confirm
		if y != c.Inner.Max.Y-1 || !point.In(c.Inner) {
			return
		}
		w2 := rw.StringWidth("  " + c.theme.tr(c.choice2) + "  ")
		w1 := rw.StringWidth("  " + c.theme.tr(c.choice1) + "  ")
		left := max(c.Inner.Min.X, c.Inner.Max.X-w2-1-w1)
		if x >= left && x < left+w2 {
			c.active = 0
			a.handleConfirmKey(enter)
		} else if x >= left+w2+1 && x < left+w2+1+w1 {
			c.active = 1
			a.handleConfirmKey(enter)
		}
	case overlayPicker:
		p := a.picker
		if !point.In(p.Inner) {
			return
		}
		if y == p.Inner.Min.Y {
			agentX := p.Inner.Min.X + rw.StringWidth(p.theme.tr("agent")+" ")
			target := protocol.AgentID("")
			if x >= agentX && x < agentX+rw.StringWidth("  Austin  ") {
				target = protocol.Austin
			} else if x >= agentX+rw.StringWidth("  Austin  ") && x < agentX+rw.StringWidth("  Austin  ")+rw.StringWidth("  Tony  ") {
				target = protocol.Tony
			}
			if target != "" && target != p.agent {
				a.handlePickerKey(tab)
			} else if x >= agentX+rw.StringWidth("  Austin  ")+rw.StringWidth("  Tony  ")+1 && x < agentX+rw.StringWidth("  Austin  ")+rw.StringWidth("  Tony  ")+11 {
				a.handlePickerKey(tcell.NewEventKey(tcell.KeyCtrlT, "", tcell.ModNone))
			}
			return
		}
		if p.tab == 0 {
			row := y - (p.Inner.Min.Y + 3)
			if p.loading {
				row--
			}
			if p.err != "" {
				row--
			}
			idx := p.offset + row
			if row >= 0 && row < p.visibleRows() && idx < len(p.filtered()) {
				p.cursor = idx
				a.handlePickerKey(enter)
			}
		} else if y == p.Inner.Max.Y-1 {
			a.handlePickerKey(enter)
		}
	case overlayDrivers:
		p := a.drivers
		if !point.In(p.Inner) {
			return
		}
		if y == p.Inner.Min.Y {
			agentX := p.Inner.Min.X + rw.StringWidth(p.theme.tr("agent")+" ")
			target := protocol.AgentID("")
			if x >= agentX && x < agentX+rw.StringWidth("  Austin  ") {
				target = protocol.Austin
			} else if x >= agentX+rw.StringWidth("  Austin  ") && x < agentX+rw.StringWidth("  Austin  ")+rw.StringWidth("  Tony  ") {
				target = protocol.Tony
			}
			if target != "" && target != p.agent {
				a.handleDriverPickerKey(tab)
			}
			return
		}
		row := y - (p.Inner.Min.Y + 3)
		if p.loading {
			row--
		}
		if p.err != "" {
			row--
		}
		idx := p.offset + row
		if row >= 0 && row < p.visibleRows() && idx < len(p.filtered()) {
			p.cursor = idx
			a.handleDriverPickerKey(enter)
		}
	case overlaySessions:
		p := a.sessions
		row := y - p.Inner.Min.Y - 1
		idx := p.offset + row
		if point.In(p.Inner) && row >= 0 && row < p.rows() && idx < len(p.items) {
			p.cursor = idx
			a.handleSessionsKey(enter)
		}
	case overlayObserverReports:
		o := a.observerReports
		if point.In(o.Inner) && y == o.Inner.Max.Y-1 {
			if o.asking {
				if x < o.Inner.Min.X+6 {
					a.handleObserverReportKey(enter)
				} else if x >= o.Inner.Min.X+7 && x < o.Inner.Min.X+15 {
					a.handleObserverReportKey(tcell.NewEventKey(tcell.KeyEsc, "", tcell.ModNone))
				}
				return
			}
			if x < o.Inner.Min.X+12 {
				a.requestObserverReport()
			} else if x < o.Inner.Min.X+29 {
				o.beginQuestion()
			}
		}
	case overlayObserver:
		o := a.observer
		if point.In(o.Inner) && y == o.Inner.Max.Y-1 {
			if x >= o.Inner.Min.X && x < o.Inner.Min.X+6 {
				a.handleObserverConfigKey(enter)
			} else if x >= o.Inner.Min.X+8 && x < o.Inner.Min.X+16 {
				a.handleObserverConfigKey(tcell.NewEventKey(tcell.KeyEsc, "", tcell.ModNone))
			}
			return
		}
		row := y - o.Inner.Min.Y - 2
		if point.In(o.Inner) && row >= 0 && row <= observerEnabledField {
			o.active = row
			if row == observerEnabledField {
				o.enabled = !o.enabled
			} else if field := o.field(); field != nil {
				o.cursor = len(*field)
			}
		}
	}
}

func (a *App) mouseMenus(x, y int) bool {
	point := image.Pt(x, y)
	if a.slashOpen() && point.In(a.slash.Inner) {
		row := y - a.slash.Inner.Min.Y
		idx := a.slash.offset + row
		if row >= 0 && row < max(1, a.slash.Inner.Dy()-1) && idx < len(a.slash.commands) {
			a.slash.cursor = idx
			a.selectSlash()
		}
		return true
	}
	if a.mentionOpen() && point.In(a.fileMenu.Inner) {
		row := y - a.fileMenu.Inner.Min.Y
		idx := a.fileMenu.offset + row
		if row >= 0 && row < max(1, a.fileMenu.Inner.Dy()-1) && idx < len(a.fileMenu.matches) {
			a.fileMenu.cursor = idx
			a.acceptMention()
		}
		return true
	}
	return false
}

func (a *App) mouseFooter(x, y int) bool {
	f := a.footer
	if f.status != "" || y != f.Min.Y || x < f.Min.X+1 {
		return false
	}
	pos := f.Min.X + 1
	for _, part := range strings.Split(f.hint, " · ") {
		end := pos + rw.StringWidth(part)
		if x >= pos && x < end {
			var key tcell.Key
			var str string
			mod := tcell.ModNone
			switch {
			case strings.Contains(part, "Enter send"):
				key = tcell.KeyEnter
			case strings.Contains(part, "Ctrl+E"):
				key = tcell.KeyCtrlE
			case strings.Contains(part, "Ctrl+S"):
				key = tcell.KeyCtrlS
			case strings.Contains(part, "Alt+M"):
				key = tcell.KeyRune
				str = "m"
				mod = tcell.ModAlt
			case strings.Contains(part, "F1"):
				key = tcell.KeyF1
			case strings.Contains(part, "F2"):
				key = tcell.KeyF2
			case strings.Contains(part, "Ctrl+Q"):
				key = tcell.KeyCtrlQ
			default:
				return false
			}
			a.handleKey(tcell.NewEventKey(key, str, mod))
			return true
		}
		pos = end + rw.StringWidth(" · ")
	}
	return false
}
func (c *composer) click(x, y int) bool {
	if !image.Pt(x, y).In(c.Inner) {
		return false
	}
	rows := c.visualRows(c.textWidth())
	idx := c.scroll + y - c.Inner.Min.Y
	if idx < 0 || idx >= len(rows) {
		return true
	}
	r := rows[idx]
	c.row = r.row
	c.col = r.start
	cells := max(0, x-c.Inner.Min.X-2)
	for c.col < r.end {
		w := max(0, rw.RuneWidth(c.lines[c.row][c.col]))
		if w > cells {
			break
		}
		cells -= w
		c.col++
	}
	c.goalCol = -1
	return true
}
