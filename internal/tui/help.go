package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

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
	{"Ctrl+O", "", "Show session overview (worktrees, plan, delivery, changes diffstat)"},
	{"Ctrl+M / Alt+M", "", "Choose the Pi model and thinking level for Austin or Tony; in the picker Tab switches agent, Space applies the model and keeps it open, Enter applies and closes, Shift+Tab cycles thinking"},
	{"Ctrl+P", "", "Toggle the Austin/Tony work preview: current tool and arguments, last error, and the text being streamed"},
	{"Ctrl+G", "", "Toggle message timestamps"},
	{"Ctrl+Q", "Ctrl+Q Quit", "Quit Duo and preserve session"},
	{"← / →", "", "Move the composer cursor"},
	{"Alt+←/→", "", "Move the composer cursor by word"},
	{"↑ / ↓", "", "Move between composer lines; recall task history at the first/last line"},
	{"PgUp / PgDn", "", "Scroll the conversation timeline earlier or later"},
	{"Home / End", "", "Move to the start or end of the composer line"},
	{"Ctrl+U / Ctrl+K", "", "Delete to the start or end of the composer line"},
	{"Ctrl+W", "", "Delete the previous word"},
	{"Backspace", "", "Delete the previous composer character"},
	{"Mouse wheel", "", "Scroll the conversation timeline"},
	{"Mouse drag", "", "Select timeline text; copies to clipboard on release"},
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
			"Escalate FAST to GOAL on the fly: type /escalate [reason] or /mode goal in the composer, or agents call duo_escalate.",
			"--resume always uses the persisted mode.",
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
			"The main view is one conversation timeline: Austin's messages on the left, Tony's on the right, under a top row naming the Git repository Duo resolved. Every message header names a direction — for example `Austin → Human`, `Austin → Tony` or `Duo → Tony` — in the speaker's own colour, with messages addressed to the human highlighted in bold. Each agent's connection, process and working state, with the animated spinner and the native-Pi attach button, live in that agent's work preview header; Duo phase, plan, and transient feedback stay below the timeline.",
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
		if a.coord != nil && a.coord.TestCommand() != "" {
			lines = append(lines, "  test-gate: "+a.coord.TestCommand())
		}
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

	lines = append(lines, "", "Changes (Austin vs base)")
	changes := a.cachedChanges
	if changes == nil && a.ws != nil {
		a.refreshChanges()
		changes = a.cachedChanges
	}
	if len(changes) == 0 {
		lines = append(lines, "  (no changes vs base)")
	} else {
		for _, l := range changes {
			lines = append(lines, "  "+l)
		}
	}

	var out []string
	for _, line := range lines {
		out = append(out, wrap(line, width)...)
	}
	return out
}

func (a *App) refreshChanges() {
	if a.ws == nil {
		a.cachedChanges = nil
		return
	}
	set := a.ws.Set()
	if set.Austin.Path == "" {
		a.cachedChanges = nil
		return
	}
	base := set.BaseCommit
	if base == "" {
		base = set.BaseBranch
	}
	a.cachedChanges = gitChanges(set.Austin.Path, base)
}

func gitChanges(dir, base string) []string {
	if dir == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var lines []string
	args := []string{"-C", dir, "diff", "--stat"}
	if base != "" {
		args = append(args, base)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	if out, err := cmd.Output(); err == nil {
		if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
			lines = append(lines, strings.Split(trimmed, "\n")...)
		}
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	cmd2 := exec.CommandContext(ctx2, "git", "-C", dir, "status", "--porcelain")
	if out2, err := cmd2.Output(); err == nil {
		var untracked []string
		for _, raw := range strings.Split(string(out2), "\n") {
			line := strings.TrimRight(raw, "\r")
			if strings.HasPrefix(line, "?? ") {
				untracked = append(untracked, strings.TrimPrefix(line, "?? "))
			}
		}
		if len(untracked) > 0 {
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, "Untracked:")
			for _, u := range untracked {
				lines = append(lines, "  ? "+u)
			}
		}
	}
	return lines
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
	foot := fmt.Sprintf(" Lines %d–%d / %d · ↑↓/jk scroll · PgUp/PgDn · Esc/Ctrl+O close · Ctrl+Q quit ", first, last, len(lines))
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
