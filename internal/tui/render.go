package tui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/terminal"
)

const (
	ansiReset  = "\x1b[0m"
	ansiBorder = "\x1b[36m"
	ansiTitle  = "\x1b[1;36m"
	ansiStatus = "\x1b[33m"
	ansiHint   = "\x1b[2;37m"
	ansiError  = "\x1b[31m"
)

const (
	minWidth  = 60
	minHeight = 18
)

// buildFrame returns one complete, self-contained frame. It never writes to the
// terminal and never emits more rows or columns than the real terminal size.
func (a *App) buildFrame(mode renderMode) string {
	if a.native != "" {
		return ""
	}
	w, h := a.width, a.height

	var b strings.Builder
	b.WriteString(terminal.BeginSync)
	b.WriteString(terminal.HideCursor)
	if mode == renderFullClear {
		b.WriteString(terminal.ClearHome)
	}
	b.WriteString(terminal.Home)
	b.WriteString(terminal.AutoWrapOff)

	if w < minWidth || h < minHeight {
		a.writeTooSmall(&b, w, h)
	} else {
		a.writeLayout(&b, w, h)
		row, col := inputCursor(w, h, string(a.input))
		b.WriteString(fmt.Sprintf("\x1b[%d;%dH", row, col))
	}

	// Restore autowrap while the cursor is already parked, so enabling it can
	// never turn a full-width last line into a wrap or scroll.
	b.WriteString(terminal.AutoWrapOn)
	b.WriteString(terminal.ShowCursor)
	b.WriteString(terminal.EndSync)
	return b.String()
}

// writeTooSmall renders a bounded notice instead of inventing a terminal size.
// Every line is clipped and padded to the real width, and the number of lines
// never exceeds the real height.
func (a *App) writeTooSmall(b *strings.Builder, w, h int) {
	lines := []string{
		"Duo",
		"",
		"Terminal too small",
		fmt.Sprintf("Minimum: %d×%d", minWidth, minHeight),
		fmt.Sprintf("Current: %d×%d", w, h),
	}
	if h < len(lines) {
		lines = lines[:maxInt(h, 0)]
	}
	for i, line := range lines {
		if i > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString(fit(line, w))
	}
}

// writeLayout draws the full Duo UI for the given real terminal geometry. It
// leaves the last terminal row unused and does not emit a trailing newline.
func (a *App) writeLayout(b *strings.Builder, w, h int) {
	duoH := 7
	topH := h - duoH
	leftW := (w - 1) / 2
	rightW := w - 1 - leftW

	snap := a.state.Snapshot()
	ar := a.tracker.Snapshot(protocol.Austin)
	tr := a.tracker.Snapshot(protocol.Tony)

	aTitle := fmt.Sprintf(" Austin · %s ", agentState(a.server.IsConnected(protocol.Austin), ar, a.frame, a.processState(protocol.Austin)))
	tTitle := fmt.Sprintf(" Tony · %s ", agentState(a.server.IsConnected(protocol.Tony), tr, a.frame, a.processState(protocol.Tony)))

	b.WriteString(paint(ansiBorder, "┌") + paint(ansiTitle, header(aTitle, leftW-1)) + paint(ansiBorder, "┬") + paint(ansiTitle, header(tTitle, rightW-1)) + paint(ansiBorder, "┐") + "\r\n")

	contentRows := topH - 2
	left := paneLines(a.austin, leftW-1, contentRows)
	right := paneLines(a.tony, rightW-1, contentRows)
	for i := 0; i < contentRows; i++ {
		b.WriteString(paint(ansiBorder, "│") + paintEntry(left[i], leftW-1) + paint(ansiBorder, "│") + paintEntry(right[i], rightW-1) + paint(ansiBorder, "│\r\n"))
	}
	b.WriteString(paint(ansiBorder, "├"+strings.Repeat("─", leftW-1)+"┴"+strings.Repeat("─", rightW-1)+"┤\r\n"))

	readyA, readyT := "○", "○"
	if snap.Ready[protocol.Austin] {
		readyA = "✓"
	}
	if snap.Ready[protocol.Tony] {
		readyT = "✓"
	}
	status := fmt.Sprintf(" Duo · %s · Plan v%d · Austin %s · Tony %s ", snap.Phase, snap.PlanVersion, readyA, readyT)
	b.WriteString(paint(ansiBorder, "│") + paint(ansiStatus, fit(status, w-2)) + paint(ansiBorder, "│\r\n"))

	plan := strings.ReplaceAll(strings.TrimSpace(snap.Plan), "\n", " ")
	if plan == "" {
		plan = "No shared plan yet"
	}
	b.WriteString(paint(ansiBorder, "│") + paint(ansiHint, fit(" Plan: "+plan, w-2)) + paint(ansiBorder, "│\r\n"))

	logs := paneLines(a.duo, w-4, 2)
	for _, line := range logs {
		b.WriteString(paint(ansiBorder, "│ ") + paintEntry(line, w-4) + paint(ansiBorder, " │\r\n"))
	}

	input := string(a.input)
	if a.status != "" && input == "" {
		input = "(" + a.status + ")"
	}
	b.WriteString(paint(ansiBorder, "│") + paint(ansiStatus, " > ") + fitTail(input, w-6) + paint(ansiBorder, " │\r\n"))
	help := " Enter send · Ctrl+A/T native · Ctrl+R Austin restart · Ctrl+Y Tony restart · Ctrl+Q quit · Ctrl+] / Ctrl+\\ return "
	b.WriteString(paint(ansiBorder, "└") + paint(ansiHint, fit(help, w-2, "─")) + paint(ansiBorder, "┘"))
}

func paint(code, text string) string {
	return code + text + ansiReset
}

func paintEntry(line entry, width int) string {
	if !line.error {
		return fit(line.text, width)
	}
	return paint(ansiError, fit(line.text, width))
}

func (a *App) processState(id protocol.AgentID) agent.ProcessState {
	if s, ok := a.agents.Session(id); ok {
		return s.State()
	}
	return agent.ProcessFailed
}

func agentState(connected bool, runtime harness.AgentRuntime, frame int, states ...agent.ProcessState) string {
	if len(states) > 0 {
		switch states[0] {
		case agent.ProcessStarting:
			return "starting"
		case agent.ProcessStopping:
			return "stopping"
		case agent.ProcessExited:
			return "exited [Restart]"
		case agent.ProcessFailed:
			return "failed [Restart]"
		}
	}
	if !connected {
		return "connecting"
	}
	spinner := []string{"|", "/", "-", "\\"}[frame%4]
	if runtime.ToolDepth > 0 {
		return "tool " + spinner
	}
	if runtime.ProviderActive {
		return "thinking " + spinner
	}
	if runtime.Busy {
		return "working " + spinner
	}
	return "idle"
}

// inputCursor returns the 1-based terminal position for the input insertion
// point, using the real terminal size (never an invented minimum).
func inputCursor(width, height int, input string) (row, col int) {
	inputWidth := width - 6
	if inputWidth < 1 {
		inputWidth = 1
	}
	visible := tailContent(input, inputWidth)
	col = 5 + displayWidth(visible)
	return height - 2, col
}

func paneLines(entries []entry, width, rows int) []entry {
	var all []entry
	for _, e := range entries {
		text := strings.TrimSpace(e.text)
		for _, raw := range strings.Split(text, "\n") {
			for _, line := range wrap(raw, width) {
				all = append(all, entry{text: line, error: e.error})
			}
		}
		all = append(all, entry{})
	}
	if len(all) > 0 && all[len(all)-1].text == "" {
		all = all[:len(all)-1]
	}
	if len(all) > rows {
		all = all[len(all)-rows:]
	}
	out := make([]entry, rows)
	copy(out[rows-len(all):], all)
	return out
}

func wrap(s string, width int) []string {
	if width <= 1 {
		return []string{""}
	}
	if s == "" {
		return []string{""}
	}
	var out []string
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := runeWidth(r)
		if used+rw > width && b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
			used = 0
		}
		b.WriteRune(r)
		used += rw
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

func header(title string, width int) string {
	button := "[↗] "
	available := width - displayWidth(button)
	if available < 1 {
		return fit(title, width)
	}
	return fit(title, available) + button
}

func fit(s string, width int, fillOpt ...string) string {
	fill := " "
	if len(fillOpt) > 0 {
		fill = fillOpt[0]
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := runeWidth(r)
		if used+rw > width {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	for used < width {
		b.WriteString(fill)
		used += displayWidth(fill)
	}
	return b.String()
}

func fitTail(s string, width int) string {
	return fit(tailContent(s, width), width)
}

func tailContent(s string, width int) string {
	if displayWidth(s) <= width {
		return s
	}
	runes := []rune(s)
	used := 0
	i := len(runes)
	for i > 0 {
		rw := runeWidth(runes[i-1])
		if used+rw > width-1 {
			break
		}
		used += rw
		i--
	}
	return "…" + string(runes[i:])
}

func displayWidth(s string) int {
	n := 0
	for _, r := range s {
		n += runeWidth(r)
	}
	return n
}

func runeWidth(r rune) int {
	if r == 0 || unicode.Is(unicode.Mn, r) {
		return 0
	}
	if r < 0x20 || (r >= 0x7f && r < 0xa0) {
		return 0
	}
	if r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a ||
		(r >= 0x2e80 && r <= 0xa4cf) || (r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) || (r >= 0xfe10 && r <= 0xfe19) ||
		(r >= 0xfe30 && r <= 0xfe6f) || (r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) || (r >= 0x1f300 && r <= 0x1faff)) {
		return 2
	}
	return 1
}

func popRune(buf []byte) []byte {
	if len(buf) == 0 {
		return buf
	}
	_, size := utf8.DecodeLastRune(buf)
	if size <= 0 {
		size = 1
	}
	return buf[:len(buf)-size]
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
