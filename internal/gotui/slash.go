package gotui

import (
	"image"
	"strings"

	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui/v5"
)

type slashCommand struct {
	name     string
	args     string
	desc     string
	needsArg bool
}

var slashCommands = []slashCommand{
	{name: "/escalate", args: "[reason]", desc: "Switch to Goal mode", needsArg: true},
	{name: "/mode", args: "[fast|goal|step]", desc: "Switch the session mode", needsArg: true},
	{name: "/model", desc: "Model & thinking picker (Ctrl+M)"},
	{name: "/observer", desc: "Configure the observer"},
	{name: "/reports", desc: "Show the observer's latest progress report (Ctrl+E)"},
	{name: "/pause", desc: "Pause after the current turns finish"},
	{name: "/unpause", desc: "Cancel a pending pause (use /continue once paused)"},
	{name: "/continue", desc: "Resume work after a full pause"},
	{name: "/driver", desc: "Driver picker (before the first task, while paused, or after DONE)"},
	{name: "/language", args: "[zh|en]", desc: "Set Duo's language and save it", needsArg: true},
	{name: "/copy", desc: "Copy Austin's latest message (Ctrl+X)"},
	{name: "/overview", desc: "Session overview and diffstat (Ctrl+O)"},
	{name: "/preview", desc: "Toggle the workflow sidebar (Ctrl+P)"},
	{name: "/timestamps", desc: "Toggle message timestamps (Ctrl+G)"},
	{name: "/help", desc: "Help and workflow guide (F1)"},
	{name: "/status", desc: "Show session status"},
	{name: "/clear", desc: "Redraw the screen"},
	{name: "/quit", desc: "Quit and preserve this session (Ctrl+Q)"},
	{name: "/exit", desc: "Alias for /quit"},
}

type slashMenu struct {
	*ui.Block
	theme    theme
	commands []slashCommand
	cursor   int
	offset   int
	box      image.Rectangle
}

func newSlashMenu(t theme) *slashMenu {
	s := &slashMenu{Block: newModalBlock(t), theme: t}
	s.Title = " COMMANDS "
	return s
}

func (s *slashMenu) place(l layout, c *composer) {
	// The palette occupies the full width of the conversation pane, so it
	// replaces that pane's content instead of floating over it and leaving
	// fragments of the border and text visible around the popup.
	r := l.main
	if r.Dx() < 10 || r.Dy() < 3 {
		r = image.Rect(1, max(0, c.Min.Y-3), max(11, l.width-1), c.Min.Y)
	}
	height := min(10, len(s.commands)+3)
	if height < 3 {
		height = 3
	}
	if available := c.Min.Y - r.Min.Y; height > available {
		height = available
	}
	if height < 3 {
		height = 3
	}
	x, width := r.Min.X, r.Dx()
	y := max(r.Min.Y, c.Min.Y-height)
	s.box = image.Rect(x, y, x+width, y+height)
	s.Block.SetRect(s.box.Min.X, s.box.Min.Y, s.box.Max.X, s.box.Max.Y)
}

func (s *slashMenu) clamp() {
	if s.cursor >= len(s.commands) {
		s.cursor = len(s.commands) - 1
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
}

func (s *slashMenu) move(delta int) {
	if len(s.commands) == 0 {
		return
	}
	s.cursor = (s.cursor + delta + len(s.commands)) % len(s.commands)
}

func (s *slashMenu) selected() (slashCommand, bool) {
	if len(s.commands) == 0 {
		return slashCommand{}, false
	}
	s.clamp()
	return s.commands[s.cursor], true
}

func (s *slashMenu) Draw(buf *ui.Buffer) {
	fillBlock(buf, s.GetRect(), ui.Style{Fg: s.theme.Fg, Bg: s.theme.PanelHigh})
	s.Title = " " + s.theme.tr("COMMANDS") + " "
	s.Block.Draw(buf)
	s.clamp()
	rows := max(1, s.Inner.Dy()-1)
	if s.cursor < s.offset {
		s.offset = s.cursor
	}
	if s.cursor >= s.offset+rows {
		s.offset = s.cursor - rows + 1
	}
	maxOffset := max(0, len(s.commands)-rows)
	if s.offset > maxOffset {
		s.offset = maxOffset
	}
	// The command column must fit the longest documented spelling, not a fixed
	// guess. `/mode [fast|goal|step]` is 22 cells; the old 18-cell column cut it
	// to `/mode [fast|goal…` and hid the mode this release added.
	nameWidth := 18
	for _, cmd := range s.commands {
		name := cmd.name
		if cmd.args != "" {
			name += " " + cmd.args
		}
		if w := rw.StringWidth(name) + 1; w > nameWidth {
			nameWidth = w
		}
	}
	nameWidth = min(nameWidth, max(1, s.Inner.Dx()-1))
	for i := 0; i < rows; i++ {
		idx := s.offset + i
		if idx >= len(s.commands) {
			break
		}
		cmd := s.commands[idx]
		style := s.theme.Body
		if idx == s.cursor {
			style = ui.Style{Fg: s.theme.Bg, Bg: s.theme.Accent, Modifier: ui.ModifierBold}
		}
		y := s.Inner.Min.Y + i
		if idx == s.cursor {
			fillBlock(buf, image.Rect(s.Inner.Min.X, y, s.Inner.Max.X, y+1), style)
		}
		name := cmd.name
		if cmd.args != "" {
			name += " " + cmd.args
		}
		buf.SetString(fitText(name, nameWidth), style, image.Pt(s.Inner.Min.X, y))
		descStyle := s.theme.BodyMuted
		if idx == s.cursor {
			descStyle = style
		}
		desc := s.theme.tr(cmd.desc)
		buf.SetString(fitText(desc, max(1, s.Inner.Dx()-nameWidth)), descStyle, image.Pt(s.Inner.Min.X+nameWidth, y))
	}
	if s.Inner.Dy() > 0 {
		buf.SetString(fitText(s.theme.tr("↑/↓ choose  ·  Tab complete  ·  Enter select  ·  Esc dismiss"), s.Inner.Dx()), s.theme.Timestamp, image.Pt(s.Inner.Min.X, s.Inner.Max.Y-1))
	}
}

// slashPrefix returns the command token if the composer currently contains only
// a command name. Arguments close the menu, matching the Legacy behaviour.
func (a *App) slashPrefix() string {
	if a.slashHidden {
		return ""
	}
	text := strings.TrimLeft(a.composer.Text(), " \t")
	if !strings.HasPrefix(text, "/") || strings.HasPrefix(text, "//") {
		return ""
	}
	for _, r := range text {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return ""
		}
	}
	return text
}

func (a *App) matchingCommands() []slashCommand {
	prefix := strings.ToLower(a.slashPrefix())
	if prefix == "" {
		return nil
	}
	var out []slashCommand
	for _, cmd := range slashCommands {
		if strings.HasPrefix(strings.ToLower(cmd.name), prefix) {
			out = append(out, cmd)
		}
	}
	return out
}

func (a *App) slashOpen() bool {
	return len(a.matchingCommands()) > 0
}

func (a *App) selectSlash() bool {
	cmd, ok := a.slash.selected()
	if !ok {
		return false
	}
	if cmd.needsArg {
		a.composer.SetText(cmd.name + " ")
		a.slashCursor = 0
		return true
	}
	a.composer.SetText(cmd.name)
	a.slashCursor = 0
	return false
}

func (a *App) autocompleteSlash() {
	cmd, ok := a.slash.selected()
	if !ok {
		return
	}
	text := cmd.name
	if cmd.needsArg {
		text += " "
	}
	a.composer.SetText(text)
	a.slashCursor = 0
}
