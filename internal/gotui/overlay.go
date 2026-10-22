package gotui

import (
	"image"
	"strings"

	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui/v5"
)

type overlayKind int

const (
	overlayNone overlayKind = iota
	overlayPicker
	overlayDrivers
	overlayHelp
	overlayDetails
	overlayConfirm
	overlayObserver
	overlayObserverReports
)

func newModalBlock(t theme) *ui.Block {
	b := ui.NewBlock()
	b.BorderRounded = true
	b.BorderStyle = ui.Style{Fg: t.Border, Bg: t.PanelHigh}
	b.BackgroundColor = t.Panel
	b.TitleStyle = ui.Style{Fg: t.Accent, Bg: t.Panel, Modifier: ui.ModifierBold}
	b.PaddingLeft, b.PaddingRight = 1, 1
	return b
}

func centered(l layout, width, height int) image.Rectangle {
	top, bottom := l.main.Min.Y, l.main.Max.Y
	if bottom <= top {
		top, bottom = 0, l.height
	}
	if width > l.width-2 {
		width = l.width - 2
	}
	if height > bottom-top-2 {
		height = bottom - top - 2
	}
	if width < 12 {
		width = max(1, l.width)
	}
	if height < 3 {
		height = max(1, bottom-top)
	}
	x := (l.width - width) / 2
	y := top + (bottom-top-height)/2
	if x < 0 {
		x = 0
	}
	if y < top {
		y = max(0, top)
	}
	return image.Rect(x, y, x+width, y+height)
}

// confirmOverlay is used for quit and destructive choices. A modal that can
// silently discard work would defeat the point of the session journal, so the
// default focus is always Cancel.
type confirmOverlay struct {
	*ui.Block
	theme   theme
	title   string
	text    string
	choice1 string
	choice2 string
	active  int
	onYes   func()
	box     image.Rectangle
}

func (c *confirmOverlay) place(l layout) {
	c.box = centered(l, 68, 7)
	c.Block.SetRect(c.box.Min.X, c.box.Min.Y, c.box.Max.X, c.box.Max.Y)
	c.Title = " " + c.title + " "
}

func (c *confirmOverlay) Draw(buf *ui.Buffer) {
	fillBlock(buf, c.GetRect(), ui.Style{Fg: c.theme.Fg, Bg: c.theme.PanelHigh})
	c.Title = " " + c.theme.tr(c.title) + " "
	c.Block.Draw(buf)
	width := c.Inner.Dx()
	y := c.Inner.Min.Y
	lines := wrapText(c.text, c.theme.BodyMuted, max(1, width))
	for i, line := range lines {
		if y+i >= c.Inner.Max.Y-2 {
			break
		}
		buf.SetString(fitText(line.text(), width), c.theme.BodyMuted, image.Pt(c.Inner.Min.X, y+i))
	}
	y = c.Inner.Max.Y - 1
	drawChoice := func(label string, active bool, x int) int {
		st := ui.Style{Fg: c.theme.Muted, Bg: c.theme.Panel}
		if active {
			st = ui.Style{Fg: c.theme.Bg, Bg: c.theme.Accent, Modifier: ui.ModifierBold}
		}
		text := "  " + c.theme.tr(label) + "  "
		buf.SetString(text, st, image.Pt(x, y))
		return x + rw.StringWidth(text)
	}
	choiceWidth := func(label string) int {
		return rw.StringWidth("  " + c.theme.tr(label) + "  ")
	}
	buttonsWidth := choiceWidth(c.choice2) + 1 + choiceWidth(c.choice1)
	x := max(c.Inner.Min.X, c.Inner.Max.X-buttonsWidth)
	x = drawChoice(c.choice2, c.active == 0, x)
	x++
	drawChoice(c.choice1, c.active == 1, x)
	buf.SetString(fitText(c.theme.tr("←/→ choose  ·  Enter select  ·  Esc cancel"), width), c.theme.Timestamp, image.Pt(c.Inner.Min.X, c.Inner.Max.Y-2))
}

// helpOverlay is a scrollable guide. Content is rebuilt on width changes, so a
// resize does not leave half-wrapped instructions behind.
type helpOverlay struct {
	*ui.Block
	theme  theme
	offset int
	lines  []renderLine
	box    image.Rectangle
	width  int
}

func newHelp(t theme) *helpOverlay {
	h := &helpOverlay{Block: newModalBlock(t), theme: t}
	h.Title = " HELP "
	return h
}

func (h *helpOverlay) place(l layout) {
	h.box = centered(l, 86, 30)
	h.Block.SetRect(h.box.Min.X, h.box.Min.Y, h.box.Max.X, h.box.Max.Y)
	if h.width != h.Inner.Dx() {
		h.width = h.Inner.Dx()
		h.rebuild()
	}
}

// helpSection is one titled block of the /help guide. Lines are translated by
// their exact text, so the Chinese table has to cover every one of them.
type helpSection struct {
	title string
	lines []string
}

// helpSections is the /help content. The slash list is generated from
// slashCommands so the guide cannot omit a command the prompt accepts.
func helpSections() []helpSection {
	commands := make([]string, 0, len(slashCommands))
	for _, cmd := range slashCommands {
		line := cmd.name
		if cmd.args != "" {
			line += " " + cmd.args
		}
		commands = append(commands, line)
	}
	return []helpSection{
		{"WORKFLOW", []string{
			"FAST: RUNNING → VERIFY → DONE. Austin implements, Tony verifies, Duo lands the diff.",
			"GOAL: PLAN → EXECUTE → REVIEW → INTEGRATE → DONE. Both agents agree on the plan first.",
			"STEP: plan a dual-approved queue, then run each step as FAST (Austin implements, Tony verifies) or GOAL (both implement and cross-sign).",
		}},
		{"KEYS", []string{
			"Enter send  ·  Ctrl+Enter / Shift+Enter / Alt+Enter / Ctrl+J newline",
			"Ctrl+M / Alt+M model & thinking picker  ·  F1 / Ctrl+/ / Alt+H help  ·  F2 split / timeline",
			"Ctrl+E observer status report  ·  /observer opens observer settings",
			"In picker: Tab switches Agent  ·  Shift+Tab cycles thinking  ·  Space selects model",
			"Ctrl+A / Alt+A attach Austin  ·  Ctrl+T / Alt+T attach Tony  ·  Ctrl+V image path  ·  Ctrl+] or Ctrl+\\ detach",
			"Ctrl+R restart Austin  ·  Ctrl+Y restart Tony  ·  Ctrl+O / Alt+O details & changes",
			"Ctrl+X copy Austin's latest  ·  Ctrl+P sidebar  ·  Ctrl+G timestamps  ·  Ctrl+L redraw",
			"Ctrl+K / Ctrl+U / Ctrl+W delete to end / to start / word  ·  ↑/↓ history  ·  PgUp/PgDn scroll  ·  Ctrl+Q / Ctrl+D quit",
			"←/→ cursor  ·  Alt+←/→ by word  ·  Home/End line start/end  ·  Backspace/Delete remove before/after cursor",
			"@ completes a tracked file  ·  Tab autocompletes slash commands or files  ·  Esc closes dialogs",
		}},
		{"PAUSE & DRIVERS", []string{
			"/pause waits for both agents to finish their current turn, then holds new work.",
			"While pausing, /unpause cancels the request; once paused, /continue resumes work.",
			"Use /driver before the first task, while fully paused, or after DONE. The new",
			"driver starts idle; /continue resumes a paused session.",
			"A resume sends nothing; /resume wakes the agents, and a new task wakes them too.",
		}},
		{"SLASH COMMANDS", append([]string{
			"Type / to see commands and descriptions; Tab completes the selected command.",
		}, chunkJoin(commands, 3)...)},
		{"NATIVE TERMINALS", []string{
			"Attach suspends the dashboard and hands the real PTY to the agent. The",
			"terminal is restored on detach; paste images directly there when Ctrl+V",
			"has no clipboard tool. Use Ctrl+] (or Ctrl+\\, Ctrl+】) to return.",
		}},
		{"SESSION", []string{
			"Every visible message is recorded through the session journal, so resume",
			"restores the same conversation when the session resumes.",
		}},
	}
}

// chunkJoin packs items into lines of at most perLine, joined by the separator
// the help guide uses between entries.
func chunkJoin(items []string, perLine int) []string {
	lines := make([]string, 0, (len(items)+perLine-1)/perLine)
	for i := 0; i < len(items); i += perLine {
		lines = append(lines, strings.Join(items[i:min(i+perLine, len(items))], " · "))
	}
	return lines
}

func (h *helpOverlay) rebuild() {
	var lines []renderLine
	for _, s := range helpSections() {
		lines = append(lines, makeLine("", h.theme.Body, 0))
		lines = append(lines, makeLine(h.theme.tr(s.title), ui.Style{Fg: h.theme.Accent, Bg: h.theme.Panel, Modifier: ui.ModifierBold}, 0))
		for _, text := range s.lines {
			wrapped := wrapText(h.theme.tr(text), h.theme.BodyMuted, max(1, h.Inner.Dx()-2))
			for _, line := range wrapped {
				line.runs = append([]textRun{{"  ", h.theme.BodyMuted, linkTarget{}}}, line.runs...)
				lines = append(lines, line)
			}
		}
	}
	h.lines = lines
}

func (h *helpOverlay) Draw(buf *ui.Buffer) {
	h.DrawBase(buf)
}

func (h *helpOverlay) DrawBase(buf *ui.Buffer) {
	fillBlock(buf, h.GetRect(), ui.Style{Fg: h.theme.Fg, Bg: h.theme.PanelHigh})
	h.Title = " " + h.theme.tr("HELP") + " "
	h.Block.Draw(buf)
	height := max(0, h.Inner.Dy()-1)
	for i := 0; i < height; i++ {
		idx := h.offset + i
		if idx >= len(h.lines) {
			break
		}
		x := h.Inner.Min.X
		for _, run := range h.lines[idx].runs {
			buf.SetString(run.text, run.style, image.Pt(x, h.Inner.Min.Y+i))
			x += rw.StringWidth(run.text)
		}
	}
	hint := h.theme.tr("↑/↓ scroll  ·  Esc close")
	if h.Inner.Dy() > 0 {
		buf.SetString(fitText(hint, h.Inner.Dx()), h.theme.Timestamp, image.Pt(h.Inner.Min.X, h.Inner.Max.Y-1))
	}
}

func (h *helpOverlay) Scroll(delta int) {
	h.offset += delta
	maxOffset := max(0, len(h.lines)-max(1, h.Inner.Dy()-1))
	if h.offset > maxOffset {
		h.offset = maxOffset
	}
	if h.offset < 0 {
		h.offset = 0
	}
}

// detailOverlay shows the same facts as /status plus a diffstat. Both reads are
// asynchronous because they can wait on a driver or git.
type detailOverlay struct {
	*ui.Block
	theme   theme
	loading bool
	err     string
	status  string
	changes []string
	offset  int
	lines   []renderLine
	width   int
	box     image.Rectangle
}

func newDetails(t theme) *detailOverlay {
	d := &detailOverlay{Block: newModalBlock(t), theme: t}
	d.Title = " SESSION DETAILS "
	return d
}

func (d *detailOverlay) place(l layout) {
	d.box = centered(l, 92, 22)
	d.Block.SetRect(d.box.Min.X, d.box.Min.Y, d.box.Max.X, d.box.Max.Y)
	if d.width != d.Inner.Dx() {
		d.width = d.Inner.Dx()
		d.rebuild()
	}
	height := min(22, max(9, len(d.lines)+3))
	if height != d.box.Dy() {
		d.box = centered(l, 92, height)
		d.Block.SetRect(d.box.Min.X, d.box.Min.Y, d.box.Max.X, d.box.Max.Y)
	}
}

func (d *detailOverlay) begin() {
	d.loading = true
	d.err = ""
	d.status = ""
	d.changes = nil
	d.offset = 0
	d.rebuild()
}

func (d *detailOverlay) loaded(payload any, err error) {
	d.loading = false
	if err != nil {
		d.err = err.Error()
	}
	if p, ok := payload.(detailPayload); ok {
		d.status = p.status
		d.changes = p.changes
	}
	d.rebuild()
}

type detailPayload struct {
	status  string
	changes []string
}

func (d *detailOverlay) rebuild() {
	d.lines = nil
	add := func(text string, style ui.Style) {
		wrapped := wrapText(text, style, max(1, d.Inner.Dx()))
		d.lines = append(d.lines, wrapped...)
	}
	add(d.theme.tr("SESSION STATUS"), ui.Style{Fg: d.theme.Accent, Bg: d.theme.Panel, Modifier: ui.ModifierBold})
	if d.loading {
		add(d.theme.tr("loading…"), d.theme.Subtle)
	}
	if d.err != "" {
		add("error: "+d.err, d.theme.Error)
	}
	for _, line := range strings.Split(strings.TrimRight(d.status, "\n"), "\n") {
		add(line, d.theme.Body)
	}
	d.lines = append(d.lines, renderLine{})
	add(d.theme.tr("CHANGES"), ui.Style{Fg: d.theme.Accent, Bg: d.theme.Panel, Modifier: ui.ModifierBold})
	if len(d.changes) == 0 {
		add(d.theme.tr("working tree clean"), d.theme.Subtle)
	} else {
		for _, line := range d.changes {
			st := d.theme.Body
			if strings.HasPrefix(strings.TrimSpace(line), "D") {
				st = d.theme.Warning
			}
			add(line, st)
		}
	}
}

func (d *detailOverlay) Draw(buf *ui.Buffer) {
	fillBlock(buf, d.GetRect(), ui.Style{Fg: d.theme.Fg, Bg: d.theme.PanelHigh})
	d.Title = " " + d.theme.tr("SESSION DETAILS") + " "
	d.Block.Draw(buf)
	height := max(0, d.Inner.Dy()-1)
	for i := 0; i < height; i++ {
		idx := d.offset + i
		if idx >= len(d.lines) {
			break
		}
		x := d.Inner.Min.X
		for _, run := range d.lines[idx].runs {
			buf.SetString(run.text, run.style, image.Pt(x, d.Inner.Min.Y+i))
			x += rw.StringWidth(run.text)
		}
	}
	buf.SetString(fitText(d.theme.tr("↑/↓ scroll  ·  Esc close"), d.Inner.Dx()), d.theme.Timestamp, image.Pt(d.Inner.Min.X, d.Inner.Max.Y-1))
}

func (d *detailOverlay) Scroll(delta int) {
	d.offset += delta
	maxOffset := max(0, len(d.lines)-max(1, d.Inner.Dy()-1))
	if d.offset > maxOffset {
		d.offset = maxOffset
	}
	if d.offset < 0 {
		d.offset = 0
	}
}
