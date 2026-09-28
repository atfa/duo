package tui

import "github.com/atfa/duo/internal/protocol"

// This file owns pane scrolling and focus bookkeeping.
//
// The pane offset fields (austinOffset / tonyOffset) and the follow/unread
// counters are mutated here and nowhere else, so there is exactly one place to
// reason about "what is the user looking at".
//
// Layout geometry (paneRows) stays in render.go: this file asks the layout for
// its width/rows rather than recomputing them, so the two cannot drift apart.

// paneFocus identifies which region owns the keyboard.
type paneFocus int

const (
	// focusComposer means the input line owns keystrokes. This is the default.
	focusComposer paneFocus = iota
	focusAustin
	focusTony
)

// focusTarget maps the current focus to an agent id, returning "" for the
// composer. Render code uses this to decide which pane is highlighted.
func (a *App) focusTarget() protocol.AgentID {
	switch a.focus {
	case focusAustin:
		return protocol.Austin
	case focusTony:
		return protocol.Tony
	default:
		return ""
	}
}

// paneFocused reports whether the given agent's pane currently owns the keyboard.
func (a *App) paneFocused(agent protocol.AgentID) bool {
	return a.focusTarget() == agent && agent != ""
}

// paneFollowing reports whether the given pane is pinned to its newest line.
func (a *App) paneFollowing(agent protocol.AgentID) bool {
	return a.paneOffset(agent) == 0
}

// paneOffset returns how many wrapped lines the pane is scrolled back from newest.
func (a *App) paneOffset(agent protocol.AgentID) int {
	if agent == protocol.Austin {
		return a.austinOffset
	}
	return a.tonyOffset
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
