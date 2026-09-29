package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/atfa/duo/internal/protocol"
)

type slashCmd struct {
	Name        string
	Args        string
	Description string
	NeedsArgs   bool
}

var slashCommandList = []slashCmd{
	{Name: "/escalate", Args: "[reason]", Description: "平滑升级至 Goal 模式 · Escalate to Goal mode", NeedsArgs: true},
	{Name: "/mode", Args: "goal", Description: "切换或升级会话模式 · Switch session mode", NeedsArgs: true},
	{Name: "/model", Args: "", Description: "选择模型与思考级别 · Model & thinking picker (Ctrl+M)", NeedsArgs: false},
	{Name: "/overview", Args: "", Description: "查看会话概览与变更统计 · Session overview & diffstat (Ctrl+O)", NeedsArgs: false},
	{Name: "/preview", Args: "", Description: "切换工作轨迹预览栏 · Toggle work preview (Ctrl+P)", NeedsArgs: false},
	{Name: "/timestamps", Args: "", Description: "切换消息时间戳 · Toggle timestamps (Ctrl+G)", NeedsArgs: false},
	{Name: "/help", Args: "", Description: "查看帮助与协作流程 · Help & workflow guide (Ctrl+/)", NeedsArgs: false},
	{Name: "/status", Args: "", Description: "查看当前会话状态 · Show session status", NeedsArgs: false},
	{Name: "/clear", Args: "", Description: "清空输入框并重绘屏幕 · Clear input & redraw screen", NeedsArgs: false},
	{Name: "/quit", Args: "", Description: "退出 Duo 并保留当前会话 · Quit Duo (Ctrl+Q)", NeedsArgs: false},
}

// slashPrefix returns the slash command prefix being typed if input is currently
// a slash command token (starts with '/' and cursor is inside the command name).
func (a *App) slashPrefix() string {
	if a.slashDismissed || a.view != viewMain {
		return ""
	}
	raw := string(a.input)
	if len(raw) == 0 {
		return ""
	}
	pos := clampInputPos(a.input, a.inputPos)
	prefixBeforeCursor := string(a.input[:pos])
	trimmed := strings.TrimLeft(prefixBeforeCursor, " \t")
	if !strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "//") {
		return ""
	}
	// If there's any whitespace before the cursor, the user is typing arguments
	if strings.ContainsAny(trimmed, " \t\r\n") {
		return ""
	}
	// Also check if remainder of the token after cursor has a space
	remainder := string(a.input[pos:])
	if len(remainder) > 0 && !strings.HasPrefix(remainder, " ") && !strings.HasPrefix(remainder, "\t") && !strings.HasPrefix(remainder, "\n") {
		end := strings.IndexAny(remainder, " \t\r\n")
		if end == -1 {
			trimmed += remainder
		} else {
			trimmed += remainder[:end]
		}
	}
	return trimmed
}

// matchingSlashCommands returns commands that match the typed slash prefix.
func (a *App) matchingSlashCommands() []slashCmd {
	prefix := a.slashPrefix()
	if prefix == "" {
		return nil
	}
	prefixLower := strings.ToLower(prefix)
	var matches []slashCmd
	for _, cmd := range slashCommandList {
		if strings.HasPrefix(strings.ToLower(cmd.Name), prefixLower) {
			matches = append(matches, cmd)
		}
	}
	return matches
}

func (a *App) hasSlashMenu() bool {
	return len(a.matchingSlashCommands()) > 0
}

func (a *App) clampSlashCursor() {
	matches := a.matchingSlashCommands()
	if len(matches) == 0 {
		a.slashCursor = 0
		return
	}
	if a.slashCursor >= len(matches) {
		a.slashCursor = len(matches) - 1
	}
	if a.slashCursor < 0 {
		a.slashCursor = 0
	}
}

func (a *App) slashUp() {
	matches := a.matchingSlashCommands()
	if len(matches) == 0 {
		return
	}
	a.slashCursor = (a.slashCursor - 1 + len(matches)) % len(matches)
}

func (a *App) slashDown() {
	matches := a.matchingSlashCommands()
	if len(matches) == 0 {
		return
	}
	a.slashCursor = (a.slashCursor + 1) % len(matches)
}

// selectSlashCommand is called when Enter is pressed while the slash menu is active.
// If the selected command needs arguments, it fills the composer with "/cmd "
// and returns true (do not submit yet, let user type arguments).
// If the selected command does not need arguments, it sets the composer to "/cmd"
// and returns false (caller should submit immediately).
func (a *App) selectSlashCommand() bool {
	matches := a.matchingSlashCommands()
	if len(matches) == 0 {
		return false
	}
	a.clampSlashCursor()
	cmd := matches[a.slashCursor]
	if cmd.NeedsArgs {
		a.input = []byte(cmd.Name + " ")
		a.inputPos = len(a.input)
		a.slashCursor = 0
		a.slashDismissed = false
		return true
	}
	a.input = []byte(cmd.Name)
	a.inputPos = len(a.input)
	a.slashCursor = 0
	a.slashDismissed = false
	return false
}

// autocompleteSlashCommand is called on Tab when the slash menu is active.
// It populates the command name (with trailing space if needsArgs).
func (a *App) autocompleteSlashCommand() {
	matches := a.matchingSlashCommands()
	if len(matches) == 0 {
		return
	}
	a.clampSlashCursor()
	cmd := matches[a.slashCursor]
	newText := cmd.Name
	if cmd.NeedsArgs {
		newText += " "
	}
	a.input = []byte(newText)
	a.inputPos = len(a.input)
	a.slashCursor = 0
	a.slashDismissed = false
}

// runSlashCommand handles client-side slash commands. Returns true if handled.
func (a *App) runSlashCommand(ctx context.Context, text string) bool {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") || strings.HasPrefix(text, "//") {
		return false
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return false
	}
	cmd := strings.ToLower(fields[0])
	switch cmd {
	case "/help":
		a.applyAction(ctx, inputAction{kind: actionToggleHelp})
		return true
	case "/overview":
		a.applyAction(ctx, inputAction{kind: actionToggleDetail})
		return true
	case "/model":
		a.openModelPicker()
		return true
	case "/preview":
		a.hidePreview = !a.hidePreview
		if a.hidePreview {
			a.setStatus("work preview hidden", false)
		} else {
			a.setStatus("work preview shown", false)
		}
		a.requestFullClear()
		return true
	case "/timestamps":
		a.applyAction(ctx, inputAction{kind: actionToggleTimestamps})
		return true
	case "/clear":
		a.requestFullClear()
		a.setStatus("screen redrawn", false)
		return true
	case "/status":
		status := "Duo status"
		if a.coord != nil {
			status = a.coord.StatusText(ctx)
		}
		a.add(protocol.Duo, status)
		a.setStatus("status reported in timeline", false)
		return true
	case "/quit", "/exit":
		a.applyAction(ctx, inputAction{kind: actionQuit})
		return true
	case "/mode":
		if len(fields) == 1 {
			mode := a.state.Snapshot().EffectiveMode().Display()
			a.setStatus(fmt.Sprintf("current mode: %s (use /mode goal to escalate)", mode), false)
			return true
		}
		return false // e.g. /mode goal, pass to SubmitUserTask
	case "/escalate":
		return false // pass to SubmitUserTask
	default:
		// If command contains a slash after the first character, it's likely a file path (e.g. /usr/bin/foo)
		if strings.Contains(cmd[1:], "/") {
			return false
		}
		a.setStatus(fmt.Sprintf("unknown command %q (type / to see available commands)", cmd), true)
		return true
	}
}

// slashMenuRows returns the number of vertical rows needed to render the slash menu.
func (a *App) slashMenuRows(w, h int) int {
	matches := a.matchingSlashCommands()
	if len(matches) == 0 {
		return 0
	}
	maxVisible := 5
	if h < 22 {
		maxVisible = 3
	}
	count := minInt(len(matches), maxVisible)
	return count + 2 // 1 top border + count item rows + 1 bottom border
}

// writeSlashMenu writes the popup box listing matching slash commands and their explanations.
func (a *App) writeSlashMenu(b *strings.Builder, w, h int) {
	matches := a.matchingSlashCommands()
	if len(matches) == 0 {
		return
	}
	maxVisible := 5
	if h < 22 {
		maxVisible = 3
	}
	total := len(matches)
	visibleCount := minInt(total, maxVisible)
	if visibleCount == 0 {
		return
	}

	a.clampSlashCursor()
	start := 0
	if a.slashCursor >= visibleCount {
		start = a.slashCursor - visibleCount + 1
	}
	end := minInt(start+visibleCount, total)
	if end-start < visibleCount && total > visibleCount {
		start = maxInt(0, end-visibleCount)
	}

	boxW := w - 4
	innerW := boxW - 2
	title := fmt.Sprintf(" Commands (%d/%d · ↑/↓ navigate · Enter/Tab apply · Esc) ", a.slashCursor+1, total)
	topLine := "┌" + fit(title, innerW, "─") + "┐"
	b.WriteString(paint(ansiBorder, "│ ") + paint(ansiTitle, topLine) + paint(ansiBorder, " │\r\n"))

	for i := start; i < end; i++ {
		cmd := matches[i]
		selected := (i == a.slashCursor)
		marker := "  "
		if selected {
			marker = "▶ "
		}
		sig := cmd.Name
		if cmd.Args != "" {
			sig += " " + cmd.Args
		}
		sigPart := fit(sig, 22)
		descW := maxInt(innerW-25, 0)
		descPart := fit(cmd.Description, descW)

		var row string
		if selected {
			row = paint(ansiTitle, marker) + paint(ansiBold, sigPart) + " " + paint(ansiStatus, descPart)
		} else {
			row = marker + paint(ansiAustinDim, sigPart) + " " + paint(ansiHint, descPart)
		}
		b.WriteString(paint(ansiBorder, "│ ") + paint(ansiBorder, "│") + row + paint(ansiBorder, "│") + paint(ansiBorder, " │\r\n"))
	}

	bottomLine := "└" + strings.Repeat("─", innerW) + "┘"
	b.WriteString(paint(ansiBorder, "│ ") + paint(ansiBorder, bottomLine) + paint(ansiBorder, " │\r\n"))
}
