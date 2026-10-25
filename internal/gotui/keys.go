package gotui

import (
	"bytes"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/atfa/duo/internal/protocol"
	"github.com/gdamore/tcell/v3"
)

func (a *App) handleEvent(ev tcell.Event) {
	switch ev := ev.(type) {
	case *tcell.EventKey:
		a.handleKey(ev)
	case *tcell.EventMouse:
		a.handleMouse(ev)
	case *tcell.EventResize:
		a.resize()
	case *tcell.EventPaste:
		if ev.Start() {
			a.pasting = true
			a.pasteBuf.Reset()
		} else {
			a.pasting = false
			pasted := a.pasteBuf.String()
			a.pasteBuf.Reset()
			if pasted != "" {
				if a.search.active {
					// A pasted term is a search query, not composer text: the prompt
					// owns the keyboard while it is open.
					a.search.query += strings.ReplaceAll(strings.TrimSpace(pasted), "\n", " ")
					a.refreshSearch(true)
					return
				}
				a.composer.InsertPaste(pasted)
			}
		}
	case *tcell.EventError:
		a.setStatus(fmt.Sprintf(a.theme.tr("terminal error: %s"), ev.Error()), true)
	}
}

func (a *App) handleKey(ev *tcell.EventKey) {
	defer a.syncMention()
	// Bracketed paste is text, not a command stream: a pasted newline must not
	// submit, and a pasted Ctrl+Q must not reach the application keymap.
	if a.pasting {
		switch ev.Key() {
		case tcell.KeyEnter, tcell.KeyCtrlJ:
			a.pasteBuf.WriteString("\n")
		case tcell.KeyTab:
			a.pasteBuf.WriteString("    ")
		case tcell.KeyRune:
			if str := ev.Str(); str != "" {
				a.pasteBuf.WriteString(str)
			}
		}
		return
	}
	if a.overlay != overlayNone {
		switch a.overlay {
		case overlayConfirm:
			a.handleConfirmKey(ev)
		case overlayObserver:
			a.handleObserverConfigKey(ev)
		case overlayPicker:
			a.handlePickerKey(ev)
		case overlayDrivers:
			a.handleDriverPickerKey(ev)
		case overlayModePicker:
			a.handleModePickerKey(ev)
		case overlayPlan:
			a.handleScrollOverlayKey(ev, func(d int) { a.plan.Scroll(d) }, a.plan.maxOffset())
		case overlayHelp:
			a.handleScrollOverlayKey(ev, func(d int) { a.help.Scroll(d) }, max(0, len(a.help.lines)-max(1, a.help.Inner.Dy()-1)))
		case overlayDetails:
			a.handleScrollOverlayKey(ev, func(d int) { a.details.Scroll(d) }, max(0, len(a.details.lines)-max(1, a.details.Inner.Dy()-1)))
		case overlayObserverReports:
			a.handleObserverReportKey(ev)
		case overlayQrcode:
			if ev.Key() == tcell.KeyEsc {
				a.closeOverlay()
			}
		case overlaySessions:
			a.handleSessionsKey(ev)
		}
		return
	}
	if a.search.active {
		a.handleSearchKey(ev)
		return
	}
	// A committed search owns n/N and Esc. Anything else ends it and is then
	// handled normally, so the composer stays usable without a mode the human
	// has to remember to leave.
	if a.search.nav {
		switch {
		case ev.Key() == tcell.KeyEsc:
			a.closeSearch(true)
			return
		case ev.Key() == tcell.KeyCtrlF:
			a.openSearch("")
			return
		case ev.Key() == tcell.KeyRune && ev.Modifiers()&(tcell.ModCtrl|tcell.ModAlt) == 0 && (ev.Str() == "n" || ev.Str() == "N"):
			delta := 1
			if ev.Str() == "N" {
				delta = -1
			}
			a.searchJump(delta)
			return
		}
		a.closeSearch(false)
	}
	if a.slashOpen() {
		switch ev.Key() {
		case tcell.KeyUp:
			a.slash.move(-1)
			a.slashCursor = a.slash.cursor
			return
		case tcell.KeyDown:
			a.slash.move(1)
			a.slashCursor = a.slash.cursor
			return
		case tcell.KeyTab:
			a.autocompleteSlash()
			return
		case tcell.KeyEnter:
			if a.selectSlash() {
				return
			}
		case tcell.KeyEsc:
			a.slashHidden = true
			return
		}
	}
	if a.mentionActive() {
		switch ev.Key() {
		case tcell.KeyUp:
			if a.mentionOpen() {
				a.fileMenu.move(-1)
				return
			}
		case tcell.KeyDown:
			if a.mentionOpen() {
				a.fileMenu.move(1)
				return
			}
		case tcell.KeyTab:
			if a.acceptMention() {
				return
			}
		case tcell.KeyEnter:
			// Ctrl/Alt/Shift+Enter still means a newline; only plain Enter
			// accepts the highlighted file.
			if ev.Modifiers() == 0 && a.acceptMention() {
				return
			}
		case tcell.KeyEsc:
			a.closeMention()
			return
		}
	}

	key := ev.Key()
	mods := ev.Modifiers()
	switch key {
	case tcell.KeyCtrlQ, tcell.KeyCtrlD:
		a.requestQuit()
		return
	case tcell.KeyCtrlC:
		a.setStatus(a.theme.tr("Ctrl+Q quits · Esc closes dialogs"), false)
		return
	case tcell.KeyCtrlA:
		a.enterNative(protocol.Austin)
		return
	case tcell.KeyCtrlT:
		a.enterNative(protocol.Tony)
		return
	case tcell.KeyCtrlR:
		a.restartAgent(protocol.Austin)
		return
	case tcell.KeyCtrlY:
		a.restartAgent(protocol.Tony)
		return
	case tcell.KeyCtrlO:
		a.openDetails()
		return
	case tcell.KeyCtrlM:
		a.openPicker()
		return
	case tcell.KeyCtrlS:
		a.openModePicker()
		return
	case tcell.KeyCtrlE:
		a.openObserverReports()
		return
	case tcell.KeyCtrlP:
		a.toggleSidebar()
		return
	case tcell.KeyCtrlG:
		a.toggleTimestamps()
		return
	case tcell.KeyCtrlX:
		a.copyLatest()
		return
	case tcell.KeyCtrlF:
		a.openSearch("")
		return
	case tcell.KeyCtrlV:
		a.pasteImageFromClipboard()
		return
	case tcell.KeyCtrlL:
		a.screen.Sync()
		return
	case tcell.KeyF1:
		a.openHelp()
		return
	case tcell.KeyF2:
		a.toggleView()
		return
	case tcell.KeyF3:
		a.openPlan()
		return
	case tcell.KeyPgUp:
		a.activeTranscript().Scroll(-max(3, a.activeTranscript().Inner.Dy()-2))
		return
	case tcell.KeyPgDn:
		a.activeTranscript().Scroll(max(3, a.activeTranscript().Inner.Dy()-2))
		return
	case tcell.KeyEnter:
		if a.pasting || mods&(tcell.ModCtrl|tcell.ModAlt|tcell.ModShift) != 0 {
			a.composer.InsertNewline()
			a.resetHistory()
			return
		}
		a.submit()
		return
	case tcell.KeyCtrlJ:
		a.composer.InsertNewline()
		a.resetHistory()
		return
	case tcell.KeyTab:
		a.composer.InsertString("    ")
		a.resetHistory()
		return
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if a.composer.Backspace() {
			a.resetHistory()
			a.slashHidden = false
		}
		return
	case tcell.KeyDelete:
		if a.composer.Delete() {
			a.resetHistory()
			a.slashHidden = false
		}
		return
	case tcell.KeyLeft:
		if mods&tcell.ModAlt != 0 {
			a.composer.WordLeft()
		} else {
			a.composer.Left()
		}
		a.resetHistory()
		return
	case tcell.KeyRight:
		if mods&tcell.ModAlt != 0 {
			a.composer.WordRight()
		} else {
			a.composer.Right()
		}
		a.resetHistory()
		return
	case tcell.KeyHome:
		a.composer.Home()
		return
	case tcell.KeyEnd:
		a.composer.End()
		return
	case tcell.KeyUp:
		if a.historyIdx >= 0 || a.composer.AtFirstVisual() {
			a.historyPrev()
			return
		}
		a.composer.MoveVertical(-1)
		return
	case tcell.KeyDown:
		if a.historyIdx >= 0 {
			a.historyNext()
			return
		}
		if a.composer.AtLastVisual() {
			a.historyNext()
			return
		}
		a.composer.MoveVertical(1)
		return
	case tcell.KeyCtrlK:
		a.composer.DeleteToEnd()
		a.resetHistory()
		return
	case tcell.KeyCtrlU:
		a.composer.DeleteToStart()
		a.resetHistory()
		return
	case tcell.KeyCtrlW:
		if a.composer.DeleteWord() {
			a.resetHistory()
			a.slashHidden = false
		}
		return
	case tcell.KeyEsc:
		if a.slashOpen() {
			a.slashHidden = true
			return
		}
		a.clearSelections()
		a.setStatus("", false)
		return
	case tcell.KeyUS:
		a.openHelp()
		return
	case tcell.KeyRune:
		str := ev.Str()
		if str == "" {
			return
		}
		if mods&tcell.ModAlt != 0 {
			switch strings.ToLower(str) {
			case "m":
				a.openPicker()
			case "s":
				a.openModePicker()
			case "a":
				a.enterNative(protocol.Austin)
			case "t":
				a.enterNative(protocol.Tony)
			case "h":
				a.openHelp()
			case "o":
				a.openDetails()
			}
			return
		}
		if mods&tcell.ModCtrl != 0 {
			if str == "/" {
				a.openHelp()
			}
			return
		}
		if str == "@" && a.mentionStart < 0 {
			line := a.composer.CurrentLine()
			_, col := a.composer.Cursor()
			if mentionBoundary(line, col) {
				a.composer.InsertString(str)
				a.openMention(col)
				a.slashHidden = false
				a.resetHistory()
				return
			}
		}
		if strings.ContainsRune(str, '\n') || strings.ContainsRune(str, '\r') {
			a.composer.InsertPaste(str)
		} else {
			a.composer.InsertString(str)
		}
		a.slashHidden = false
		a.resetHistory()
	}
}

func (a *App) handleConfirmKey(ev *tcell.EventKey) {
	switch ev.Key() {
	case tcell.KeyEsc, tcell.KeyCtrlQ:
		a.closeOverlay()
	case tcell.KeyLeft, tcell.KeyRight, tcell.KeyTab:
		a.confirm.active = 1 - a.confirm.active
	case tcell.KeyRune:
		switch strings.ToLower(ev.Str()) {
		case "y":
			yes := a.confirm.onYes
			a.closeOverlay()
			if yes != nil {
				yes()
			}
		case "n":
			a.closeOverlay()
		}
	case tcell.KeyEnter:
		if a.confirm.active == 1 {
			yes := a.confirm.onYes
			a.closeOverlay()
			if yes != nil {
				yes()
			}
		} else {
			a.closeOverlay()
		}
	}
}

// handleDriverPickerKey mirrors the model picker: Tab moves the target agent,
// typing filters, Enter applies, Space applies and keeps the modal open for the
// other agent.
func (a *App) handleDriverPickerKey(ev *tcell.EventKey) {
	p := a.drivers
	switch ev.Key() {
	case tcell.KeyEsc, tcell.KeyCtrlM:
		a.closeOverlay()
		return
	case tcell.KeyTab:
		agent := p.switchAgent()
		p.setCurrent(a.live.Agent(agent).Driver)
		return
	case tcell.KeyUp:
		p.move(-1)
		return
	case tcell.KeyDown:
		p.move(1)
		return
	case tcell.KeyPgUp:
		p.move(-max(1, p.visibleRows()))
		return
	case tcell.KeyPgDn:
		p.move(max(1, p.visibleRows()))
		return
	case tcell.KeyHome:
		p.cursor = 0
		p.clamp()
		return
	case tcell.KeyEnd:
		if list := p.filtered(); len(list) > 0 {
			p.cursor = len(list) - 1
			p.clamp()
		}
		return
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		p.backspaceFilter()
		return
	case tcell.KeyEnter:
		a.applyDriver(false)
		return
	case tcell.KeyRune:
		str := ev.Str()
		if str == "" {
			return
		}
		if ev.Modifiers()&tcell.ModAlt != 0 {
			if strings.EqualFold(str, "m") {
				a.closeOverlay()
			}
			return
		}
		if ev.Modifiers()&tcell.ModCtrl != 0 {
			return
		}
		if str == " " {
			a.applyDriver(true)
			return
		}
		for _, r := range str {
			p.addFilter(r)
		}
		return
	}
}

func (a *App) handleModePickerKey(ev *tcell.EventKey) {
	p := a.modePicker
	switch ev.Key() {
	case tcell.KeyEsc, tcell.KeyCtrlS:
		a.closeOverlay()
	case tcell.KeyUp:
		p.move(-1)
	case tcell.KeyDown:
		p.move(1)
	case tcell.KeyHome:
		p.cursor = 0
	case tcell.KeyEnd:
		p.cursor = len(pickerModes) - 1
	case tcell.KeyEnter:
		target := p.selected()
		// Close first: requestModeSwitch may open the confirm or resume prompt,
		// which must replace this panel instead of being hidden behind it.
		a.closeOverlay()
		a.requestModeSwitch(target, "")
	}
}

func (a *App) handlePickerKey(ev *tcell.EventKey) {
	p := a.picker
	switch ev.Key() {
	case tcell.KeyEsc:
		a.closeOverlay()
		return
	case tcell.KeyCtrlM:
		a.closeOverlay()
		return
	case tcell.KeyCtrlT:
		p.tab = 1 - p.tab
		return
	case tcell.KeyTab:
		a.modelTarget = p.switchAgent()
		a.ensurePickerCatalog()
		return
	case tcell.KeyBacktab:
		a.cycleThinking()
		return
	case tcell.KeyUp:
		if p.tab == 0 {
			p.move(-1)
		}
		return
	case tcell.KeyDown:
		if p.tab == 0 {
			p.move(1)
		}
		return
	case tcell.KeyPgUp:
		p.move(-max(1, p.visibleRows()))
		return
	case tcell.KeyPgDn:
		p.move(max(1, p.visibleRows()))
		return
	case tcell.KeyHome:
		p.cursor = 0
		p.clamp()
		return
	case tcell.KeyEnd:
		if list := p.filtered(); len(list) > 0 {
			p.cursor = len(list) - 1
			p.clamp()
		}
		return
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if p.tab == 0 {
			p.backspaceFilter()
		}
		return
	case tcell.KeyEnter:
		if p.tab == 1 {
			a.cycleThinking()
			return
		}
		a.applyPickerModel(false)
		return
	case tcell.KeyRune:
		str := ev.Str()
		if str == "" {
			return
		}
		if ev.Modifiers()&tcell.ModAlt != 0 {
			if strings.EqualFold(str, "m") {
				a.closeOverlay()
			}
			return
		}
		if ev.Modifiers()&tcell.ModCtrl != 0 {
			return
		}
		if p.tab == 1 {
			return
		}
		if str == " " {
			a.applyPickerModel(true)
			return
		}
		for _, r := range str {
			p.addFilter(r)
		}
		return
	}
}

// handleObserverReportKey scrolls the report panel, re-asks the observer on R,
// and opens the one-line question input on `?`. While the input is open, keys
// edit the question; Enter sends it and Esc cancels. The panel is the only
// place observer output lives, so asking is one key.
func (a *App) handleObserverReportKey(ev *tcell.EventKey) {
	if a.observerReports.asking {
		if sent, consumed := a.observerReports.handleQuestionKey(ev); consumed {
			if sent != "" {
				a.requestObserverAnswer(sent)
			}
			return
		}
	}
	if ev.Key() == tcell.KeyRune {
		switch strings.ToLower(ev.Str()) {
		case "r":
			a.requestObserverReport()
			return
		case "?":
			a.observerReports.beginQuestion()
			return
		}
	}
	a.handleScrollOverlayKey(ev, func(d int) { a.observerReports.Scroll(d) },
		a.observerReports.maxOffset())
}

func (a *App) handleScrollOverlayKey(ev *tcell.EventKey, scroll func(int), maxOffset int) {
	switch ev.Key() {
	case tcell.KeyEsc:
		a.closeOverlay()
	case tcell.KeyUp:
		scroll(-1)
	case tcell.KeyDown:
		scroll(1)
	case tcell.KeyPgUp:
		scroll(-10)
	case tcell.KeyPgDn:
		scroll(10)
	case tcell.KeyHome:
		scroll(-maxOffset)
	case tcell.KeyEnd:
		scroll(maxOffset)
	}
}

func (a *App) handleMouse(ev *tcell.EventMouse) {
	x, y := ev.Position()
	btn := ev.Buttons()
	if a.overlay != overlayNone {
		switch {
		case btn&tcell.WheelUp != 0:
			switch a.overlay {
			case overlayHelp:
				a.help.Scroll(-3)
			case overlayDetails:
				a.details.Scroll(-3)
			case overlayPlan:
				a.plan.Scroll(-3)
			case overlayObserverReports:
				a.observerReports.Scroll(-3)
			}
		case btn&tcell.WheelDown != 0:
			switch a.overlay {
			case overlayHelp:
				a.help.Scroll(3)
			case overlayDetails:
				a.details.Scroll(3)
			case overlayPlan:
				a.plan.Scroll(3)
			case overlayObserverReports:
				a.observerReports.Scroll(3)
			}
		}
		return
	}
	if btn&tcell.WheelUp != 0 {
		if t := a.transcriptAt(x, y); t != nil {
			t.Scroll(-3)
		}
		return
	}
	if btn&tcell.WheelDown != 0 {
		if t := a.transcriptAt(x, y); t != nil {
			t.Scroll(3)
		}
		return
	}
	if btn&tcell.Button1 != 0 {
		if a.mousePane != nil {
			a.mousePane.DragSelection(x, y)
			return
		}
		a.pendingLink = nil
		point := image.Pt(x, y)
		if point.In(a.layout.tabSplit) || point.In(a.layout.tabTimeline) {
			if point.In(a.layout.tabSplit) {
				a.setViewMode(viewSplit)
			} else {
				a.setViewMode(viewTimeline)
			}
			return
		}
		if point.In(a.header.modeBadge) {
			a.openModePicker()
			return
		}
		if panel := a.sidebarPanelAt(x, y); panel != panelNone {
			if a.sidebarFocus == panel {
				a.sidebarFocus = panelNone
			} else {
				a.sidebarFocus = panel
			}
			return
		}
		if t := a.transcriptAt(x, y); t != nil {
			a.clearSelections()
			if target, ok := t.linkAt(x, y); ok {
				a.pendingLink = &target
			}
			if t.StartSelection(x, y) {
				a.mousePane = t
				a.focusPane = t
			}
		}
		return
	}
	if btn == tcell.ButtonNone {
		pending := a.pendingLink
		a.pendingLink = nil
		if a.mousePane != nil {
			pane := a.mousePane
			a.mousePane = nil
			text, ok := pane.FinishSelection()
			if ok && strings.TrimSpace(text) != "" {
				if err := a.copyToClipboard(text); err != nil {
					a.setStatus(fmt.Sprintf(a.theme.tr("copy failed: %s"), err.Error()), true)
				} else {
					a.showCopiedToast(len([]rune(text)))
				}
				return
			}
		}
		if pending != nil {
			a.openLink(*pending)
		}
	}
}

func (a *App) sidebarPanelAt(x, y int) sidebarPanel {
	if !a.layout.sidebarVisible {
		return panelNone
	}
	pt := image.Pt(x, y)
	switch {
	case pt.In(a.layout.workflow):
		return panelWorkflow
	case pt.In(a.layout.austinCard):
		return panelAustin
	case pt.In(a.layout.tonyCard):
		return panelTony
	default:
		return panelNone
	}
}

func (a *App) transcriptAt(x, y int) *transcript {
	if a.layout.split {
		for _, t := range []*transcript{a.duo, a.tony, a.austin} {
			if t.Contains(x, y) {
				return t
			}
		}
		return nil
	}
	if a.timeline.Contains(x, y) {
		return a.timeline
	}
	return nil
}

func (a *App) clearSelections() {
	a.timeline.ClearSelection()
	a.austin.ClearSelection()
	a.tony.ClearSelection()
	a.duo.ClearSelection()
}

func (a *App) copyToClipboard(text string) error {
	if text == "" {
		return fmt.Errorf("nothing selected")
	}
	if a.clipboard != nil {
		return a.clipboard(text)
	}
	if a.screen != nil {
		a.screen.SetClipboard([]byte(text))
	}
	_ = nativeClipboardWrite(text)
	return nil
}

func nativeClipboardWrite(text string) error {
	switch runtime.GOOS {
	case "darwin":
		cmd := exec.Command("pbcopy")
		cmd.Stdin = bytes.NewBufferString(text)
		return cmd.Run()
	case "linux":
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			cmd := exec.Command("wl-copy")
			cmd.Stdin = bytes.NewBufferString(text)
			return cmd.Run()
		}
		cmd := exec.Command("xclip", "-selection", "clipboard")
		cmd.Stdin = bytes.NewBufferString(text)
		if err := cmd.Run(); err == nil {
			return nil
		}
		cmd = exec.Command("xsel", "--clipboard", "--input")
		cmd.Stdin = bytes.NewBufferString(text)
		return cmd.Run()
	}
	return nil
}

func (a *App) historyPrev() {
	if len(a.history) == 0 {
		return
	}
	if a.historyIdx < 0 {
		a.historyDraft = a.composer.Text()
		a.historyIdx = len(a.history)
	}
	if a.historyIdx > 0 {
		a.historyIdx--
	}
	a.composer.SetText(a.history[a.historyIdx])
	a.slashHidden = false
}

func (a *App) historyNext() {
	if a.historyIdx < 0 {
		return
	}
	if a.historyIdx < len(a.history)-1 {
		a.historyIdx++
		a.composer.SetText(a.history[a.historyIdx])
		a.slashHidden = false
		return
	}
	a.historyIdx = -1
	a.composer.SetText(a.historyDraft)
	a.slashHidden = false
}

func (a *App) resetHistory() {
	a.historyIdx = -1
	a.historyDraft = ""
}

func (a *App) pushHistory(text string) {
	text = strings.TrimSpace(text)
	if text != "" {
		if n := len(a.history); n == 0 || a.history[n-1] != text {
			a.history = append(a.history, text)
			if len(a.history) > 100 {
				a.history = a.history[len(a.history)-100:]
			}
			appendComposerHistory(text)
		}
	}
	a.resetHistory()
}

func historyFilePath() string {
	if path := os.Getenv("DUO_HISTORY_FILE"); path != "" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".duo", "history")
}

func loadComposerHistory() []string {
	path := historyFilePath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if line != "" {
			out = append(out, strings.ReplaceAll(line, "\\n", "\n"))
		}
	}
	if len(out) > 100 {
		out = out[len(out)-100:]
	}
	return out
}

func appendComposerHistory(text string) {
	path := historyFilePath()
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintln(f, strings.ReplaceAll(text, "\n", "\\n"))
}
