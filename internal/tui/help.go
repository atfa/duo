package tui

import "strings"

type viewMode int

const (
	viewMain viewMode = iota
	viewHelp
)

type keyBinding struct {
	Keys        string
	ShortLabel  string
	Description string
}

var keyBindings = []keyBinding{
	{"Enter", "Enter Send", "Send task/message to Austin"},
	{"Ctrl+A", "Ctrl+A/T Native", "Open Austin native Pi"},
	{"Ctrl+T", "", "Open Tony native Pi"},
	{"Ctrl+]", "", "Return from native Pi to Duo"},
	{"Ctrl+\\", "", "Return from native Pi to Duo"},
	{"Ctrl+R", "", "Restart Austin if exited/failed"},
	{"Ctrl+Y", "", "Restart Tony if exited/failed"},
	{"Ctrl+/", "Ctrl+/ Help", "Toggle Help"},
	{"Ctrl+Q", "Ctrl+Q Quit", "Quit Duo and preserve session"},
	{"Backspace", "", "Edit composer"},
}

func composerPrefix() string { return " Duo → Austin > " }

func mainFooter() string {
	var labels []string
	for _, binding := range keyBindings {
		if binding.ShortLabel != "" {
			labels = append(labels, binding.ShortLabel)
		}
	}
	return " " + strings.Join(labels, " · ") + " "
}

func (a *App) helpLines(width int) []string {
	if width < 1 {
		return nil
	}
	sections := []struct {
		title string
		lines []string
	}{
		{"Quick Start", []string{
			"1. Type a task in the Duo composer and press Enter.",
			"2. The human composer sends to Austin by default.",
			"3. Austin wakes Tony through duo_send in a fresh task.",
			"4. Austin and Tony work independently in private Git worktrees.",
			"5. Duo Core manages phase and delivery.",
		}},
		{"Keyboard", helpKeyboardLines()},
		{"Collaboration Lifecycle", []string{
			"PLAN → EXECUTE → REVIEW → INTEGRATE → DONE",
			"PLAN: agree on shared plan.",
			"EXECUTE: work independently in private worktrees.",
			"REVIEW: cross-review peer commit.",
			"INTEGRATE: merge into Austin integration branch and final review.",
			"DONE: final approved artifact has been delivered back to original repository.",
		}},
		{"Native Pi", []string{
			"Ctrl+A → Austin native Pi; Ctrl+T → Tony native Pi.",
			"In native Pi, /model, /settings, /tree, Pi extensions, and Pi shortcuts are handled by Pi.",
			"Return to Duo with Ctrl+] or Ctrl+\\.",
		}},
		{"Resume & Recovery", []string{
			"duo --resume", "duo --resume <session-id>",
			"Resume restores Duo state, worktrees, Pi conversation identity, working scope, and collaboration wake-up.",
		}},
		{"Delivery", []string{
			"DONE means the final integrated artifact has been delivered to the original repository.",
			"If delivery is blocked: duo apply or duo apply <session-id>. Delivery is fast-forward only.",
		}},
		{"Working Scope", []string{
			"cd repo/packages/web", "duo",
			"Git boundary: repo. Agent default cwd: repo/packages/web.",
			"Scope is a default working directory, not a filesystem sandbox.",
		}},
		{"Agent Status", []string{
			"Austin and Tony headers show connection, process, and working state. Duo phase, plan, and transient feedback remain below the panes.",
		}},
	}
	var out []string
	for _, section := range sections {
		out = append(out, section.title, "")
		for _, line := range section.lines {
			out = append(out, wrap(line, width)...)
		}
		out = append(out, "")
	}
	return out
}

func helpKeyboardLines() []string {
	var lines []string
	for _, binding := range keyBindings {
		lines = append(lines, "  "+binding.Keys+strings.Repeat(" ", maxInt(12-displayWidth(binding.Keys), 1))+binding.Description)
	}
	return append(lines, "", "Help navigation:", "  ↑ / k        Scroll up", "  ↓ / j        Scroll down", "  PgUp         Previous page", "  PgDn         Next page", "  Home / g     Top", "  End / G      Bottom", "  Esc          Close Help", "  Ctrl+/       Close Help", "  Ctrl+Q       Quit Duo")
}

func (a *App) helpVisibleRows() int { return maxInt(a.height-4, 1) }

func (a *App) maxHelpOffset() int {
	return maxInt(len(a.helpLines(maxInt(a.width-2, 1)))-a.helpVisibleRows(), 0)
}

func (a *App) clampHelpOffset() {
	if a.helpOffset < 0 {
		a.helpOffset = 0
	}
	if max := a.maxHelpOffset(); a.helpOffset > max {
		a.helpOffset = max
	}
}

func (a *App) scrollHelp(delta int) {
	a.helpOffset += delta
	a.clampHelpOffset()
}
