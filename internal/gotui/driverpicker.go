package gotui

import (
	"fmt"
	"image"
	"sort"
	"strings"

	"github.com/atfa/duo/internal/protocol"
	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui/v5"
)

// driverPicker selects the executable behind one agent. It is opened only while
// the session is fully paused, because replacing a driver restarts its process
// and must never interrupt a turn.
//
// It deliberately mirrors modelPicker: the same modal shape, agent tabs, typing
// filter and current marker, so switching a driver feels like switching a model.
// The catalog comes from the session, which owns driver discovery.
type driverPicker struct {
	*ui.Block
	theme  theme
	agent  protocol.AgentID
	filter []rune
	cursor int
	offset int

	names   []string
	loaded  bool
	loading bool
	err     string
	current string
	box     image.Rectangle
}

func newDriverPicker(t theme) *driverPicker {
	p := &driverPicker{Block: newModalBlock(t), theme: t, agent: protocol.Austin}
	p.Title = " DRIVERS "
	return p
}

func (p *driverPicker) place(l layout) {
	height := 9
	if n := len(p.filtered()); n > 0 {
		height = min(24, n+7)
	}
	p.box = centered(l, 70, height)
	p.Block.SetRect(p.box.Min.X, p.box.Min.Y, p.box.Max.X, p.box.Max.Y)
	p.clamp()
}

// begin resets the transient query for one agent and reports whether the driver
// catalog still needs to be discovered.
func (p *driverPicker) begin(agent protocol.AgentID, current string) bool {
	p.agent = agent
	p.current = current
	p.filter = nil
	p.cursor = 0
	p.offset = 0
	p.clamp()
	return !p.loaded && !p.loading
}

func (p *driverPicker) beginLoad() {
	p.loading = true
	p.err = ""
}

// setNames installs the discovered driver names. They are sorted here rather
// than trusted to arrive sorted, so the list cannot depend on directory order.
func (p *driverPicker) setNames(names []string) {
	unique := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || unique[name] {
			continue
		}
		unique[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	p.names = out
	p.loaded = true
	p.loading = false
	p.err = ""
	p.selectCurrent()
	p.clamp()
}

func (p *driverPicker) fail(err error) {
	p.loading = false
	if err != nil {
		p.err = err.Error()
	}
}

func (p *driverPicker) setCurrent(current string) {
	p.current = current
	p.selectCurrent()
	p.clamp()
}

func (p *driverPicker) filtered() []string {
	q := strings.ToLower(strings.TrimSpace(string(p.filter)))
	if q == "" {
		return p.names
	}
	var out []string
	for _, name := range p.names {
		if strings.Contains(strings.ToLower(name), q) {
			out = append(out, name)
		}
	}
	return out
}

func (p *driverPicker) clamp() {
	list := p.filtered()
	if p.cursor >= len(list) {
		p.cursor = len(list) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
	rows := max(1, p.visibleRows())
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+rows {
		p.offset = p.cursor - rows + 1
	}
	if maxOffset := max(0, len(list)-rows); p.offset > maxOffset {
		p.offset = maxOffset
	}
	if p.offset < 0 {
		p.offset = 0
	}
}

func (p *driverPicker) visibleRows() int {
	return max(1, p.Inner.Dy()-5)
}

func (p *driverPicker) move(delta int) {
	p.cursor += delta
	p.clamp()
}

func (p *driverPicker) addFilter(r rune) {
	p.filter = append(p.filter, r)
	p.cursor, p.offset = 0, 0
	p.clamp()
}

func (p *driverPicker) backspaceFilter() {
	if len(p.filter) > 0 {
		p.filter = p.filter[:len(p.filter)-1]
		p.cursor, p.offset = 0, 0
		p.clamp()
	}
}

func (p *driverPicker) selected() (string, bool) {
	list := p.filtered()
	if p.cursor < 0 || p.cursor >= len(list) {
		return "", false
	}
	return list[p.cursor], true
}

func (p *driverPicker) selectCurrent() {
	list := p.filtered()
	for i, name := range list {
		if name == p.current {
			p.cursor = i
			p.clamp()
			return
		}
	}
}

func (p *driverPicker) switchAgent() protocol.AgentID {
	if p.agent == protocol.Austin {
		p.agent = protocol.Tony
	} else {
		p.agent = protocol.Austin
	}
	p.filter = nil
	p.cursor, p.offset = 0, 0
	p.clamp()
	return p.agent
}

func (p *driverPicker) Draw(buf *ui.Buffer) {
	fillBlock(buf, p.GetRect(), ui.Style{Fg: p.theme.Fg, Bg: p.theme.PanelHigh})
	p.Title = " " + p.theme.tr("DRIVERS") + " "
	p.Block.Draw(buf)
	if p.Inner.Dx() <= 2 || p.Inner.Dy() < 6 {
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

	// Target row, identical to the model picker so both pickers read the same.
	austin := "  Austin  "
	tony := "  Tony  "
	aStyle := ui.Style{Fg: p.theme.Muted, Bg: p.theme.Panel}
	tStyle := aStyle
	if p.agent == protocol.Austin {
		aStyle = ui.Style{Fg: p.theme.Bg, Bg: p.theme.Austin, Modifier: ui.ModifierBold}
	} else {
		tStyle = ui.Style{Fg: p.theme.Bg, Bg: p.theme.Tony, Modifier: ui.ModifierBold}
	}
	agentLabel := p.theme.tr("agent") + " "
	buf.SetString(agentLabel, p.theme.Subtle, image.Pt(x, y))
	agentX := x + rw.StringWidth(agentLabel)
	buf.SetString(austin, aStyle, image.Pt(agentX, y))
	buf.SetString(tony, tStyle, image.Pt(agentX+rw.StringWidth(austin), y))
	buf.SetString("   "+p.theme.tr("Tab switch agent"), p.theme.Timestamp, image.Pt(agentX+rw.StringWidth(austin)+rw.StringWidth(tony), y))
	y++

	filter := string(p.filter)
	label := p.theme.tr("search") + "  "
	if filter == "" {
		label += p.theme.tr("all drivers")
	} else {
		label += filter
	}
	line(label, p.theme.BodyMuted)
	if y < p.Inner.Max.Y {
		for xx := p.Inner.Min.X; xx < p.Inner.Max.X; xx++ {
			buf.SetCell(ui.NewCell('─', ui.Style{Fg: p.theme.Border, Bg: p.theme.Panel}), image.Pt(xx, y))
		}
		y++
	}

	rows := max(1, p.Inner.Max.Y-y-2)
	if p.loading {
		line(spinnerFrame(len(p.filter))+" "+p.theme.tr("reading")+" "+p.theme.tr("drivers")+"…", p.theme.Warning)
	}
	if p.err != "" {
		line(p.theme.tr("catalog error:")+" "+p.err, p.theme.Error)
	}
	list := p.filtered()
	if !p.loading && len(list) == 0 && p.err == "" {
		line(p.theme.tr("no drivers found"), p.theme.Subtle)
	}
	p.clamp()
	for i := 0; i < rows; i++ {
		idx := p.offset + i
		if idx >= len(list) {
			break
		}
		name := list[idx]
		mark := "  "
		if name == p.current {
			mark = "● "
		}
		st := p.theme.Body
		if idx == p.cursor {
			st = ui.Style{Fg: p.theme.Bg, Bg: p.theme.Accent, Modifier: ui.ModifierBold}
		} else if name == p.current {
			st = p.theme.Success
		}
		text := mark + name
		buf.SetString(strings.Repeat(" ", p.Inner.Dx()), st, image.Pt(x, y))
		buf.SetString(fitText(text, p.Inner.Dx()), st, image.Pt(x, y))
		y++
	}

	bottom := p.Inner.Max.Y - 1
	hint := p.theme.tr("Tab agent · Enter apply · Esc close")
	buf.SetString(fitText(hint, p.Inner.Dx()), p.theme.Timestamp, image.Pt(p.Inner.Min.X, bottom))
	if len(p.names) > 0 && p.Inner.Dx() > 45 {
		count := fmt.Sprintf("%d %s", len(p.filtered()), p.theme.tr("drivers"))
		buf.SetString(fitText(count, 12), p.theme.Timestamp, image.Pt(max(p.Inner.Min.X, p.Inner.Max.X-12), p.Inner.Min.Y))
	}
}
