package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/atfa/duo/internal/agent"
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
	// The frame budget outside the content is 11 rows in the split layout and 6
	// in the timeline layout, plus the band and its separator; shrink the band
	// first, then hide it.
	fixed := 11
	if a.timeline {
		fixed = 7
	}
	if spare := h - fixed - composerRows - 1 - previewMinPaneRows; rows > spare {
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
	b.WriteString(paint(a.frameColor(), "│") + paint(ansiTitle, header(left, l.leftW)) + paint(a.frameColor(), "│") + paint(ansiTitle, header(right, l.rightW)) + paint(a.frameColor(), "│\r\n"))

	lbody := a.previewBody(protocol.Austin, l.leftW, l.preview-1)
	rbody := a.previewBody(protocol.Tony, l.rightW, l.preview-1)
	for i := 0; i < l.preview-1; i++ {
		b.WriteString(paint(a.frameColor(), "│") + paint(ansiHint, fit(lbody[i], l.leftW)) + paint(a.frameColor(), "│") + paint(ansiHint, fit(rbody[i], l.rightW)) + paint(a.frameColor(), "│\r\n"))
	}
}

// previewHeader is the band's title: the same state word the pane header used —
// animated by the spinner frame, which is now the only place the live state
// appears — plus how long the current turn has been running, which is what tells
// a human whether a long turn is still moving or stuck. A narrow half drops the
// turn age and then the state rather than letting header() clip them mid-word.
func (a *App) driverName(agent protocol.AgentID) string {
	if a.agents != nil {
		return a.agents.DriverTypeFor(agent)
	}
	return "pi"
}

func (a *App) isConnected(id protocol.AgentID) bool {
	if a.server != nil && a.server.IsConnected(id) {
		return true
	}
	// pi always reports itself over the bridge. The other drivers may not have a
	// usable bridge yet: agy has none at all, and opencode's only attaches once
	// its TUI has a session. A running process is still a present agent, and
	// showing it as "connecting" hid the fact that it was ready to work.
	return a.driverName(id) != "pi" && a.processState(id) == agent.ProcessRunning
}

func (a *App) previewHeader(agent protocol.AgentID, width int) string {
	rt := a.tracker.Snapshot(agent)
	state := agentState(a.isConnected(agent), rt, a.frame, a.processState(agent))
	available := width - displayWidth("[↗] ")
	drv := a.driverName(agent)
	model := a.modelForAgent(agent)

	tag := drv
	if model != "" {
		tag = fmt.Sprintf("%s · %s", drv, model)
	}

	age := elapsedSince(rt.TurnStarted)
	hasAge := age != "unknown"

	var candidates []string
	if hasAge {
		candidates = append(candidates, fmt.Sprintf(" %s preview (%s) · %s · %s ", agent, tag, state, age))
	}
	candidates = append(candidates, fmt.Sprintf(" %s preview (%s) · %s ", agent, tag, state))

	var shortTag string
	if idx := strings.LastIndex(model, "/"); idx != -1 && idx+1 < len(model) {
		shortTag = fmt.Sprintf("%s · %s", drv, model[idx+1:])
		if hasAge {
			candidates = append(candidates, fmt.Sprintf(" %s preview (%s) · %s · %s ", agent, shortTag, state, age))
		}
		candidates = append(candidates, fmt.Sprintf(" %s preview (%s) · %s ", agent, shortTag, state))
	}

	if hasAge {
		candidates = append(candidates, fmt.Sprintf(" %s preview (%s) · %s · %s ", agent, drv, state, age))
	}
	candidates = append(candidates, fmt.Sprintf(" %s preview (%s) · %s ", agent, drv, state))
	candidates = append(candidates, fmt.Sprintf(" %s preview (%s) ", agent, tag))
	if shortTag != "" {
		candidates = append(candidates, fmt.Sprintf(" %s preview (%s) ", agent, shortTag))
	}
	candidates = append(candidates, fmt.Sprintf(" %s preview (%s) ", agent, drv))

	// Narrow fallbacks without "preview"
	if hasAge {
		candidates = append(candidates, fmt.Sprintf(" %s (%s) · %s · %s ", agent, tag, state, age))
	}
	candidates = append(candidates, fmt.Sprintf(" %s (%s) · %s ", agent, tag, state))
	if shortTag != "" {
		if hasAge {
			candidates = append(candidates, fmt.Sprintf(" %s (%s) · %s · %s ", agent, shortTag, state, age))
		}
		candidates = append(candidates, fmt.Sprintf(" %s (%s) · %s ", agent, shortTag, state))
	}
	candidates = append(candidates, fmt.Sprintf(" %s (%s) · %s ", agent, drv, state))
	candidates = append(candidates, fmt.Sprintf(" %s (%s) ", agent, tag))
	if shortTag != "" {
		candidates = append(candidates, fmt.Sprintf(" %s (%s) ", agent, shortTag))
	}
	candidates = append(candidates, fmt.Sprintf(" %s (%s) ", agent, drv))

	usage := previewUsage(rt)
	if usage != "" {
		for _, c := range candidates {
			withUsage := strings.TrimRight(c, " ") + " · " + usage + " "
			if displayWidth(withUsage) <= available {
				return withUsage
			}
		}
	}
	for _, c := range candidates {
		if displayWidth(c) <= available {
			return c
		}
	}
	return fmt.Sprintf(" %s (%s) ", agent, drv)
}

func previewUsage(rt harness.AgentRuntime) string {
	var parts []string
	if rt.ContextWindow > 0 {
		parts = append(parts, fmt.Sprintf("ctx %s/%s", compactTokenCount(rt.ContextTokens), compactTokenCount(rt.ContextWindow)))
	}
	if (rt.ProviderActive || rt.Busy) && rt.ToolDepth == 0 && rt.TokensPerSecond > 0 && !rt.TokenSpeedAt.IsZero() && time.Since(rt.TokenSpeedAt) < 2*time.Second {
		parts = append(parts, fmt.Sprintf("%.0f tok/s", rt.TokensPerSecond))
	}
	return strings.Join(parts, " · ")
}

func compactTokenCount(tokens int) string {
	switch {
	case tokens >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
	case tokens >= 10_000:
		return fmt.Sprintf("%.0fk", float64(tokens)/1_000)
	case tokens >= 1_000:
		return fmt.Sprintf("%.1fk", float64(tokens)/1_000)
	default:
		return fmt.Sprintf("%d", tokens)
	}
}

// previewBody renders the work detail of one agent: what it is doing now, the
// turn's recent tool trail, the turn's last error, and the tail of the text
// being streamed. Rows are spent in that order so the newest state wins when the
// band is short.
func (a *App) previewBody(agent protocol.AgentID, width, rows int) []string {
	if rows < 1 {
		return nil
	}
	rt := a.tracker.Snapshot(agent)
	out := []string{a.previewNow(agent, rt)}
	remaining := rows - 1

	if remaining > 0 {
		if trail := previewTrail(rt, width); trail != "" {
			out = append(out, trail)
			remaining--
		}
	}
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

// previewNow is the one line that answers "what is it doing right now": the
// running tool and how long it has been running, or the model it is thinking
// with, or how long it has been quiet. A just-updated line carries no age, so
// the row stays a statement instead of always ending in "now".
func (a *App) previewNow(id protocol.AgentID, rt harness.AgentRuntime) string {
	tool := rt.Tool
	if tool == "" {
		tool = "tool"
	}
	switch {
	case rt.ToolFailed:
		line := "✗ " + tool
		if rt.ToolDetail != "" {
			line += " · " + oneLine(rt.ToolDetail)
		}
		return line
	case rt.ToolDepth > 0:
		line := "▶ " + tool
		if age := elapsedSince(rt.ToolStarted); age != "unknown" && age != "now" {
			line += " " + age
		}
		if rt.ToolDetail != "" {
			line += " · " + oneLine(rt.ToolDetail)
		}
		return line
	case rt.ProviderActive:
		label := a.modelLabel(id)
		if label == "" {
			label = "thinking"
		}
		return "◆ " + label + ageSuffix(rt.LastActivity)
	case rt.Busy:
		return "▶ working" + ageSuffix(rt.LastActivity)
	default:
		// A dead process is not "waiting": saying so hides the one fact that
		// explains why the agent never connected or stopped responding.
		switch a.processState(id) {
		case agent.ProcessExited:
			return "· process exited — check the agent log, then Restart"
		case agent.ProcessFailed:
			return "· process failed — check the agent log, then Restart"
		case agent.ProcessStopping:
			return "· stopping"
		}
		if age := elapsedSince(rt.LastActivity); age != "unknown" && age != "now" {
			return "· waiting · quiet " + age
		}
		return "· waiting"
	}
}

// modelLabel names the model an agent is thinking with, shortened to the part
// after the provider because the provider repeats in both panes. It returns ""
// until the bridge has reported a model.
func (a *App) modelLabel(agent protocol.AgentID) string {
	label := a.modelForAgent(agent)
	if label == "" {
		return ""
	}
	if i := strings.LastIndex(label, "/"); i >= 0 && i+1 < len(label) {
		label = label[i+1:]
	}
	if level := a.currentThinking[agent]; level != "" {
		label += " · " + level
	}
	return label
}

// ageSuffix renders "how long ago" only when it says something.
func ageSuffix(at time.Time) string {
	if age := elapsedSince(at); age != "unknown" && age != "now" {
		return " · " + age
	}
	return ""
}

// previewTrail is the turn's recent tool history, newest last. The tool count
// and the newest calls are kept while the older ones are dropped from the left,
// so a narrow half loses history instead of the current state.
func previewTrail(rt harness.AgentRuntime, width int) string {
	if len(rt.Recent) == 0 {
		return ""
	}
	const sep = " · "
	budget := width - 1
	// Newest first while choosing, oldest first when rendering, so the newest
	// call survives a narrow half and only history is dropped.
	pieces := make([]string, 0, len(rt.Recent))
	for i := len(rt.Recent) - 1; i >= 0; i-- {
		pieces = append(pieces, trailPiece(rt.Recent[i]))
	}
	head := ""
	if rt.Tools > 0 {
		head = fmt.Sprintf("%d tools", rt.Tools)
	}
	for kept := len(pieces); kept >= 1; kept-- {
		chosen := make([]string, 0, kept+1)
		if head != "" {
			chosen = append(chosen, head)
		}
		for i := kept - 1; i >= 0; i-- {
			chosen = append(chosen, pieces[i])
		}
		if line := strings.Join(chosen, sep); displayWidth(line) <= budget {
			return line
		}
	}
	// One call alone does not fit: show it cut rather than lose it entirely. The
	// count gives way first, because it is the least specific fact here.
	return strings.TrimRight(fit(pieces[0], budget), " ")
}

// trailPiece is one tool of the trail: outcome, name, how long it took, and for
// a failure the reason, which is the part that explains a stuck turn.
func trailPiece(note harness.ToolNote) string {
	mark := "✓"
	if !note.OK {
		mark = "✗"
	}
	piece := mark + " " + note.Name
	if note.Duration >= time.Second {
		piece += " " + shortDuration(note.Duration)
	}
	if !note.OK && note.Detail != "" {
		if reason := strings.TrimSpace(fit(oneLine(note.Detail), 24)); reason != "" {
			piece += " " + reason
		}
	}
	return piece
}

// shortDuration is elapsedSince for a completed call, which has no "now".
func shortDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return "<1s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
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
