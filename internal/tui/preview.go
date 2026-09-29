package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/protocol"
)

// previewMinHeight is the smallest terminal where the work preview is worth its
// rows: below it the panes would lose more than the preview adds.
const previewMinHeight = 28

// previewRowsFor returns how many rows the preview band occupies, including its
// header row, or 0 when it is hidden or does not fit. The band never pushes the
// panes below previewMinPaneRows.
func (a *App) previewRowsFor(h, composerRows int) int {
	if a.hidePreview || h < previewMinHeight {
		return 0
	}
	const previewMinPaneRows = 6
	// Three rows (header, activity, tail) is the useful minimum; taller
	// terminals get a little more tail, but the panes are still the priority.
	rows := minInt(maxInt(h/6, 3), 6)
	// The frame budget outside the panes is 11 rows plus the band and its
	// separator; shrink the band first, then hide it.
	if spare := h - 11 - composerRows - 1 - previewMinPaneRows; rows > spare {
		rows = spare
	}
	if rows < 3 {
		return 0
	}
	return rows
}

// writePreview renders the band rows for both agents: one header row and the
// body rows below it. The caller has already drawn the separator that closes
// the panes, which doubles as the band's top border.
func (a *App) writePreview(b *strings.Builder, l layout) {
	left := a.previewHeader(protocol.Austin, l.leftW)
	right := a.previewHeader(protocol.Tony, l.rightW)
	b.WriteString(paint(ansiBorder, "│") + paint(ansiTitle, header(left, l.leftW)) + paint(ansiBorder, "│") + paint(ansiTitle, header(right, l.rightW)) + paint(ansiBorder, "│\r\n"))

	lbody := a.previewBody(protocol.Austin, l.leftW, l.preview-1)
	rbody := a.previewBody(protocol.Tony, l.rightW, l.preview-1)
	for i := 0; i < l.preview-1; i++ {
		b.WriteString(paint(ansiBorder, "│") + paint(ansiHint, fit(lbody[i], l.leftW)) + paint(ansiBorder, "│") + paint(ansiHint, fit(rbody[i], l.rightW)) + paint(ansiBorder, "│\r\n"))
	}
}

// previewHeader is the band's title: the same state word the pane header uses,
// plus how long the agent has been quiet, which is what tells a human whether a
// long turn is still moving or stuck. A narrow half drops the elapsed time and
// then the state rather than letting header() clip them mid-word, and the pane
// header above always carries the full state.
func (a *App) previewHeader(agent protocol.AgentID, width int) string {
	rt := a.tracker.Snapshot(agent)
	state := agentState(a.server.IsConnected(agent), rt, a.frame, a.processState(agent))
	available := width - displayWidth("[↗] ")
	full := fmt.Sprintf(" %s preview · %s · %s ", agent, state, elapsedSince(rt.LastActivity))
	if displayWidth(full) <= available {
		return full
	}
	if short := fmt.Sprintf(" %s · %s ", agent, state); displayWidth(short) <= available {
		return short
	}
	return fmt.Sprintf(" %s ", agent)
}

// previewBody renders the work detail: the current tool and its arguments, the
// last error of this turn, and the tail of the text being streamed. The newest
// text is kept when the rows run out, so the band always shows the latest state.
func (a *App) previewBody(agent protocol.AgentID, width, rows int) []string {
	if rows < 1 {
		return nil
	}
	rt := a.tracker.Snapshot(agent)
	out := []string{previewActivity(rt)}
	remaining := rows - 1

	if remaining > 0 && rt.LastError != "" {
		lines := wrap("✗ "+oneLine(rt.LastError), width)
		if len(lines) > remaining {
			lines = lines[:remaining]
		}
		out = append(out, lines...)
		remaining -= len(lines)
	}
	if remaining > 0 && rt.StreamTail != "" {
		lines := wrap("▸ "+oneLine(rt.StreamTail), width)
		if len(lines) > remaining {
			lines = lines[len(lines)-remaining:]
			// Mark the cut so a mid-word fragment does not look like something the
			// agent actually wrote.
			lines[0] = "…" + strings.TrimLeft(lines[0], " ")
		}
		out = append(out, lines...)
	}
	for len(out) < rows {
		out = append(out, "")
	}
	return out
}

// previewActivity is the one line that answers "what is it doing right now".
func previewActivity(rt harness.AgentRuntime) string {
	tool := rt.Tool
	if tool == "" {
		tool = "tool"
	}
	if rt.ToolDetail != "" {
		tool += " · " + oneLine(rt.ToolDetail)
	}
	switch {
	case rt.ToolFailed:
		return "✗ " + tool
	case rt.ToolDepth > 0:
		return "▶ " + tool
	case rt.ProviderActive:
		return "◆ model thinking"
	case rt.Busy:
		return "▶ working"
	default:
		return "· waiting"
	}
}

// oneLine flattens detail to a single line: PTY-free preview rows must never
// depend on embedded newlines.
func oneLine(text string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(text), " "))
}

// elapsedSince formats how long ago an agent last reported any activity.
func elapsedSince(at time.Time) string {
	if at.IsZero() {
		return "unknown"
	}
	d := time.Since(at)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Second:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
