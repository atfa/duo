package tui

import (
	"fmt"
	"strings"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/workspace"
)

type viewMode int

const (
	viewMain viewMode = iota
	viewHelp
	viewDetail
	viewModel
)

type keyBinding struct {
	Keys        string
	ShortLabel  string
	Description string
}

var keyBindings = []keyBinding{
	{"Enter", "Enter Send", "Send task/message to Austin"},
	{"Ctrl/Shift+Enter", "Ctrl/Shift+Enter Newline", "Insert a newline in the composer"},
	{"Ctrl+A", "Ctrl+A/T Native", "Open Austin native Pi"},
	{"Ctrl+T", "", "Open Tony native Pi"},
	{"Ctrl+]", "", "Return from native Pi to Duo"},
	{"Ctrl+\\", "", "Return from native Pi to Duo"},
	{"Ctrl+】", "", "Return from native Pi to Duo"},
	{"Ctrl+R", "", "Restart Austin if exited/failed"},
	{"Ctrl+Y", "", "Restart Tony if exited/failed"},
	{"Ctrl+/", "Ctrl+/ Help", "Toggle Help"},
	{"Ctrl+O", "", "Show the full session, worktrees, verification or plan, and delivery"},
	{"Ctrl+M / Alt+M", "", "Choose the Pi model and thinking level for Austin or Tony; in the picker Tab switches agent, Space applies the model and keeps it open, Enter applies and closes, Shift+Tab cycles thinking"},
	{"Ctrl+G", "", "Toggle message timestamps"},
	{"Ctrl+Q", "Ctrl+Q Quit", "Quit Duo and preserve session"},
	{"← / →", "", "Move the composer cursor"},
	{"Alt+←/→", "", "Move the composer cursor by word"},
	{"↑ / ↓", "", "Move between composer lines; recall task history at the first/last line"},
	{"Home / End", "", "Move to the start or end of the composer line"},
	{"Ctrl+U / Ctrl+K", "", "Delete to the start or end of the composer line"},
	{"Ctrl+W", "", "Delete the previous word"},
	{"Backspace", "", "Delete the previous composer character"},
	{"Mouse wheel", "", "Scroll Austin, Tony or the system log history"},
	{"Mouse drag", "", "Select pane text; copies on release (macOS)"},
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
	quickStart := []string{
		"1. Type a task in the Duo composer and press Enter.",
		"2. The human composer sends to Austin by default.",
		"3. Austin wakes Tony through duo_send in a fresh task.",
		"4. Austin and Tony work independently in private Git worktrees.",
		"5. Duo Core manages phase and delivery.",
	}
	lifecycle := []string{
		"PLAN → EXECUTE → REVIEW → INTEGRATE → DONE",
		"PLAN: agree on shared plan.",
		"EXECUTE: work independently in private worktrees.",
		"REVIEW: cross-review peer commit.",
		"INTEGRATE: merge into Austin integration branch and final review.",
		"DONE: final approved artifact has been delivered back to original repository.",
	}
	if a.state.Snapshot().EffectiveMode() == project.ModeFast {
		quickStart = []string{
			"1. Type a task in the Duo composer and press Enter.",
			"2. Austin drives the task in its private Git worktree.",
			"3. Tony is a read-only copilot: it advises and independently verifies, and never commits.",
			"4. When Austin is done, it requests verification with duo_set_status ready=true.",
			"5. Tony reports passed or issue_found with duo_set_verification.",
			"6. Duo Core delivers the verified artifact back to the original repository.",
		}
		lifecycle = []string{
			"RUNNING → VERIFY → DONE (Fast mode)",
			"RUNNING: Austin drives; Tony advises on request.",
			"VERIFY: Austin requested verification of one exact HEAD.",
			"  passed → Duo delivers the artifact → DONE.",
			"  issue_found → back to RUNNING with a concrete issue for Austin.",
			"DONE: the verified artifact has been delivered to the original repository.",
		}
	}
	sections := []struct {
		title string
		lines []string
	}{
		{"Quick Start", quickStart},
		{"Mode", []string{
			"FAST (default): Austin drives, Tony independently verifies. No shared plan, no dual sign-off.",
			"GOAL (duo --mode goal): shared plan + independent work + cross-review + dual sign-off.",
			"Mode is fixed for a session's lifetime; --resume always uses the persisted mode.",
		}},
		{"Keyboard", helpKeyboardLines()},
		{"Collaboration Lifecycle", lifecycle},
		{"Native Pi", []string{
			"Ctrl+A → Austin native Pi; Ctrl+T → Tony native Pi.",
			"In native Pi, /model, /settings, /tree, Pi extensions, and Pi shortcuts are handled by Pi.",
			"Return to Duo with Ctrl+], Ctrl+\\ or Ctrl+】.",
		}},
		{"Resume & Recovery", []string{
			"duo --resume", "duo --resume <session-id>",
			"Resume restores Duo state, worktrees, Pi conversation identity, working scope, pane history, and collaboration wake-up.",
		}},
		{"Delivery", []string{
			"DONE means the final integrated artifact has been delivered to the original repository.",
			"If delivery is blocked: duo apply or duo apply <session-id>. Delivery is fast-forward only.",
			"A merge you finish yourself (git merge --no-ff <final-head>) is recognized as already applied by duo apply.",
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

// detailLines is the Ctrl+O session overview: the values that used to be
// reachable only through the session store or the Help text, plus the full
// verification note or shared plan that the one-line status row truncates.
func (a *App) detailLines(width int) []string {
	if width < 1 {
		return nil
	}
	snap := a.state.Snapshot()
	set := workspace.Set{}
	if a.ws != nil {
		set = a.ws.Set()
	}
	lines := []string{
		"Session",
		"  id:      " + set.Session,
		"  repo:    " + set.Repository,
		"  branch:  " + set.BaseBranch,
		"  mode:    " + snap.EffectiveMode().Display(),
		"  phase:   " + string(snap.Phase),
		"",
		"Worktrees",
		"  Austin:  " + worktreeLine(set, protocol.Austin),
		"  Tony:    " + worktreeLine(set, protocol.Tony),
		"",
	}
	if snap.EffectiveMode() == project.ModeFast {
		lines = append(lines, "Verification", "  "+snap.Verification.Label())
		if head := strings.TrimSpace(snap.Verification.Head); head != "" {
			lines = append(lines, "  head: "+head)
		}
		if note := strings.TrimSpace(snap.Verification.Note); note != "" {
			lines = append(lines, "  note:")
			for _, l := range strings.Split(note, "\n") {
				lines = append(lines, "    "+l)
			}
		}
	} else {
		lines = append(lines, fmt.Sprintf("Shared plan (v%d)", snap.PlanVersion))
		plan := strings.TrimSpace(snap.Plan)
		if plan == "" {
			plan = "(none yet)"
		}
		for _, l := range strings.Split(plan, "\n") {
			lines = append(lines, "  "+l)
		}
	}
	lines = append(lines, "", "Delivery", "  "+a.deliverySummary())

	var out []string
	for _, line := range lines {
		out = append(out, wrap(line, width)...)
	}
	return out
}

func worktreeLine(set workspace.Set, agent protocol.AgentID) string {
	wt, ok := set.For(agent)
	if !ok || wt.Path == "" {
		return "(none)"
	}
	return wt.Path + "  [" + wt.Branch + "]"
}

func (a *App) detailVisibleRows() int { return maxInt(a.height-4, 1) }

func (a *App) maxDetailOffset() int {
	return maxInt(len(a.detailLines(maxInt(a.width-2, 1)))-a.detailVisibleRows(), 0)
}

func (a *App) clampDetailOffset() {
	if a.detailOffset < 0 {
		a.detailOffset = 0
	}
	if max := a.maxDetailOffset(); a.detailOffset > max {
		a.detailOffset = max
	}
}

func (a *App) scrollDetail(delta int) {
	a.detailOffset += delta
	a.clampDetailOffset()
}

// scrollOverlay routes the shared scroll keys to whichever overlay is open.
func (a *App) scrollOverlay(delta int) {
	if a.view == viewDetail {
		a.scrollDetail(delta)
		return
	}
	a.scrollHelp(delta)
}

// overlayTop and overlayBottom implement Home/End for both overlays.
func (a *App) overlayTop() {
	if a.view == viewDetail {
		a.detailOffset = 0
		return
	}
	a.helpOffset = 0
}

func (a *App) overlayBottom() {
	if a.view == viewDetail {
		a.detailOffset = a.maxDetailOffset()
		return
	}
	a.helpOffset = a.maxHelpOffset()
}

func (a *App) writeDetail(b *strings.Builder, w, h int) {
	contentWidth := w - 2
	lines := a.detailLines(contentWidth)
	a.clampDetailOffset()
	visible := maxInt(h-4, 1)
	title := " Duo Session · " + a.version + " "
	b.WriteString(paint(ansiBorder, "┌") + paint(ansiTitle, fit(title, contentWidth, "─")) + paint(ansiBorder, "┐\r\n"))
	for i := 0; i < visible; i++ {
		line := ""
		if at := a.detailOffset + i; at < len(lines) {
			line = lines[at]
		}
		color := ansiHint
		if line != "" && !strings.HasPrefix(line, " ") {
			color = ansiTitle
		}
		b.WriteString(paint(ansiBorder, "│") + paint(color, fit(line, contentWidth)) + paint(ansiBorder, "│\r\n"))
	}
	b.WriteString(paint(ansiBorder, "├") + paint(ansiBorder, strings.Repeat("─", contentWidth)) + paint(ansiBorder, "┤\r\n"))
	first, last := a.detailOffset+1, minInt(a.detailOffset+visible, len(lines))
	if len(lines) == 0 {
		first, last = 0, 0
	}
	foot := fmt.Sprintf(" Lines %d–%d / %d · ↑↓/jk scroll · PgUp/PgDn · Esc close · Ctrl+Q quit ", first, last, len(lines))
	b.WriteString(paint(ansiBorder, "└") + paint(ansiHint, fit(foot, contentWidth, "─")) + paint(ansiBorder, "┘"))
}

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
