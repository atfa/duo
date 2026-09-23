package tui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/terminal"
)

func (a *App) render() {
	if a.native != "" || a.tty == nil {
		return
	}
	w, h := a.width, a.height
	if w < 60 {
		w = 60
	}
	if h < 18 {
		h = 18
	}
	duoH := 7
	topH := h - duoH
	leftW := (w - 1) / 2
	rightW := w - 1 - leftW

	snap := a.state.Snapshot()
	ar := a.tracker.Snapshot(protocol.Austin)
	tr := a.tracker.Snapshot(protocol.Tony)

	aTitle := fmt.Sprintf(" Austin · %s ", agentState(a.server.IsConnected(protocol.Austin), ar.Busy))
	tTitle := fmt.Sprintf(" Tony · %s ", agentState(a.server.IsConnected(protocol.Tony), tr.Busy))

	var b strings.Builder
	b.WriteString(terminal.Home)
	b.WriteString("┌" + header(aTitle, leftW-1) + "┬" + header(tTitle, rightW-1) + "┐\r\n")

	contentRows := topH - 2
	left := paneLines(a.austin, leftW-1, contentRows)
	right := paneLines(a.tony, rightW-1, contentRows)
	for i := 0; i < contentRows; i++ {
		b.WriteString("│" + fit(left[i], leftW-1) + "│" + fit(right[i], rightW-1) + "│\r\n")
	}
	b.WriteString("├" + strings.Repeat("─", leftW-1) + "┴" + strings.Repeat("─", rightW-1) + "┤\r\n")

	readyA, readyT := "○", "○"
	if snap.Ready[protocol.Austin] {
		readyA = "✓"
	}
	if snap.Ready[protocol.Tony] {
		readyT = "✓"
	}
	status := fmt.Sprintf(" Duo · %s · Plan v%d · Austin %s · Tony %s ", snap.Phase, snap.PlanVersion, readyA, readyT)
	b.WriteString("│" + fit(status, w-2) + "│\r\n")

	plan := strings.ReplaceAll(strings.TrimSpace(snap.Plan), "\n", " ")
	if plan == "" {
		plan = "No shared plan yet"
	}
	b.WriteString("│" + fit(" Plan: "+plan, w-2) + "│\r\n")

	logs := paneLines(a.duo, w-4, 2)
	for _, line := range logs {
		b.WriteString("│ " + fit(line, w-4) + " │\r\n")
	}

	input := string(a.input)
	if a.status != "" && input == "" {
		input = "(" + a.status + ")"
	}
	b.WriteString("│ > " + fitTail(input, w-6) + " │\r\n")
	help := " Enter send · Ctrl+A Austin native · Ctrl+T Tony native · click [↗] · Ctrl+Q quit · Ctrl+] return from native "
	b.WriteString("└" + fit(help, w-2, "─") + "┘")

	_, _ = a.tty.File.WriteString(b.String())
}

func agentState(connected, busy bool) string {
	if !connected {
		return "connecting"
	}
	if busy {
		return "working"
	}
	return "idle"
}

func paneLines(entries []entry, width, rows int) []string {
	var all []string
	for _, e := range entries {
		text := strings.TrimSpace(e.text)
		for _, raw := range strings.Split(text, "\n") {
			all = append(all, wrap(raw, width)...)
		}
		all = append(all, "")
	}
	if len(all) > 0 && all[len(all)-1] == "" {
		all = all[:len(all)-1]
	}
	if len(all) > rows {
		all = all[len(all)-rows:]
	}
	out := make([]string, rows)
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
	if displayWidth(s) <= width {
		return fit(s, width)
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
	return fit("…"+string(runes[i:]), width)
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
