package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/atfa/duo/internal/clidoc"
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
	// Dispatch lists the normalised key names this row documents, so the Help
	// table stays checkable against the real key router.
	Dispatch    []string
	Keys        string
	ShortLabel  string
	Description string
}

var keyBindings = []keyBinding{
	{[]string{"enter"}, "Enter", "Enter Send", "Send task/message to Austin; selects the highlighted command when the palette is open"},
	{[]string{"ctrl-enter"}, "Ctrl/Shift+Enter", "Ctrl/Shift+Enter Newline", "Insert a newline in the composer"},
	{[]string{"ctrl-a"}, "Ctrl+A", "Ctrl+A/T Native", "Attach to Austin's own agent session directly"},
	{[]string{"ctrl-t"}, "Ctrl+T", "", "Attach to Tony's own agent session directly"},
	{nil, "Ctrl+]", "", "Return from the agent's own session to Duo (only while attached)"},
	{nil, "Ctrl+\\", "", "Return from the agent's own session to Duo (only while attached)"},
	{nil, "Ctrl+】", "", "Return from the agent's own session to Duo (only while attached)"},
	{[]string{"ctrl-r"}, "Ctrl+R", "", "Restart Austin if exited/failed"},
	{[]string{"ctrl-y"}, "Ctrl+Y", "", "Restart Tony if exited/failed"},
	{[]string{"ctrl-slash"}, "Ctrl+/", "Ctrl+/ Help", "Toggle Help"},
	{[]string{"ctrl-o"}, "Ctrl+O", "", "Show session overview (worktrees, plan, delivery, changes diffstat)"},
	{[]string{"ctrl-m", "alt-m"}, "Ctrl+M / Alt+M", "", "Open the model and thinking level picker for the target agent"},
	{[]string{"shift-tab"}, "Shift+Tab", "", "Cycle the thinking level of the target agent"},
	{[]string{"ctrl-p"}, "Ctrl+P", "", "Toggle the Austin/Tony work preview: current tool and arguments, last error, and the text being streamed"},
	{[]string{"ctrl-g"}, "Ctrl+G", "", "Toggle message timestamps"},
	{[]string{"ctrl-q"}, "Ctrl+Q", "Ctrl+Q Quit", "Quit Duo and preserve session"},
	{[]string{"tab"}, "Tab", "", "Autocomplete the highlighted slash command; switch agent inside the model picker"},
	{[]string{"esc"}, "Esc", "", "Dismiss the slash menu, close the model picker, or leave Help and the session overview"},
	{[]string{"left", "right"}, "← / →", "", "Move the composer cursor"},
	{[]string{"alt-left", "alt-right"}, "Alt+←/→", "", "Move the composer cursor by word"},
	{[]string{"up", "down"}, "↑ / ↓", "", "Move between composer lines; recall task history at the first/last line"},
	{[]string{"page-up", "page-down"}, "PgUp / PgDn", "", "Scroll the conversation timeline earlier or later"},
	{[]string{"home", "end"}, "Home / End", "", "Move to the start or end of the composer line"},
	{[]string{"ctrl-u", "ctrl-k"}, "Ctrl+U / Ctrl+K", "", "Delete to the start or end of the composer line"},
	{[]string{"ctrl-w"}, "Ctrl+W", "", "Delete the previous word"},
	{[]string{"backspace", "delete"}, "Backspace / Delete", "", "Delete the previous or next composer character"},
	{nil, "Mouse wheel", "", "Scroll the conversation timeline"},
	{nil, "Mouse drag", "", "Select timeline text; copies to clipboard on release"},
	{nil, "Mouse click [↗]", "", "Attach that agent's own session from its work preview header"},
}

// modelPickerKeys documents the modal opened by Ctrl+M / Alt+M, which replaces
// the composer while it is open.
var modelPickerKeys = []keyBinding{
	{[]string{"tab"}, "Tab", "", "Switch the target agent between Austin and Tony"},
	{[]string{"shift-tab"}, "Shift+Tab", "", "Cycle the target agent's thinking level"},
	{nil, "type / Backspace / Delete", "", "Filter the catalog the target's own driver reports"},
	{[]string{"up", "down"}, "↑ / ↓", "", "Move the cursor one row"},
	{[]string{"page-up", "page-down"}, "PgUp / PgDn", "", "Move the cursor one page"},
	{[]string{"home", "end"}, "Home / End", "", "Jump to the first / last model"},
	{[]string{" "}, "Space", "", "Apply the model and keep the picker open"},
	{[]string{"enter"}, "Enter", "", "Apply the model and close the picker"},
	{[]string{"esc", "ctrl-m", "alt-m"}, "Esc / Ctrl+M / Alt+M", "", "Close the picker without applying"},
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
	if a.live.EffectiveMode() == project.ModeFast {
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
		{"Model Picker (Ctrl+M)", helpModelPickerLines()},
		{"Slash Commands", []string{
			"Type / in the composer to open the interactive command palette.",
			"Navigate with ↑/↓, Tab to autocomplete, Enter to select/execute, Esc to dismiss.",
			"/escalate [reason]  Dynamically upgrade a Fast session to Goal mode.",
			"/mode                Print the current session mode.",
			"/mode goal           Upgrade the session to Goal mode; /mode fast is rejected, a session never returns to Fast.",
			"/model               Open the model and thinking level picker (Ctrl+M).",
			"/overview            Toggle the session overview and git diffstat (Ctrl+O).",
			"/preview             Toggle the agent work preview band (Ctrl+P).",
			"/timestamps          Toggle message timestamps (Ctrl+G).",
			"/help                Open Duo Help screen (Ctrl+/).",
			"/status              Print authoritative session and worktree status.",
			"/clear               The composer only holds this command, so it is already empty; Duo forces a full redraw and says so.",
			"/quit or /exit       Exit Duo and preserve session state.",
			"An unknown command is reported as an error. //text and an absolute path (e.g. /usr/bin/python) are sent to Austin as a task.",
			"Composer history is persisted to ~/.duo/history (override with DUO_HISTORY_FILE) and is recalled with ↑/↓ at the first/last line.",
			"Ctrl+Shift+Enter and Ctrl+M need a terminal that reports them distinctly (CSI-u or modifyOtherKeys); use Ctrl+Enter and Alt+M elsewhere.",
		}},
		{"Collaboration Lifecycle", lifecycle},
		{"Native session", []string{
			"Ctrl+A → Austin; Ctrl+T → Tony. This bypasses Duo: no task is routed through it, so the phase machine is untouched while you are attached.",
			"In Fast mode a commit made during an attach is still deliverable: when Austin asks for verification of a new HEAD, Duo reopens the round on it. A request naming the already-delivered HEAD is rejected.",
			"While attached, the agent's own commands and key bindings are handled by the agent, not by Duo — whatever CLI it runs, shipped or installed as a plugin.",
			"Return to Duo with Ctrl+], Ctrl+\\ or Ctrl+】.",
		}},
		{"Command Line", helpCommandLineLines()},
		{"Resume & Recovery", []string{
			"duo --resume (or -r, or the bare word resume) picks this repository's unfinished session;",
			"duo --resume <id> picks one specific session.",
			"Resume restores Duo state, worktrees, Pi conversation identity, working scope, pane history, and collaboration wake-up.",
		}},
		{"Delivery", []string{
			"DONE means the final integrated artifact has been delivered to the original repository.",
			"At DONE Duo asks Austin to summarise the whole session for you, and the answer arrives as `Austin → Human`.",
			"If delivery is blocked: duo apply or duo apply <session-id>. Delivery is fast-forward only.",
			"A merge you finish yourself (git merge --no-ff <final-head>) is recognized as already applied by duo apply.",
		}},
		{"Working Scope", []string{
			"cd repo/packages/web", "duo",
			"Git boundary: repo. Agent default cwd: repo/packages/web.",
			"Scope is a default working directory, not a filesystem sandbox.",
		}},
		{"Agent Status", []string{
			"The main view is one conversation timeline: Austin's messages on the left, Tony's on the right, under a top row that leads with the workflow mode — `[FAST]` or `[GOAL]` in bold — and then names the Git repository Duo resolved. The frame lines are tinted by mode, cyan for Fast and magenta for Goal. Every message header names a direction — for example `Austin → Human`, `Austin → Tony` or `Duo → Tony` — in the speaker's own colour, with messages addressed to the human highlighted in bold. Each agent's connection, process and working state, with the animated spinner and the attach button, live in that agent's work preview header; Duo phase, plan, and transient feedback stay below the timeline.",
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

func helpModelPickerLines() []string {
	lines := make([]string, 0, len(modelPickerKeys))
	for _, binding := range modelPickerKeys {
		lines = append(lines, "  "+binding.Keys+strings.Repeat(" ", maxInt(24-displayWidth(binding.Keys), 1))+binding.Description)
	}
	return lines
}

// helpCommandLineLines is the in-app mirror of the duo CLI surface. Every
// signature line comes from internal/clidoc, the same table `duo --help`
// renders, so the two cannot drift.
func helpCommandLineLines() []string {
	lines := append([]string{}, clidoc.SignatureLines()...)
	return append(lines,
		"",
		"The bare word 'resume' is an alias of --resume.",
		"Run duo --help for every flag, its default, and the DUO_* environment variables.",
	)
}

func (a *App) helpVisibleRows() int { return maxInt(a.height-4, 1) }

// detailLines is the Ctrl+O session overview: the values that used to be
// reachable only through the session store or the Help text, plus the full
// verification note or shared plan that the one-line status row truncates.
func (a *App) detailLines(width int) []string {
	if width < 1 {
		return nil
	}
	snap := a.live.Project
	set := a.live.Worktrees
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
		if a.live.TestCommand != "" {
			lines = append(lines, "  test-gate: "+a.live.TestCommand)
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
	if changes == nil {
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
	if a.svc == nil {
		a.cachedChanges = nil
		return
	}
	if set := a.live.Worktrees; set.Austin.Path == "" {
		a.cachedChanges = nil
		return
	}
	// Reading the worktree runs git, so it belongs to the session and not to a
	// paint. The detail view keeps its own cache of the result.
	a.cachedChanges = a.svc.Changes(context.Background())
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
	b.WriteString(paint(a.frameColor(), "┌") + paint(ansiTitle, fit(title, contentWidth, "─")) + paint(a.frameColor(), "┐\r\n"))
	for i := 0; i < visible; i++ {
		line := ""
		if at := a.detailOffset + i; at < len(lines) {
			line = lines[at]
		}
		color := ansiHint
		if line != "" && !strings.HasPrefix(line, " ") {
			color = ansiTitle
		}
		b.WriteString(paint(a.frameColor(), "│") + paint(color, fit(line, contentWidth)) + paint(a.frameColor(), "│\r\n"))
	}
	b.WriteString(paint(a.frameColor(), "├") + paint(a.frameColor(), strings.Repeat("─", contentWidth)) + paint(a.frameColor(), "┤\r\n"))
	first, last := a.detailOffset+1, minInt(a.detailOffset+visible, len(lines))
	if len(lines) == 0 {
		first, last = 0, 0
	}
	foot := fmt.Sprintf(" Lines %d–%d / %d · ↑↓/jk scroll · PgUp/PgDn · Esc/Ctrl+O close · Ctrl+Q quit ", first, last, len(lines))
	b.WriteString(paint(a.frameColor(), "└") + paint(ansiHint, fit(foot, contentWidth, "─")) + paint(a.frameColor(), "┘"))
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
