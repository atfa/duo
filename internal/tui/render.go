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
	ansiBold   = "\x1b[1m"
	ansiItalic = "\x1b[3m"
	ansiCode   = "\x1b[2;36m"
	ansiLink   = "\x1b[4;36m"
	ansiSelect = "\x1b[7m"
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
	} else if a.view == viewHelp {
		a.writeHelp(&b, w, h)
	} else {
		a.writeLayout(&b, w, h)
		row, col := a.composerCursor(w, h)
		b.WriteString(fmt.Sprintf("\x1b[%d;%dH", row, col))
	}

	// Restore autowrap while the cursor is already parked, so enabling it can
	// never turn a full-width last line into a wrap or scroll.
	b.WriteString(terminal.AutoWrapOn)
	if a.view == viewMain && w >= minWidth && h >= minHeight {
		b.WriteString(terminal.ShowCursor)
	}
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
	composer := a.composerLayout(w)
	duoH := 7 + composer.rows
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
	a.clampPaneOffsets(leftW-1, rightW-1, contentRows)
	left := styledPaneLinesAt(a.austin, leftW-1, contentRows, a.austinOffset)
	right := styledPaneLinesAt(a.tony, rightW-1, contentRows, a.tonyOffset)
	for i := 0; i < contentRows; i++ {
		b.WriteString(paint(ansiBorder, "│") + a.paintPaneEntry(left[i], leftW-1, protocol.Austin, i) + paint(ansiBorder, "│") + a.paintPaneEntry(right[i], rightW-1, protocol.Tony, i) + paint(ansiBorder, "│\r\n"))
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

	logs := styledPaneLines(a.duo, w-4, 2)
	for _, line := range logs {
		b.WriteString(paint(ansiBorder, "│ ") + paintEntry(line, w-4) + paint(ansiBorder, " │\r\n"))
	}

	statusLine := " Status: " + a.status
	statusColor := ansiStatus
	if a.statusError {
		statusColor = ansiError
	}
	b.WriteString(paint(ansiBorder, "│") + paint(statusColor, fit(statusLine, w-2)) + paint(ansiBorder, "│\r\n"))
	for _, line := range composer.lines {
		b.WriteString(paint(ansiBorder, "│") + paint(ansiStatus, line.prefix) + fit(line.text, line.width) + paint(ansiBorder, "│\r\n"))
	}
	b.WriteString(paint(ansiBorder, "└") + paint(ansiHint, fit(mainFooter(), w-2, "─")) + paint(ansiBorder, "┘"))
}

func paint(code, text string) string {
	return code + text + ansiReset
}

func paintEntry(line paneLine, width int) string {
	if line.error {
		return paint(ansiError, fit(line.text(), width))
	}
	var b strings.Builder
	used := 0
	for _, span := range line.spans {
		if span.style == "" {
			b.WriteString(span.text)
		} else {
			b.WriteString(paint(span.style, span.text))
		}
		used += displayWidth(span.text)
	}
	return b.String() + strings.Repeat(" ", maxInt(width-used, 0))
}

func (a *App) paintPaneEntry(line paneLine, width int, agent protocol.AgentID, row int) string {
	from, to, selected := a.selectionColumns(agent, row)
	if !selected {
		return paintEntry(line, width)
	}
	var b strings.Builder
	used := 0
	for _, span := range line.spans {
		for _, r := range span.text {
			rw := runeWidth(r)
			if used+rw > from && used < to {
				b.WriteString(paint(ansiSelect, string(r)))
			} else if line.error {
				b.WriteString(paint(ansiError, string(r)))
			} else if span.style != "" {
				b.WriteString(paint(span.style, string(r)))
			} else {
				b.WriteRune(r)
			}
			used += rw
		}
	}
	return b.String() + strings.Repeat(" ", maxInt(width-used, 0))
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
	app := &App{width: width, height: height, input: []byte(input), inputPos: len(input)}
	return app.composerCursor(width, height)
}

func (a *App) writeHelp(b *strings.Builder, w, h int) {
	contentWidth := w - 2
	lines := a.helpLines(contentWidth)
	a.clampHelpOffset()
	visible := a.helpVisibleRows()
	title := " Duo Help · " + a.version + " "
	b.WriteString(paint(ansiBorder, "┌") + paint(ansiTitle, fit(title, contentWidth, "─")) + paint(ansiBorder, "┐\r\n"))
	for i := 0; i < visible; i++ {
		line := ""
		if at := a.helpOffset + i; at < len(lines) {
			line = lines[at]
		}
		color := ansiHint
		if line != "" && !strings.HasPrefix(line, " ") {
			color = ansiTitle
		}
		b.WriteString(paint(ansiBorder, "│") + paint(color, fit(line, contentWidth)) + paint(ansiBorder, "│\r\n"))
	}
	b.WriteString(paint(ansiBorder, "├") + paint(ansiBorder, strings.Repeat("─", contentWidth)) + paint(ansiBorder, "┤\r\n"))
	first, last := a.helpOffset+1, minInt(a.helpOffset+visible, len(lines))
	if len(lines) == 0 {
		first, last = 0, 0
	}
	foot := fmt.Sprintf(" Lines %d–%d / %d · ↑↓/jk scroll · PgUp/PgDn · Esc close · Ctrl+Q quit ", first, last, len(lines))
	b.WriteString(paint(ansiBorder, "└") + paint(ansiHint, fit(foot, contentWidth, "─")) + paint(ansiBorder, "┘"))
}

func styledPaneLines(entries []entry, width, rows int) []paneLine {
	return styledPaneLinesAt(entries, width, rows, 0)
}

func styledPaneLinesAt(entries []entry, width, rows, offset int) []paneLine {
	all := wrappedPaneLines(entries, width)
	start := maxInt(len(all)-rows-maxInt(offset, 0), 0)
	end := minInt(start+rows, len(all))
	all = all[start:end]
	out := make([]paneLine, rows)
	copy(out[rows-len(all):], all)
	return out
}

// paneLines is retained for scrolling tests and callers that need plain text.
func paneLinesAt(entries []entry, width, rows, offset int) []entry {
	styled := styledPaneLinesAt(entries, width, rows, offset)
	out := make([]entry, len(styled))
	for i, line := range styled {
		out[i] = entry{text: line.text(), error: line.error}
	}
	return out
}

type markdownSpan struct {
	text  string
	style string
}

type paneLine struct {
	spans        []markdownSpan
	continuation string
	error        bool
}

func (l paneLine) text() string {
	var b strings.Builder
	for _, span := range l.spans {
		b.WriteString(span.text)
	}
	return b.String()
}

func wrappedPaneLines(entries []entry, width int) []paneLine {
	var all []paneLine
	for _, e := range entries {
		text := strings.TrimSpace(e.text)
		for _, line := range markdownLines(text, width) {
			for _, wrapped := range wrapMarkdown(line, width) {
				wrapped.error = e.error
				all = append(all, wrapped)
			}
		}
		all = append(all, paneLine{})
	}
	if len(all) > 0 && all[len(all)-1].text() == "" {
		all = all[:len(all)-1]
	}
	return all
}

// markdownLines keeps terminal layout separate from styling: text is wrapped
// before ANSI is added, so colored spans cannot affect width accounting.
func markdownLines(text string, width int) []paneLine {
	var out []paneLine
	inCode := false
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		if !inCode && i+1 < len(lines) {
			if header, align, ok := markdownTableHeader(raw, lines[i+1]); ok {
				rows := [][]string{header}
				i += 2
				for i < len(lines) {
					row, ok := markdownTableRow(lines[i], len(header))
					if !ok {
						break
					}
					rows = append(rows, row)
					i++
				}
				out = append(out, renderMarkdownTable(rows, align, width)...)
				i--
				continue
			}
		}
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "```") {
			inCode = !inCode
			continue
		}
		if inCode {
			out = append(out, paneLine{spans: []markdownSpan{{text: raw, style: ansiCode}}})
			continue
		}
		if isRule(trimmed) {
			out = append(out, paneLine{spans: []markdownSpan{{text: "────────", style: ansiHint}}})
			continue
		}
		prefix, body, style := "", raw, ""
		if n := headingPrefix(raw); n > 0 {
			prefix, body, style = strings.Repeat(" ", n-1), strings.TrimSpace(raw[n:]), ansiTitle
		} else if strings.HasPrefix(trimmed, ">") {
			prefix, body, style = "│ ", strings.TrimSpace(strings.TrimPrefix(trimmed, ">")), ansiHint
		} else if marker, rest, ok := listPrefix(raw); ok {
			prefix, body = marker, rest
		}
		spans := append([]markdownSpan{{text: prefix, style: style}}, markdownInline(body)...)
		if style != "" {
			for i := 1; i < len(spans); i++ {
				if spans[i].style == "" {
					spans[i].style = style
				}
			}
		}
		line := paneLine{spans: compactSpans(spans)}
		if prefix != "" && style == "" && body != raw {
			line.continuation = strings.Repeat(" ", displayWidth(prefix))
		}
		out = append(out, line)
	}
	return out
}

type tableAlign uint8

const (
	tableLeft tableAlign = iota
	tableCenter
	tableRight
)

func markdownTableHeader(header, separator string) ([]string, []tableAlign, bool) {
	cells, ok := markdownTableCells(header)
	if !ok {
		return nil, nil, false
	}
	dividers, ok := markdownTableCells(separator)
	if !ok || len(cells) != len(dividers) {
		return nil, nil, false
	}
	align := make([]tableAlign, len(cells))
	for i, divider := range dividers {
		d := strings.TrimSpace(divider)
		left, right := strings.HasPrefix(d, ":"), strings.HasSuffix(d, ":")
		d = strings.Trim(d, ":")
		if len(d) < 3 || strings.Trim(d, "-") != "" {
			return nil, nil, false
		}
		if right {
			align[i] = tableRight
		}
		if left && right {
			align[i] = tableCenter
		}
	}
	return cells, align, true
}

func markdownTableRow(line string, columns int) ([]string, bool) {
	cells, ok := markdownTableCells(line)
	return cells, ok && len(cells) == columns
}

func markdownTableCells(line string) ([]string, bool) {
	if !strings.Contains(line, "|") {
		return nil, false
	}
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts, true
}

func renderMarkdownTable(rows [][]string, align []tableAlign, width int) []paneLine {
	for i, row := range rows {
		for j, cell := range row {
			rows[i][j] = markdownText(cell)
		}
	}
	columns := len(rows[0])
	widths := make([]int, columns)
	minimums := make([]int, columns)
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = maxInt(widths[i], maxInt(displayWidth(cell), 1))
			for _, r := range cell {
				minimums[i] = maxInt(minimums[i], runeWidth(r))
			}
		}
	}
	for i := range minimums {
		minimums[i] = maxInt(minimums[i], 1)
	}
	available := maxInt(width-columns-1, tableContentWidth(minimums))
	for tableContentWidth(widths) > available {
		wide := -1
		for i, cellWidth := range widths {
			if cellWidth > minimums[i] && (wide < 0 || cellWidth > widths[wide]) {
				wide = i
			}
		}
		if wide < 0 {
			break
		}
		widths[wide]--
	}
	border := func(left, middle, right string) paneLine {
		spans := []markdownSpan{{text: left, style: ansiHint}}
		for i, cellWidth := range widths {
			if i > 0 {
				spans = append(spans, markdownSpan{text: middle, style: ansiHint})
			}
			spans = append(spans, markdownSpan{text: strings.Repeat("─", cellWidth), style: ansiHint})
		}
		return paneLine{spans: append(spans, markdownSpan{text: right, style: ansiHint})}
	}
	out := []paneLine{border("┌", "┬", "┐")}
	for rowIndex, row := range rows {
		out = append(out, markdownTableLines(row, widths, align, rowIndex == 0)...)
		if rowIndex == 0 {
			out = append(out, border("├", "┼", "┤"))
		}
	}
	return append(out, border("└", "┴", "┘"))
}

func markdownText(s string) string {
	var b strings.Builder
	for _, span := range markdownInline(s) {
		b.WriteString(span.text)
	}
	return b.String()
}

func tableContentWidth(widths []int) int {
	total := 0
	for _, width := range widths {
		total += width
	}
	return total
}

func markdownTableLines(cells []string, widths []int, align []tableAlign, header bool) []paneLine {
	wrapped := make([][]string, len(cells))
	height := 1
	for i, cell := range cells {
		wrapped[i] = wrap(cell, widths[i])
		height = maxInt(height, len(wrapped[i]))
	}
	out := make([]paneLine, height)
	for line := range out {
		row := make([]string, len(cells))
		for i := range cells {
			if line < len(wrapped[i]) {
				row[i] = wrapped[i][line]
			}
		}
		out[line] = markdownTableLine(row, widths, align, header)
	}
	return out
}

func markdownTableLine(cells []string, widths []int, align []tableAlign, header bool) paneLine {
	spans := []markdownSpan{{text: "│", style: ansiHint}}
	for i, cell := range cells {
		padding := widths[i] - displayWidth(cell)
		left, right := 0, padding
		if align[i] == tableRight {
			left, right = padding, 0
		} else if align[i] == tableCenter {
			left, right = padding/2, padding-padding/2
		}
		if left > 0 {
			spans = append(spans, markdownSpan{text: strings.Repeat(" ", left)})
		}
		style := ""
		if header {
			style = ansiBold
		}
		spans = append(spans, markdownSpan{text: cell, style: style})
		if right > 0 {
			spans = append(spans, markdownSpan{text: strings.Repeat(" ", right)})
		}
		spans = append(spans, markdownSpan{text: "│", style: ansiHint})
	}
	return paneLine{spans: compactSpans(spans)}
}

func headingPrefix(s string) int {
	n := 0
	for n < len(s) && s[n] == '#' {
		n++
	}
	if n > 0 && n <= 6 && n < len(s) && s[n] == ' ' {
		return n + 1
	}
	return 0
}

func isRule(s string) bool {
	if len(s) < 3 {
		return false
	}
	for _, r := range s {
		if r != '-' && r != '*' && r != '_' && r != ' ' {
			return false
		}
	}
	return true
}

func listPrefix(s string) (marker, rest string, ok bool) {
	t := strings.TrimLeft(s, " \t")
	indent := strings.ReplaceAll(s[:len(s)-len(t)], "\t", "  ")
	if len(t) >= 2 && (t[0] == '-' || t[0] == '*' || t[0] == '+') && t[1] == ' ' {
		return "  " + indent + "• ", t[2:], true
	}
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	if i > 0 && i+1 < len(t) && (t[i] == '.' || t[i] == ')') && t[i+1] == ' ' {
		return "  " + indent + t[:i+2], t[i+2:], true
	}
	return "", "", false
}

func markdownInline(s string) []markdownSpan {
	var out []markdownSpan
	for len(s) > 0 {
		if strings.HasPrefix(s, "[") {
			if end := strings.Index(s, "]("); end > 1 {
				if close := strings.Index(s[end+2:], ")"); close >= 0 {
					out = append(out, markdownSpan{text: s[1:end], style: ansiLink}, markdownSpan{text: " <" + s[end+2:end+2+close] + ">", style: ansiHint})
					s = s[end+3+close:]
					continue
				}
			}
		}
		matched := false
		for _, token := range []struct{ mark, style string }{{"**", ansiBold}, {"__", ansiBold}, {"`", ansiCode}, {"*", ansiItalic}, {"_", ansiItalic}} {
			if strings.HasPrefix(s, token.mark) {
				if end := strings.Index(s[len(token.mark):], token.mark); end > 0 {
					out = append(out, markdownSpan{text: s[len(token.mark) : len(token.mark)+end], style: token.style})
					s = s[len(token.mark)+end+len(token.mark):]
					matched = true
					break
				}
			}
		}
		if matched {
			continue
		}
		_, size := utf8.DecodeRuneInString(s)
		out = append(out, markdownSpan{text: s[:size]})
		s = s[size:]
	}
	return compactSpans(out)
}

func compactSpans(spans []markdownSpan) []markdownSpan {
	var out []markdownSpan
	for _, span := range spans {
		if span.text == "" {
			continue
		}
		if len(out) > 0 && out[len(out)-1].style == span.style {
			out[len(out)-1].text += span.text
		} else {
			out = append(out, span)
		}
	}
	return out
}

func wrapMarkdown(line paneLine, width int) []paneLine {
	if width <= 1 {
		return []paneLine{{}}
	}
	var out []paneLine
	current := paneLine{}
	used := 0
	appendRune := func(r rune, style string) {
		if len(current.spans) > 0 && current.spans[len(current.spans)-1].style == style {
			current.spans[len(current.spans)-1].text += string(r)
		} else {
			current.spans = append(current.spans, markdownSpan{text: string(r), style: style})
		}
	}
	for _, span := range line.spans {
		for _, r := range span.text {
			rw := runeWidth(r)
			if used+rw > width && len(current.spans) > 0 {
				out = append(out, current)
				current, used = paneLine{}, 0
				indent := line.continuation
				if displayWidth(indent) >= width {
					indent = strings.Repeat(" ", width-1)
				}
				if displayWidth(indent)+rw > width {
					indent = strings.Repeat(" ", maxInt(width-rw, 0))
				}
				if indent != "" {
					current.spans = append(current.spans, markdownSpan{text: indent})
					used = displayWidth(indent)
				}
			}
			appendRune(r, span.style)
			used += rw
		}
	}
	if len(current.spans) > 0 || len(out) == 0 {
		out = append(out, current)
	}
	return out
}

type composerLine struct {
	text       string
	prefix     string
	width      int
	start, end int
}

type composerLayout struct {
	lines     []composerLine
	cursorRow int
	cursorCol int
	rows      int
}

func (a *App) composerLayout(width int) composerLayout {
	contentWidth := maxInt(width-2, 1)
	prefix := composerPrefix()
	firstWidth := maxInt(contentWidth-displayWidth(prefix), 1)
	input := a.input
	pos := clampInputPos(input, a.inputPos)
	var all []composerLine
	start, used := 0, 0
	linePrefix, lineWidth := prefix, firstWidth
	appendLine := func(end int) {
		all = append(all, composerLine{text: string(input[start:end]), prefix: linePrefix, width: lineWidth, start: start, end: end})
	}
	for i := 0; i < len(input); {
		r, size := utf8.DecodeRune(input[i:])
		if size == 0 {
			break
		}
		if r == '\n' {
			appendLine(i)
			i += size
			start = i
			used = 0
			linePrefix = ""
			lineWidth = contentWidth
			continue
		}
		rw := runeWidth(r)
		if used+rw > lineWidth && i > start {
			appendLine(i)
			start = i
			used = 0
			linePrefix = ""
			lineWidth = contentWidth
		}
		used += rw
		i += size
	}
	appendLine(len(input))
	cursor := 0
	for i, line := range all {
		if pos >= line.start && pos <= line.end {
			cursor = i
			break
		}
	}
	rows := minInt(maxInt(len(all), 1), 4)
	first := minInt(maxInt(cursor-rows+1, 0), maxInt(len(all)-rows, 0))
	visible := all[first : first+rows]
	line := visible[cursor-first]
	end := minInt(maxInt(pos, line.start), line.end)
	return composerLayout{lines: visible, cursorRow: cursor - first, cursorCol: 2 + displayWidth(line.prefix) + displayWidth(string(input[line.start:end])), rows: rows}
}

func (a *App) composerCursor(width, height int) (int, int) {
	composer := a.composerLayout(width)
	return height - 1 - composer.rows + composer.cursorRow, composer.cursorCol
}

func (a *App) paneRows() (leftW, rightW, rows int) {
	composer := a.composerLayout(a.width)
	leftW = (a.width - 1) / 2
	rightW = a.width - 1 - leftW
	return leftW - 1, rightW - 1, maxInt(a.height-(7+composer.rows)-2, 0)
}

func (a *App) hitPane(x, y int) protocol.AgentID {
	leftW, _, rows := a.paneRows()
	if y < 2 || y > rows+1 {
		return ""
	}
	if x >= 2 && x <= leftW {
		return protocol.Austin
	}
	if x >= leftW+2 && x <= a.width-1 {
		return protocol.Tony
	}
	return ""
}

func paneLineCount(entries []entry, width int) int {
	return len(wrappedPaneLines(entries, width))
}

func (a *App) maxPaneOffset(agent protocol.AgentID, width, rows int) int {
	var entries []entry
	if agent == protocol.Austin {
		entries = a.austin
	} else {
		entries = a.tony
	}
	return maxInt(paneLineCount(entries, width)-rows, 0)
}

func (a *App) clampPaneOffsets(leftW, rightW, rows int) {
	a.austinOffset = minInt(maxInt(a.austinOffset, 0), a.maxPaneOffset(protocol.Austin, leftW, rows))
	a.tonyOffset = minInt(maxInt(a.tonyOffset, 0), a.maxPaneOffset(protocol.Tony, rightW, rows))
}

func (a *App) clampOffsets() {
	if a.view == viewHelp {
		a.clampHelpOffset()
	}
	if a.width >= minWidth && a.height >= minHeight {
		left, right, rows := a.paneRows()
		a.clampPaneOffsets(left, right, rows)
	}
}

func (a *App) scrollPane(agent protocol.AgentID, delta int) {
	left, right, rows := a.paneRows()
	if agent == protocol.Austin {
		a.austinOffset = minInt(maxInt(a.austinOffset+delta, 0), a.maxPaneOffset(agent, left, rows))
	} else if agent == protocol.Tony {
		a.tonyOffset = minInt(maxInt(a.tonyOffset+delta, 0), a.maxPaneOffset(agent, right, rows))
	}
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

func clampInputPos(buf []byte, pos int) int {
	if pos < 0 {
		return 0
	}
	if pos > len(buf) {
		pos = len(buf)
	}
	for pos > 0 && pos < len(buf) && (buf[pos]&0xc0) == 0x80 {
		pos--
	}
	return pos
}

func previousRune(buf []byte, pos int) int {
	pos = clampInputPos(buf, pos)
	if pos == 0 {
		return 0
	}
	_, size := utf8.DecodeLastRune(buf[:pos])
	if size < 1 {
		size = 1
	}
	return pos - size
}

func nextRune(buf []byte, pos int) int {
	pos = clampInputPos(buf, pos)
	if pos == len(buf) {
		return pos
	}
	_, size := utf8.DecodeRune(buf[pos:])
	if size < 1 {
		size = 1
	}
	return minInt(pos+size, len(buf))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
