package gotui

import (
	"fmt"
	"image"
	"strings"

	"github.com/atfa/duo/internal/models"
	"github.com/atfa/duo/internal/protocol"
	ui "github.com/metaspartan/gotui/v5"
)

// modelPicker is the only place a human changes what an agent runs. The catalog
// is loaded asynchronously by App, because asking a driver can start a CLI.
type modelPicker struct {
	*ui.Block
	theme  theme
	agent  protocol.AgentID
	tab    int // 0 models, 1 thinking
	filter []rune
	cursor int
	offset int

	catalogAgent protocol.AgentID
	all          []models.Model
	loadedFlag   bool
	loading      bool
	err          string

	current  string
	thinking string
	box      image.Rectangle
}

func newModelPicker(t theme) *modelPicker {
	p := &modelPicker{Block: newModalBlock(t), theme: t, agent: protocol.Austin}
	p.Title = " MODELS & THINKING "
	return p
}

func (p *modelPicker) place(l layout) {
	height := 9
	if n := len(p.filtered()); n > 0 {
		height = min(24, n+7)
	}
	p.box = centered(l, 78, height)
	p.Block.SetRect(p.box.Min.X, p.box.Min.Y, p.box.Max.X, p.box.Max.Y)
	p.clamp()
}

// begin resets the transient query and reports whether the catalog for the
// target still needs to be fetched.
func (p *modelPicker) begin(agent protocol.AgentID, current, thinking string) bool {
	p.agent = agent
	p.current = current
	p.thinking = thinking
	p.filter = nil
	p.cursor = 0
	p.offset = 0
	p.tab = 0
	if p.catalogAgent != agent {
		p.catalogAgent = agent
		p.all = nil
		p.loadedFlag = false
		p.err = ""
		p.loading = false
	}
	return !p.loadedFlag && !p.loading
}

func (p *modelPicker) beginLoad() {
	p.loading = true
	p.err = ""
}

func (p *modelPicker) loaded(agent protocol.AgentID, payload any, err error) {
	if agent != p.agent {
		return
	}
	p.loading = false
	if err != nil {
		p.err = err.Error()
		return
	}
	list, _ := payload.([]models.Model)
	p.all = list
	p.loadedFlag = true
	p.err = ""
	p.selectCurrent()
	p.clamp()
}

func (p *modelPicker) filtered() []models.Model {
	q := strings.ToLower(strings.TrimSpace(string(p.filter)))
	if q == "" {
		return p.all
	}
	var out []models.Model
	for _, m := range p.all {
		if strings.Contains(strings.ToLower(m.Reference()), q) {
			out = append(out, m)
		}
	}
	return out
}

func (p *modelPicker) clamp() {
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

func (p *modelPicker) visibleRows() int {
	return max(1, p.Inner.Dy()-5)
}

func (p *modelPicker) move(delta int) {
	p.cursor += delta
	p.clamp()
}

func (p *modelPicker) addFilter(r rune) {
	p.filter = append(p.filter, r)
	p.cursor, p.offset = 0, 0
	p.clamp()
}

func (p *modelPicker) backspaceFilter() {
	if len(p.filter) > 0 {
		p.filter = p.filter[:len(p.filter)-1]
		p.cursor, p.offset = 0, 0
		p.clamp()
	}
}

func (p *modelPicker) selected() (models.Model, bool) {
	list := p.filtered()
	if p.cursor < 0 || p.cursor >= len(list) {
		return models.Model{}, false
	}
	return list[p.cursor], true
}

func (p *modelPicker) selectCurrent() {
	list := p.filtered()
	for i, m := range list {
		if m.Reference() == p.current {
			p.cursor = i
			p.clamp()
			return
		}
	}
}

func (p *modelPicker) switchAgent() protocol.AgentID {
	if p.agent == protocol.Austin {
		p.agent = protocol.Tony
	} else {
		p.agent = protocol.Austin
	}
	p.filter = nil
	p.cursor, p.offset = 0, 0
	if p.catalogAgent != p.agent {
		p.catalogAgent = p.agent
		p.all = nil
		p.loadedFlag = false
		p.loading = false
		p.err = ""
	}
	p.clamp()
	return p.agent
}

func (p *modelPicker) Draw(buf *ui.Buffer) {
	fillBlock(buf, p.GetRect(), ui.Style{Fg: p.theme.Fg, Bg: p.theme.PanelHigh})
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

	// Target row.
	austin := "  Austin  "
	tony := "  Tony  "
	aStyle := ui.Style{Fg: p.theme.Muted, Bg: p.theme.Panel}
	tStyle := aStyle
	if p.agent == protocol.Austin {
		aStyle = ui.Style{Fg: p.theme.Bg, Bg: p.theme.Austin, Modifier: ui.ModifierBold}
	} else {
		tStyle = ui.Style{Fg: p.theme.Bg, Bg: p.theme.Tony, Modifier: ui.ModifierBold}
	}
	buf.SetString("agent ", p.theme.Subtle, image.Pt(x, y))
	buf.SetString(austin, aStyle, image.Pt(x+6, y))
	buf.SetString(tony, tStyle, image.Pt(x+6+len([]rune(austin)), y))
	buf.SetString("   Tab switch agent", p.theme.Timestamp, image.Pt(x+6+len([]rune(austin))+len([]rune(tony)), y))
	y++

	if p.tab == 0 {
		filter := string(p.filter)
		label := "search  "
		if filter == "" {
			label += "all models"
		} else {
			label += filter
		}
		line(label, p.theme.BodyMuted)
	} else {
		line("thinking level  ·  Enter advances · shifts the driver's reasoning effort", p.theme.BodyMuted)
	}
	if y < p.Inner.Max.Y {
		for xx := p.Inner.Min.X; xx < p.Inner.Max.X; xx++ {
			buf.SetCell(ui.NewCell('─', ui.Style{Fg: p.theme.Border, Bg: p.theme.Panel}), image.Pt(xx, y))
		}
		y++
	}
	rows := max(1, p.Inner.Max.Y-y-2)
	if p.tab == 1 {
		levels := []string{"off", "low", "medium", "high", "xhigh"}
		current := strings.ToLower(strings.TrimSpace(p.thinking))
		if current == "" {
			current = "default"
		}
		line("current  "+current, ui.Style{Fg: p.theme.Accent, Bg: p.theme.Panel, Modifier: ui.ModifierBold})
		for _, level := range levels {
			st := p.theme.BodyMuted
			mark := "  "
			if level == current {
				st = p.theme.Success
				mark = "● "
			}
			line(mark+level, st)
		}
		line("Enter advances one level (some drivers restart to apply it).", p.theme.Timestamp)
	} else {
		if p.loading {
			line(spinnerFrame(len(p.filter))+" reading "+string(p.agent)+" model catalog…", p.theme.Warning)
		}
		if p.err != "" {
			line("catalog error: "+p.err, p.theme.Error)
		}
		list := p.filtered()
		if !p.loading && len(list) == 0 && p.err == "" {
			line("no models reported by this driver", p.theme.Subtle)
		}
		p.clamp()
		for i := 0; i < rows; i++ {
			idx := p.offset + i
			if idx >= len(list) {
				break
			}
			m := list[idx]
			mark := "  "
			if m.Reference() == p.current {
				mark = "● "
			}
			suffix := ""
			if m.Thinking {
				suffix += "  · thinking"
			}
			if m.Images {
				suffix += "  · images"
			}
			text := mark + m.Reference() + suffix
			st := p.theme.Body
			if idx == p.cursor {
				st = ui.Style{Fg: p.theme.Bg, Bg: p.theme.Accent, Modifier: ui.ModifierBold}
			} else if m.Reference() == p.current {
				st = p.theme.Success
			}
			buf.SetString(strings.Repeat(" ", p.Inner.Dx()), st, image.Pt(x, y))
			buf.SetString(fitText(text, p.Inner.Dx()), st, image.Pt(x, y))
			y++
		}
	}
	bottom := p.Inner.Max.Y - 1
	hint := "Enter apply · Space keep open · Ctrl+T thinking · Esc close"
	if p.tab == 1 {
		hint = "Enter advance thinking · Ctrl+T models · Esc close"
	}
	buf.SetString(fitText(hint, p.Inner.Dx()), p.theme.Timestamp, image.Pt(p.Inner.Min.X, bottom))
	if len(p.all) > 0 && p.Inner.Dx() > 55 {
		count := fmt.Sprintf("%d models", len(p.filtered()))
		buf.SetString(fitText(count, 12), p.theme.Timestamp, image.Pt(max(p.Inner.Min.X, p.Inner.Max.X-12), p.Inner.Min.Y))
	}
}
