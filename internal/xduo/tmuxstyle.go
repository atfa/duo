package xduo

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
