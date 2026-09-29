package tui

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
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
	// duoLogRows is the height of the always-visible system log between the two
	// agent panes and the status line. It is scrollable, so system, harness and
	// delivery messages are no longer limited to the last two lines.
	duoLogRows = 4
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
	} else if a.view == viewDetail {
		a.writeDetail(&b, w, h)
	} else if a.view == viewModel {
		a.writeModel(&b, w, h)
	} else {
		if a.timeline {
			a.writeTimelineLayout(&b, w, h)
		} else {
			a.writeLayout(&b, w, h)
		}
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
	l := a.layoutFor(w, h)
	a.clampPaneOffsets()

	ar := a.tracker.Snapshot(protocol.Austin)
	tr := a.tracker.Snapshot(protocol.Tony)

	aState := agentState(a.server.IsConnected(protocol.Austin), ar, a.frame, a.processState(protocol.Austin))
	tState := agentState(a.server.IsConnected(protocol.Tony), tr, a.frame, a.processState(protocol.Tony))

	b.WriteString(paint(ansiBorder, "┌") + paint(ansiTitle, header(paneTitle("Austin", aState, a.austinOffset, a.austinNew), l.leftW)) + paint(ansiBorder, "┬") + paint(ansiTitle, header(paneTitle("Tony", tState, a.tonyOffset, a.tonyNew), l.rightW)) + paint(ansiBorder, "┐") + "\r\n")

	left := a.styledPane(protocol.Austin, l.leftW, l.content, a.austinOffset)
	right := a.styledPane(protocol.Tony, l.rightW, l.content, a.tonyOffset)
	for i := 0; i < l.content; i++ {
		b.WriteString(paint(ansiBorder, "│") + a.paintPaneEntry(left[i], l.leftW, protocol.Austin, i) + paint(ansiBorder, "│") + a.paintPaneEntry(right[i], l.rightW, protocol.Tony, i) + paint(ansiBorder, "│\r\n"))
	}
	b.WriteString(paint(ansiBorder, "├"+strings.Repeat("─", l.leftW)+"┴"+strings.Repeat("─", l.rightW)+"┤\r\n"))
	if l.preview > 0 {
		a.writePreview(b, l)
		b.WriteString(paint(ansiBorder, "├"+strings.Repeat("─", l.leftW)+"┴"+strings.Repeat("─", l.rightW)+"┤\r\n"))
	}

	for _, line := range a.styledPane(protocol.Duo, l.logW, duoLogRows, a.duoOffset) {
		b.WriteString(paint(ansiBorder, "│ ") + paintEntry(line, l.logW) + paint(ansiBorder, " │\r\n"))
	}

	a.writeFrameTail(b, w, composer)
}

// writeTimelineLayout draws the main-view conversation timeline: one
// chronological stream in which Austin's messages sit on the left and Tony's
// are indented from the left so their blocks read on the right.
func (a *App) writeTimelineLayout(b *strings.Builder, w, h int) {
	composer := a.composerLayout(w)
	l := a.layoutFor(w, h)
	a.clampPaneOffsets()

	ar := a.tracker.Snapshot(protocol.Austin)
	tr := a.tracker.Snapshot(protocol.Tony)
	aState := agentState(a.server.IsConnected(protocol.Austin), ar, a.frame, a.processState(protocol.Austin))
	tState := agentState(a.server.IsConnected(protocol.Tony), tr, a.frame, a.processState(protocol.Tony))

	b.WriteString(paint(ansiBorder, "┌") + paint(ansiTitle, header(paneTitle("Austin", aState, a.duoOffset, a.duoNew), l.leftW)) + paint(ansiBorder, "┬") + paint(ansiTitle, header(paneTitle("Tony", tState, a.duoOffset, a.duoNew), l.rightW)) + paint(ansiBorder, "┐") + "\r\n")

	lines := a.styledPane(protocol.Duo, l.timelineW, l.content, a.duoOffset)
	for i := 0; i < l.content; i++ {
		b.WriteString(paint(ansiBorder, "│") + a.paintPaneEntry(lines[i], l.timelineW, protocol.Duo, i) + paint(ansiBorder, "│\r\n"))
	}
	b.WriteString(paint(ansiBorder, "├"+strings.Repeat("─", w-2)+"┤\r\n"))
	if l.preview > 0 {
		a.writePreview(b, l)
		b.WriteString(paint(ansiBorder, "├"+strings.Repeat("─", l.leftW)+"┴"+strings.Repeat("─", l.rightW)+"┤\r\n"))
	}

	a.writeFrameTail(b, w, composer)
}

// writeFrameTail draws the mode/phase status rows, the transient status line,
// the composer and the footer, which the split and timeline layouts share.
func (a *App) writeFrameTail(b *strings.Builder, w int, composer composerLayout) {
	snap := a.state.Snapshot()
	var status, second string
	if snap.EffectiveMode() == project.ModeFast {
		// Fast has no shared plan and no sign-off: show the workflow roles and the
		// verification state instead, which is the only gate before delivery.
		status = fmt.Sprintf(" Duo · %s · %s · Austin DRIVER · Tony COPILOT ", snap.EffectiveMode().Display(), snap.Phase)
		// Delivery comes before the note so a long note can never truncate it away.
		second = " Verification: " + snap.Verification.Label() + " · " + a.deliverySummary()
		if note := strings.TrimSpace(snap.Verification.Note); note != "" {
			second += " — " + strings.ReplaceAll(note, "\n", " ")
		}
	} else {
		readyA, readyT := "○", "○"
		if snap.Ready[protocol.Austin] {
			readyA = "✓"
		}
		if snap.Ready[protocol.Tony] {
			readyT = "✓"
		}
		status = fmt.Sprintf(" Duo · %s · Plan v%d · Austin %s · Tony %s ", snap.Phase, snap.PlanVersion, readyA, readyT)

		plan := strings.ReplaceAll(strings.TrimSpace(snap.Plan), "\n", " ")
		if plan == "" {
			plan = "No shared plan yet"
		}
		// Delivery is always visible and is placed before the plan text, which
		// can be arbitrarily long.
		second = " " + a.deliverySummary() + " · Plan: " + plan
	}
	b.WriteString(paint(ansiBorder, "│") + paint(ansiStatus, fit(status, w-2)) + paint(ansiBorder, "│\r\n"))
	b.WriteString(paint(ansiBorder, "│") + paint(ansiHint, fit(second, w-2)) + paint(ansiBorder, "│\r\n"))

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

// paneTitle renders a pane header, marking a scrolled pane and any unseen
// output that arrived below it while the human was reading history.
func paneTitle(name, state string, offset int, hasNew bool) string {
	title := fmt.Sprintf(" %s · %s ", name, state)
	if offset <= 0 {
		return title
	}
	mark := fmt.Sprintf("↑%d", offset)
	if hasNew {
		mark += "▼"
	}
	return " " + mark + title
}

// deliverySummary reports the durable hand-off state so it stays visible in the
// status area instead of flashing by as a system log line.
func (a *App) deliverySummary() string {
	if a.coord == nil || a.ws == nil {
		return "Delivery: unknown"
	}
	d := a.coord.CurrentDelivery()
	switch {
	case d.Applied():
		return "Delivery: applied " + shortHead(d.AppliedHead)
	case strings.TrimSpace(d.FinalHead) != "":
		return "Delivery: pending " + shortHead(d.FinalHead)
	default:
		return "Delivery: none"
	}
}

func shortHead(head string) string {
	head = strings.TrimSpace(head)
	if head == "" {
		return "(none)"
	}
	if len(head) > 10 {
		return head[:10]
	}
	return head
}

func paint(code, text string) string {
	return code + text + ansiReset
}

func paintEntry(line paneLine, width int) string {
	if line.error {
		return paint(ansiError, fit(line.text(), width))
	}
	if line.warning {
		return paint(ansiStatus, fit(line.text(), width))
	}
	var b strings.Builder
	used := 0
	for _, span := range line.spans {
		text := span.text
		if span.link != "" {
			text = hyperlink(span.link, text)
		}
		if span.style == "" {
			b.WriteString(text)
		} else {
			b.WriteString(paint(span.style, text))
		}
		used += displayWidth(span.text)
	}
	return b.String() + strings.Repeat(" ", maxInt(width-used, 0))
}

// hyperlink wraps text in an OSC 8 sequence so a terminal can make a markdown
// link clickable. The escape bytes carry no display width, so column math is
// unaffected.
func hyperlink(url, text string) string {
	if url == "" {
		return text
	}
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

func (a *App) paintPaneEntry(line paneLine, width int, agent protocol.AgentID, row int) string {
	from, to, selected := a.selectionColumns(agent, row)
	if !selected {
		return paintEntry(line, width)
	}
	var b strings.Builder
	used := 0
	for _, span := range line.spans {
		spanRunes := []rune(span.text)
		for j, r := range spanRunes {
			rw := runeWidthNear(r, runeAfter(spanRunes, j))
			if used+rw > from && used < to {
				b.WriteString(paint(ansiSelect, string(r)))
			} else if line.error {
				b.WriteString(paint(ansiError, string(r)))
			} else if line.warning {
				b.WriteString(paint(ansiStatus, string(r)))
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
	word := stateWord(connected, runtime, states...)
	// Only the states that can last a long time animate; the preview band shows
	// the plain word, so a spinner frame there cannot read as a stray border.
	switch word {
	case "tool", "thinking", "working":
		return word + " " + []string{"|", "/", "-", "\\"}[frame%4]
	}
	return word
}

// stateWord is agentState without the animation frame.
func stateWord(connected bool, runtime harness.AgentRuntime, states ...agent.ProcessState) string {
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
	if runtime.ToolDepth > 0 {
		return "tool"
	}
	if runtime.ProviderActive {
		return "thinking"
	}
	if runtime.Busy {
		return "working"
	}
	return "idle"
}

// inputCursor returns the 1-based terminal position for the input insertion
// point, using the real terminal size (never an invented minimum).
func inputCursor(width, height int, input string) (row, col int) {
	app := &App{width: width, height: height, input: []byte(input), inputPos: len(input)}
	return app.composerCursor(width, height)
}

func (a *App) writeHelp(b *strings.Builder, w, _ int) {
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

func styledPaneLinesAt(entries []entry, width, rows, offset int) []paneLine {
	return slicePaneLines(wrappedPaneLines(entries, width, false), rows, offset)
}

// slicePaneLines returns exactly `rows` lines ending `offset` lines above the
// bottom, padded at the top when the history is shorter than the pane.
func slicePaneLines(all []paneLine, rows, offset int) []paneLine {
	start := maxInt(len(all)-rows-maxInt(offset, 0), 0)
	end := minInt(start+rows, len(all))
	window := all[start:end]
	out := make([]paneLine, rows)
	copy(out[rows-len(window):], window)
	return out
}

// paneEntries returns the entries backing a pane. protocol.Duo is the system log.
func (a *App) paneEntries(agent protocol.AgentID) []entry {
	switch agent {
	case protocol.Austin:
		return a.austin
	case protocol.Tony:
		return a.tony
	default:
		return a.duo
	}
}

func (a *App) paneCacheFor(agent protocol.AgentID) (*paneCache, int) {
	switch agent {
	case protocol.Austin:
		return &a.austinCache, a.austinRev
	case protocol.Tony:
		return &a.tonyCache, a.tonyRev
	default:
		return &a.duoCache, a.duoRev
	}
}

// paneLines returns one pane's wrapped lines, rebuilding them only when the
// entries or the width changed, so a frame that only advances a spinner or
// moves a scroll offset does not re-parse every markdown block. In timeline
// mode the Duo region is the merged conversation rather than the system log.
func (a *App) paneLines(agent protocol.AgentID, width int) []paneLine {
	if a.timeline && agent == protocol.Duo {
		cache := &a.timelineCache
		if cache.lines != nil && cache.width == width && cache.rev == a.timelineRev && cache.ts == a.showTimestamps {
			return cache.lines
		}
		cache.lines = a.buildTimelineLines(width, a.showTimestamps)
		cache.width = width
		cache.rev = a.timelineRev
		cache.ts = a.showTimestamps
		return cache.lines
	}
	cache, rev := a.paneCacheFor(agent)
	if cache.lines != nil && cache.width == width && cache.rev == rev && cache.ts == a.showTimestamps {
		return cache.lines
	}
	cache.lines = wrappedPaneLines(a.paneEntries(agent), width, a.showTimestamps)
	cache.width = width
	cache.rev = rev
	cache.ts = a.showTimestamps
	return cache.lines
}

func (a *App) styledPane(agent protocol.AgentID, width, rows, offset int) []paneLine {
	return slicePaneLines(a.paneLines(agent, width), rows, offset)
}

// paneLines is retained for scrolling tests and callers that need plain text.
func paneLinesAt(entries []entry, width, rows, offset int) []entry {
	styled := styledPaneLinesAt(entries, width, rows, offset)
	out := make([]entry, len(styled))
	for i, line := range styled {
		out[i] = entry{text: line.text(), error: line.error, warning: line.warning}
	}
	return out
}

type markdownSpan struct {
	text  string
	style string
	// link is the target of a markdown link. It is emitted as an OSC 8
	// hyperlink so the label stays copyable as plain text.
	link string
}

type paneLine struct {
	spans        []markdownSpan
	continuation string
	error        bool
	warning      bool
}

func (l paneLine) text() string {
	var b strings.Builder
	for _, span := range l.spans {
		b.WriteString(span.text)
	}
	return b.String()
}

// wrappedPaneLines renders one pane: every entry becomes styled lines, and a
// dim rule separates consecutive messages so a long transcript has a visible
// boundary. Timestamps are opt-in because they cost seven columns of the pane.
func wrappedPaneLines(entries []entry, width int, timestamps bool) []paneLine {
	var all []paneLine
	for i, e := range entries {
		if i > 0 {
			all = append(all, dividerLine(width))
		}
		text := strings.TrimSpace(e.text)
		switch {
		case e.label != "":
			// A labeled entry names its sender and target, so the split panes
			// stay readable now that peer text is not mirrored into both.
			head := e.label
			if timestamps && !e.at.IsZero() {
				head += " · " + e.at.Format("15:04:05")
			}
			text = head + "\n" + text
		case timestamps && !e.at.IsZero():
			text = e.at.Format("15:04:05") + " " + text
		}
		for _, line := range markdownLines(text, width) {
			for _, wrapped := range wrapMarkdown(line, width) {
				wrapped.error = e.error
				wrapped.warning = e.warning
				all = append(all, wrapped)
			}
		}
	}
	return all
}

// timelineEntry is one message in the merged conversation, tagged with the
// speaker so the timeline can place it left (Austin), right (Tony) or neutral
// (Duo/system).
type timelineEntry struct {
	origin protocol.AgentID
	entry  entry
}

func (a *App) timelineEntries() []timelineEntry {
	out := make([]timelineEntry, 0, len(a.austin)+len(a.tony)+len(a.duo))
	for _, e := range a.austin {
		out = append(out, timelineEntry{origin: protocol.Austin, entry: e})
	}
	for _, e := range a.tony {
		out = append(out, timelineEntry{origin: protocol.Tony, entry: e})
	}
	for _, e := range a.duo {
		out = append(out, timelineEntry{origin: protocol.Duo, entry: e})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].entry.seq < out[j].entry.seq })
	return out
}

// buildTimelineLines merges every speaker's entries in true arrival order.
// The timeline is a chat: every message is anchored to the speaker's own side
// (Austin, the human and Duo system lines on the left, Tony on the right) and
// capped to the same bubble width, so the two speakers are told apart by the
// side they sit on rather than by a heavier header. When timestamps is true
// each message header carries its HH:MM:SS time; Ctrl+G toggles it.
func (a *App) buildTimelineLines(width int, timestamps bool) []paneLine {
	entries := a.timelineEntries()
	var all []paneLine
	for i, e := range entries {
		if i > 0 {
			all = append(all, dividerLine(width))
		}
		all = append(all, timelineEntryLines(e, width, timestamps)...)
	}
	return all
}

// timelineIndent is the left inset of Tony's blocks. The same value is taken
// off every speaker's bubble width, so Austin's text stops short of the right
// margin exactly as Tony's stops short of the left one.
func timelineIndent(width int) int {
	return minInt(maxInt(width/4, 4), maxInt(width/2, 0))
}

// timelineEntryLines renders one message on its speaker's side: a muted
// Sender → Receiver header above a body that is at most three quarters of the
// width. Both speakers share the header style and the bubble width; only the
// anchoring differs, which is what makes the direction readable at a glance.
func timelineEntryLines(e timelineEntry, width int, timestamps bool) []paneLine {
	bubble := maxInt(width-timelineIndent(width), 1)
	indent := 0
	if e.origin == protocol.Tony {
		indent = timelineIndent(width)
	}

	head := e.entry.label
	if head == "" {
		head = string(e.origin)
	}
	if timestamps && !e.entry.at.IsZero() {
		head += " · " + e.entry.at.Format("15:04:05")
	}

	// fit both clamps a long header and pads it to the bubble so every row still
	// measures width. Tony's header hugs the outer (right) edge of his bubble;
	// Austin's sits on the left one.
	headText := fit(head, bubble)
	if e.origin == protocol.Tony {
		headText = fit(strings.Repeat(" ", maxInt(bubble-displayWidth(head), 0))+head, bubble)
	}
	lines := []paneLine{{spans: []markdownSpan{{text: strings.Repeat(" ", indent) + headText, style: ansiHint}}}}

	prefix := strings.Repeat(" ", indent)
	body := strings.TrimSpace(e.entry.text)
	for _, line := range markdownLines(body, bubble) {
		for _, wrapped := range wrapMarkdown(line, bubble) {
			wrapped.error = e.entry.error
			wrapped.warning = e.entry.warning
			if indent > 0 {
				wrapped.spans = append([]markdownSpan{{text: prefix}}, wrapped.spans...)
			}
			lines = append(lines, wrapped)
		}
	}
	return lines
}

func dividerLine(width int) paneLine {
	if width <= 0 {
		return paneLine{}
	}
	return paneLine{spans: []markdownSpan{{text: strings.Repeat("╌", width), style: ansiHint}}}
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
			cellRunes := []rune(cell)
			for j, r := range cellRunes {
				minimums[i] = maxInt(minimums[i], runeWidthNear(r, runeAfter(cellRunes, j)))
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
					url := s[end+2 : end+2+close]
					out = append(out, markdownSpan{text: s[1:end], style: ansiLink, link: url}, markdownSpan{text: " <" + url + ">", style: ansiHint})
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
		if len(out) > 0 && out[len(out)-1].style == span.style && out[len(out)-1].link == span.link {
			out[len(out)-1].text += span.text
		} else {
			out = append(out, span)
		}
	}
	return out
}

// wrapMarkdown wraps one styled line to `width` terminal columns, preferring a
// space boundary so words are not split mid-token. A run with no usable space
// still hard-wraps, and the continuation indent counts against the width.
func wrapMarkdown(line paneLine, width int) []paneLine {
	if width <= 1 {
		return []paneLine{{}}
	}
	type cell struct {
		r     rune
		style string
		link  string
	}
	var cells []cell
	for _, span := range line.spans {
		for _, r := range span.text {
			cells = append(cells, cell{r: r, style: span.style, link: span.link})
		}
	}
	if len(cells) == 0 {
		return []paneLine{{}}
	}

	build := func(seg []cell, indent string) paneLine {
		out := paneLine{continuation: line.continuation}
		if indent != "" {
			out.spans = append(out.spans, markdownSpan{text: indent})
		}
		for _, c := range seg {
			n := len(out.spans)
			if n > 0 && out.spans[n-1].style == c.style && out.spans[n-1].link == c.link {
				out.spans[n-1].text += string(c.r)
			} else {
				out.spans = append(out.spans, markdownSpan{text: string(c.r), style: c.style, link: c.link})
			}
		}
		return out
	}
	nextCell := func(i int) rune {
		if i+1 < len(cells) {
			return cells[i+1].r
		}
		return 0
	}
	trimTrailing := func(seg []cell) []cell {
		for len(seg) > 0 {
			if r := seg[len(seg)-1].r; r == ' ' || r == '\t' {
				seg = seg[:len(seg)-1]
				continue
			}
			break
		}
		return seg
	}

	var out []paneLine
	for start := 0; start < len(cells); {
		indent := ""
		limit := width
		if len(out) > 0 {
			indent = line.continuation
			if displayWidth(indent) >= width {
				indent = strings.Repeat(" ", width-1)
			}
			if limit = width - displayWidth(indent); limit < 1 {
				indent, limit = "", width
			}
		}

		used := 0
		i := start
		lastSpace := -1
		for i < len(cells) {
			rw := runeWidthNear(cells[i].r, nextCell(i))
			if used+rw > limit {
				break
			}
			used += rw
			if cells[i].r == ' ' || cells[i].r == '\t' {
				lastSpace = i
			}
			i++
		}
		if i >= len(cells) {
			out = append(out, build(cells[start:i], indent))
			break
		}
		end := i
		switch {
		case i < len(cells) && (cells[i].r == ' ' || cells[i].r == '\t'):
			// The next character is a space, so the line already ends on a word
			// boundary: break exactly here instead of backing up to an earlier one.
			end = i
		case lastSpace > start && lastSpace-start >= limit/2:
			// Break at the last space only when it is far enough along to avoid a
			// tiny line; otherwise hard-wrap the overlong token.
			end = lastSpace + 1
		}
		if end <= start {
			end = i
		}
		out = append(out, build(trimTrailing(cells[start:end]), indent))
		start = end
		for start < len(cells) && (cells[start].r == ' ' || cells[start].r == '\t') {
			start++
		}
	}
	if len(out) == 0 {
		out = append(out, paneLine{})
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
		nextValue := rune(0)
		if i+size < len(input) {
			nextValue, _ = utf8.DecodeRune(input[i+size:])
		}
		rw := runeWidthNear(r, nextValue)
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

// layout is the fixed geometry of one frame. Rows are 1-based terminal rows,
// matching the cursor moves written by writeLayout.
type layout struct {
	leftW     int // Austin pane content columns (also the header's left half)
	rightW    int // Tony pane content columns (also the header's right half)
	content   int // pane content rows, or timeline rows in timeline mode
	preview   int // work preview rows including its header row; 0 when hidden
	logW      int // system log content columns (split layout)
	logFirst  int // first system log row (split layout)
	logLast   int // last system log row (split layout)
	timelineW int // conversation timeline content columns (timeline layout)
}

func (a *App) layout() layout { return a.layoutFor(a.width, a.height) }

func (a *App) layoutFor(w, h int) layout {
	composer := a.composerLayout(w)
	left := (w - 1) / 2
	right := w - 1 - left
	preview := a.previewRowsFor(h, composer.rows)
	// A visible band also needs the separator that closes it before the Duo
	// status rows.
	band := 0
	if preview > 0 {
		band = 1
	}
	if a.timeline {
		// header + timeline + separator + preview band + two status rows +
		// status line + composer + footer = content + preview + band + composer + 7,
		// which leaves the last terminal row unused like the split layout.
		content := maxInt(h-7-preview-band-composer.rows, 0)
		return layout{
			leftW:     maxInt(left-1, 0),
			rightW:    maxInt(right-1, 0),
			content:   content,
			preview:   preview,
			timelineW: maxInt(w-2, 1),
		}
	}
	content := maxInt(h-7-duoLogRows-composer.rows-preview-band, 0)
	return layout{
		leftW:    maxInt(left-1, 0),
		rightW:   maxInt(right-1, 0),
		content:  content,
		preview:  preview,
		logW:     maxInt(w-4, 1),
		logFirst: 5 + content + preview + band,
		logLast:  4 + content + duoLogRows + preview + band,
	}
}

func (a *App) paneRows() (leftW, rightW, rows int) {
	l := a.layout()
	return l.leftW, l.rightW, l.content
}

func (a *App) hitPane(x, y int) protocol.AgentID {
	l := a.layout()
	if a.timeline {
		if y >= 2 && y <= l.content+1 && x >= 2 && x <= a.width-1 {
			return protocol.Duo
		}
		return ""
	}
	if y >= 2 && y <= l.content+1 {
		switch {
		case x >= 2 && x <= l.leftW+1:
			return protocol.Austin
		case x >= l.leftW+3 && x <= a.width-1:
			return protocol.Tony
		}
		return ""
	}
	if y >= l.logFirst && y <= l.logLast && x >= 2 && x <= a.width-1 {
		return protocol.Duo
	}
	return ""
}

func (a *App) maxPaneOffset(agent protocol.AgentID, width, rows int) int {
	return maxInt(len(a.paneLines(agent, width))-rows, 0)
}

// stickyScroll keeps a scrolled pane on the same content while new output is
// appended: it grows the bottom-relative offset by the number of new wrapped
// lines. A pane pinned to the bottom (offset 0) is untouched and keeps
// following new output. `hadNew` preserves the "new output below" flag until
// the human scrolls again.
func stickyScroll(offset, seen, total, rows int, hadNew bool) (int, int, bool) {
	grew := offset > 0 && seen > 0 && total > seen
	if grew {
		offset += total - seen
	}
	offset = minInt(maxInt(offset, 0), maxInt(total-rows, 0))
	return offset, total, (hadNew || grew) && offset > 0
}

func (a *App) clampPaneOffsets() {
	l := a.layout()
	if a.timeline {
		a.duoOffset, a.duoSeen, a.duoNew = stickyScroll(a.duoOffset, a.duoSeen, len(a.paneLines(protocol.Duo, l.timelineW)), l.content, a.duoNew)
		return
	}
	a.austinOffset, a.austinSeen, a.austinNew = stickyScroll(a.austinOffset, a.austinSeen, len(a.paneLines(protocol.Austin, l.leftW)), l.content, a.austinNew)
	a.tonyOffset, a.tonySeen, a.tonyNew = stickyScroll(a.tonyOffset, a.tonySeen, len(a.paneLines(protocol.Tony, l.rightW)), l.content, a.tonyNew)
	a.duoOffset, a.duoSeen, a.duoNew = stickyScroll(a.duoOffset, a.duoSeen, len(a.paneLines(protocol.Duo, l.logW)), duoLogRows, a.duoNew)
}

func (a *App) clampOffsets() {
	if a.view == viewHelp {
		a.clampHelpOffset()
	}
	if a.view == viewDetail {
		a.clampDetailOffset()
	}
	if a.width >= minWidth && a.height >= minHeight {
		a.clampPaneOffsets()
	}
}

func (a *App) scrollPane(agent protocol.AgentID, delta int) {
	l := a.layout()
	var offset *int
	var fresh *bool
	var width, rows int
	switch agent {
	case protocol.Austin:
		offset, fresh, width, rows = &a.austinOffset, &a.austinNew, l.leftW, l.content
	case protocol.Tony:
		offset, fresh, width, rows = &a.tonyOffset, &a.tonyNew, l.rightW, l.content
	case protocol.Duo:
		if a.timeline {
			offset, fresh, width, rows = &a.duoOffset, &a.duoNew, l.timelineW, l.content
		} else {
			offset, fresh, width, rows = &a.duoOffset, &a.duoNew, l.logW, duoLogRows
		}
	default:
		return
	}
	*offset = minInt(maxInt(*offset+delta, 0), a.maxPaneOffset(agent, width, rows))
	*fresh = false
}

func wrap(s string, width int) []string {
	if width <= 1 {
		return []string{""}
	}
	if s == "" {
		return []string{""}
	}
	runes := []rune(s)
	var out []string
	var b strings.Builder
	used := 0
	for i, r := range runes {
		rw := runeWidthNear(r, runeAfter(runes, i))
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
	runes := []rune(s)
	var b strings.Builder
	used := 0
	for i, r := range runes {
		rw := runeWidthNear(r, runeAfter(runes, i))
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

func displayWidth(s string) int {
	runes := []rune(s)
	n := 0
	for i, r := range runes {
		n += runeWidthNear(r, runeAfter(runes, i))
	}
	return n
}

// runeAfter returns the rune after index i, or 0 at the end. It exists so a
// variation selector can resolve the width of the pictograph before it.
func runeAfter(runes []rune, i int) rune {
	if i+1 < len(runes) {
		return runes[i+1]
	}
	return 0
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
