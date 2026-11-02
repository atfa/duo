package xduo

import "strings"

// StatusLeftLength keeps the session label readable; tmux truncates status-left
// at 10 columns by default, which turned the session name into "[xduo-3309".
const StatusLeftLength = "40"

// StatusRightText is the instance hint. #{prefix} renders the user's actual
// tmux prefix, so the hint stays correct for custom prefix keys.
const StatusRightText = "#{prefix} + Shift+L: back to xduo"

// StatusStyle returns the session-scoped tmux options that make the status bar
// match Duo's dark palette. tmux defaults to a green status bar, which clashes
// with Duo's terminal UI. Only the xduo session is restyled, so the user's
// global tmux configuration and unrelated sessions keep their own colours.
func StatusStyle() [][2]string {
	return [][2]string{
		{"status-style", "bg=#141b22,fg=#8b98a8"},
		{"window-status-style", "bg=#141b22,fg=#8b98a8"},
		{"window-status-current-style", "bg=#1d262d,fg=#6fdac1,bold"},
		{"window-status-activity-style", "bg=#141b22,fg=#fbbf24"},
		{"window-status-bell-style", "bg=#141b22,fg=#f87171"},
	}
}

// StatusContent returns the status bar text that replaces tmux's raw session
// name and single-window list: a readable label plus a window list that only
// appears once a session actually holds more than one window. xduo sessions are
// single-window Duo terminals, where "0:sh*" only ever meant "this is the only
// window" — the index, the shell name and the * marker carried no information.
func StatusContent(label string) [][2]string {
	return [][2]string{
		{"status-left", "#[fg=#6fdac1,bold]" + escapeStatusText(label) + "#[fg=#8b98a8] "},
		{"window-status-format", "#{?#{==:#{session_windows},1},,#{window_index}:#{window_name} }"},
		{"window-status-current-format", "#{?#{==:#{session_windows},1},,#[fg=#6fdac1,bold]#{window_index}:#{window_name}#[fg=#8b98a8,nobold] }"},
	}
}

// StatusHint returns the right-hand options that tell the user how to get back
// to the xduo manager.
func StatusHint() [][2]string {
	return [][2]string{
		{"status-right-length", "60"},
		{"status-right", StatusRightText},
	}
}

// escapeStatusText keeps a label literal: "#" introduces tmux formats, so a
// project directory containing "#{...}" would otherwise be expanded.
func escapeStatusText(text string) string {
	return strings.ReplaceAll(text, "#", "##")
}
