package tui

import (
	"bytes"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/atfa/duo/internal/protocol"
)

const (
	bracketedPasteStart = "\x1b[200~"
	bracketedPasteEnd   = "\x1b[201~"
)

type actionKind int

const (
	actionNone actionKind = iota
	actionSubmit
	actionQuit
	actionToggleHelp
	actionToggleDetail
	actionToggleTimestamps
	actionCloseHelp
	actionScrollUp
	actionScrollDown
	actionPageUp
	actionPageDown
	actionHome
	actionEnd
	actionScrollPane
	actionAttach
	actionRestart
	actionSelectStart
	actionSelectMove
	actionSelectEnd
)

type inputAction struct {
	kind  actionKind
	agent protocol.AgentID
	delta int
	mouse mouseEvent
}

func (a *App) handleByte(b byte) inputAction {
	if len(a.escBuf) > 0 || b == 0x1b {
		a.escBuf = append(a.escBuf, b)
		if bytes.HasPrefix(a.escBuf, []byte(bracketedPasteStart)) {
			if bytes.HasSuffix(a.escBuf, []byte(bracketedPasteEnd)) {
				paste := string(a.escBuf[len(bracketedPasteStart) : len(a.escBuf)-len(bracketedPasteEnd)])
				a.escBuf = nil
				a.insertInput(strings.ReplaceAll(strings.ReplaceAll(paste, "\r\n", "\n"), "\r", "\n"))
				a.setStatus("", false)
			}
			return inputAction{}
		}
		if len(a.escBuf) > 64 {
			a.escBuf = nil
			return inputAction{}
		}
		if b == 'M' || b == 'm' || b == '~' || b == 'u' || (len(a.escBuf) >= 3 && ((b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z'))) {
			seq := string(a.escBuf)
			a.escBuf = nil
			if mouse, ok := parseMouse(seq); ok && a.view == viewMain {
				if mouse.wheel() {
					if agent := a.hitPane(mouse.x, mouse.y); agent != "" {
						delta := 1
						if mouse.button == 65 {
							delta = -1
						}
						return inputAction{kind: actionScrollPane, agent: agent, delta: delta}
					}
					return inputAction{}
				}
				switch mouse.kind {
				case mousePress:
					if !mouse.primary() {
						return inputAction{}
					}
					if agent := a.hitNativeButton(mouse.x, mouse.y); agent != "" {
						return inputAction{kind: actionAttach, agent: agent}
					}
					if a.hitPane(mouse.x, mouse.y) != "" {
						return inputAction{kind: actionSelectStart, mouse: mouse}
					}
				case mouseDrag:
					if a.selection.active && mouse.primary() {
						return inputAction{kind: actionSelectMove, mouse: mouse}
					}
				case mouseRelease:
					if a.selection.active {
						return inputAction{kind: actionSelectEnd, mouse: mouse}
					}
				}
				return inputAction{}
			}
			return a.handleKey(decodeEscape(seq))
		}
		return inputAction{}
	}
	return a.handleKey(keyForByte(b))
}

// handleEscapeTimeout turns a bare Escape into a key after the input loop has
// allowed a short window for a terminal escape sequence.
func (a *App) handleEscapeTimeout() inputAction {
	if string(a.escBuf) != "\x1b" {
		return inputAction{}
	}
	a.escBuf = nil
	return a.handleKey("esc")
}

func keyForByte(b byte) string {
	switch b {
	case 1:
		return "ctrl-a"
	case 20:
		return "ctrl-t"
	case 18:
		return "ctrl-r"
	case 25:
		return "ctrl-y"
	case 17:
		return "ctrl-q"
	case 31:
		return "ctrl-slash"
	case 15:
		return "ctrl-o"
	case 7:
		return "ctrl-g"
	case 21:
		return "ctrl-u"
	case 11:
		return "ctrl-k"
	case 23:
		return "ctrl-w"
	case 13:
		return "enter"
	case 10:
		return "ctrl-enter"
	case 127, 8:
		return "backspace"
	}
	return string([]byte{b})
}

func decodeEscape(seq string) string {
	switch seq {
	case "\x1b[1;3D":
		return "alt-left"
	case "\x1b[1;3C":
		return "alt-right"
	case "\x1b[A":
		return "up"
	case "\x1b[B":
		return "down"
	case "\x1b[H", "\x1b[1~":
		return "home"
	case "\x1b[F", "\x1b[4~":
		return "end"
	case "\x1b[5~":
		return "page-up"
	case "\x1b[6~":
		return "page-down"
	case "\x1b[D":
		return "left"
	case "\x1b[C":
		return "right"
	case "\x1b[13;2u", "\x1b[27;2;13~", "\x1b[13;5u", "\x1b[27;5;13~":
		return "ctrl-enter"
	}
	return decodeModifiedKey(seq)
}

// decodeModifiedKey handles the CSI-u and modifyOtherKeys encodings enabled
// by Duo so existing Ctrl shortcuts remain usable outside native Pi.
func decodeModifiedKey(seq string) string {
	var code, modifier string
	switch {
	case strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "u"):
		parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "u"), ";")
		if len(parts) != 2 {
			return ""
		}
		code, modifier = parts[0], parts[1]
	case strings.HasPrefix(seq, "\x1b[27;") && strings.HasSuffix(seq, "~"):
		parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "~"), ";")
		if len(parts) != 3 || parts[0] != "27" {
			return ""
		}
		modifier, code = parts[1], parts[2]
	default:
		return ""
	}
	mod, _, _ := strings.Cut(modifier, ":")
	if !ctrlModifier(mod) {
		return ""
	}
	n, err := strconv.Atoi(code)
	if err != nil {
		return ""
	}
	switch n {
	case 1, 97:
		return "ctrl-a"
	case 17, 113:
		return "ctrl-q"
	case 18, 114:
		return "ctrl-r"
	case 20, 116:
		return "ctrl-t"
	case 25, 121:
		return "ctrl-y"
	case 31, 47:
		return "ctrl-slash"
	case 15, 111:
		return "ctrl-o"
	case 7, 103:
		return "ctrl-g"
	case 21, 117:
		return "ctrl-u"
	case 11, 107:
		return "ctrl-k"
	case 23, 119:
		return "ctrl-w"
	case 13:
		return "ctrl-enter"
	}
	return ""
}

func (a *App) handleKey(key string) inputAction {
	if key == "ctrl-q" {
		return inputAction{kind: actionQuit}
	}
	if a.view == viewHelp || a.view == viewDetail {
		switch key {
		case "ctrl-slash":
			return inputAction{kind: actionToggleHelp}
		case "esc":
			return inputAction{kind: actionCloseHelp}
		case "up", "k":
			return inputAction{kind: actionScrollUp}
		case "down", "j":
			return inputAction{kind: actionScrollDown}
		case "page-up":
			return inputAction{kind: actionPageUp}
		case "page-down":
			return inputAction{kind: actionPageDown}
		case "home", "g":
			return inputAction{kind: actionHome}
		case "end", "G":
			return inputAction{kind: actionEnd}
		}
		return inputAction{}
	}

	switch key {
	case "ctrl-a":
		return inputAction{kind: actionAttach, agent: protocol.Austin}
	case "ctrl-t":
		return inputAction{kind: actionAttach, agent: protocol.Tony}
	case "ctrl-r":
		return inputAction{kind: actionRestart, agent: protocol.Austin}
	case "ctrl-y":
		return inputAction{kind: actionRestart, agent: protocol.Tony}
	case "ctrl-slash":
		return inputAction{kind: actionToggleHelp}
	case "ctrl-o":
		return inputAction{kind: actionToggleDetail}
	case "ctrl-g":
		return inputAction{kind: actionToggleTimestamps}
	case "enter":
		return inputAction{kind: actionSubmit}
	case "ctrl-enter":
		a.insertInput("\n")
		a.setStatus("", false)
	case "left":
		a.inputPos = previousRune(a.input, a.inputPos)
	case "right":
		a.inputPos = nextRune(a.input, a.inputPos)
	case "alt-left":
		a.moveWordBack()
	case "alt-right":
		a.moveWordForward()
	case "home":
		a.inputPos, _ = a.lineBounds()
	case "end":
		_, a.inputPos = a.lineBounds()
	case "up":
		if !a.moveComposerLine(-1) {
			a.historyPrev()
		}
	case "down":
		if !a.moveComposerLine(1) {
			a.historyNext()
		}
	case "ctrl-u":
		a.deleteToLineStart()
	case "ctrl-k":
		a.deleteToLineEnd()
	case "ctrl-w":
		a.deleteWordBack()
	case "backspace":
		start := previousRune(a.input, a.inputPos)
		a.input = append(a.input[:start], a.input[a.inputPos:]...)
		a.inputPos = start
		a.resetHistory()
	default:
		if (len(key) == 1 && key[0] >= 32) || (len(key) == 1 && key[0] >= 0x80) {
			a.insertInput(key)
			a.setStatus("", false)
		}
	}
	return inputAction{}
}

type mouseKind uint8

const (
	mousePress mouseKind = iota
	mouseDrag
	mouseRelease
)

type mouseEvent struct {
	button, x, y int
	kind         mouseKind
}

func (m mouseEvent) wheel() bool   { return m.button&64 != 0 }
func (m mouseEvent) primary() bool { return m.button&3 == 0 }

func parseMouse(seq string) (mouseEvent, bool) {
	if !strings.HasPrefix(seq, "\x1b[<") || len(seq) < 7 {
		return mouseEvent{}, false
	}
	if !strings.HasSuffix(seq, "M") && !strings.HasSuffix(seq, "m") {
		return mouseEvent{}, false
	}
	body := seq[3 : len(seq)-1]
	parts := strings.Split(body, ";")
	if len(parts) != 3 {
		return mouseEvent{}, false
	}
	button, err1 := strconv.Atoi(parts[0])
	xv, err2 := strconv.Atoi(parts[1])
	yv, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return mouseEvent{}, false
	}
	kind := mousePress
	if seq[len(seq)-1] == 'm' {
		kind = mouseRelease
	} else if button&32 != 0 && button&64 == 0 {
		kind = mouseDrag
	}
	return mouseEvent{button: button, x: xv, y: yv, kind: kind}, true
}

// lineBounds returns the byte offsets of the composer line containing the
// cursor, used by Home/End and the line-delete keys.
func (a *App) lineBounds() (start, end int) {
	i := clampInputPos(a.input, a.inputPos)
	start = i
	for start > 0 && a.input[start-1] != '\n' {
		start = previousRune(a.input, start)
	}
	end = i
	for end < len(a.input) && a.input[end] != '\n' {
		end = nextRune(a.input, end)
	}
	return start, end
}

func (a *App) deleteToLineStart() {
	start, _ := a.lineBounds()
	a.input = append(a.input[:start], a.input[a.inputPos:]...)
	a.inputPos = start
	a.resetHistory()
}

func (a *App) deleteToLineEnd() {
	_, end := a.lineBounds()
	a.input = append(a.input[:a.inputPos], a.input[end:]...)
	a.resetHistory()
}

func (a *App) deleteWordBack() {
	pos := clampInputPos(a.input, a.inputPos)
	end := pos
	for end > 0 {
		r, size := utf8.DecodeLastRune(a.input[:end])
		if !unicode.IsSpace(r) {
			break
		}
		end -= size
	}
	for end > 0 {
		r, size := utf8.DecodeLastRune(a.input[:end])
		if unicode.IsSpace(r) {
			break
		}
		end -= size
	}
	a.input = append(a.input[:end], a.input[pos:]...)
	a.inputPos = end
	a.resetHistory()
}

func (a *App) moveWordBack() {
	pos := clampInputPos(a.input, a.inputPos)
	for pos > 0 {
		r, size := utf8.DecodeLastRune(a.input[:pos])
		if !unicode.IsSpace(r) {
			break
		}
		pos -= size
	}
	for pos > 0 {
		r, size := utf8.DecodeLastRune(a.input[:pos])
		if unicode.IsSpace(r) {
			break
		}
		pos -= size
	}
	a.inputPos = pos
}

func (a *App) moveWordForward() {
	pos := clampInputPos(a.input, a.inputPos)
	for pos < len(a.input) {
		r, size := utf8.DecodeRune(a.input[pos:])
		if !unicode.IsSpace(r) {
			break
		}
		pos += size
	}
	for pos < len(a.input) {
		r, size := utf8.DecodeRune(a.input[pos:])
		if unicode.IsSpace(r) {
			break
		}
		pos += size
	}
	a.inputPos = pos
}

// moveComposerLine moves the cursor one visual line up or down, keeping the
// display column. It reports false at the first or last line so the caller can
// fall back to input history.
func (a *App) moveComposerLine(delta int) bool {
	layout := a.composerLayout(a.width)
	if len(layout.lines) <= 1 {
		return false
	}
	next := layout.cursorRow + delta
	if next < 0 || next >= len(layout.lines) {
		return false
	}
	col := layout.cursorCol - 2 - displayWidth(layout.lines[layout.cursorRow].prefix)
	target := layout.lines[next]
	off := target.start
	used := 0
	for off < target.end {
		r, size := utf8.DecodeRune(a.input[off:])
		if used+runeWidth(r) > col {
			break
		}
		used += runeWidth(r)
		off += size
	}
	a.inputPos = off
	return true
}

// historyPrev and historyNext recall previously submitted tasks. The draft in
// progress is restored when recall moves past the newest entry.
func (a *App) historyPrev() {
	if len(a.history) == 0 {
		return
	}
	if a.historyIdx < 0 {
		a.historyDraft = append([]byte(nil), a.input...)
		a.historyIdx = len(a.history)
	}
	if a.historyIdx > 0 {
		a.historyIdx--
	}
	a.input = []byte(a.history[a.historyIdx])
	a.inputPos = len(a.input)
}

func (a *App) historyNext() {
	if a.historyIdx < 0 {
		return
	}
	if a.historyIdx < len(a.history)-1 {
		a.historyIdx++
		a.input = []byte(a.history[a.historyIdx])
	} else {
		a.historyIdx = -1
		a.input = append([]byte(nil), a.historyDraft...)
	}
	a.inputPos = len(a.input)
}

func (a *App) pushHistory(text string) {
	text = strings.TrimSpace(text)
	if text != "" {
		if n := len(a.history); n == 0 || a.history[n-1] != text {
			a.history = append(a.history, text)
			if len(a.history) > 100 {
				a.history = a.history[len(a.history)-100:]
			}
		}
	}
	a.resetHistory()
}

func (a *App) resetHistory() {
	a.historyIdx = -1
	a.historyDraft = nil
}

func (a *App) insertInput(s string) {
	a.resetHistory()
	a.inputPos = clampInputPos(a.input, a.inputPos)
	a.input = append(a.input, make([]byte, len(s))...)
	copy(a.input[a.inputPos+len(s):], a.input[a.inputPos:len(a.input)-len(s)])
	copy(a.input[a.inputPos:], s)
	a.inputPos += len(s)
}

func (a *App) hitNativeButton(x, y int) protocol.AgentID {
	if y != 1 || a.width < 20 {
		return ""
	}
	leftW := (a.width - 1) / 2
	if x >= 2 && x <= leftW {
		return protocol.Austin
	}
	if x >= leftW+2 && x <= a.width-1 {
		return protocol.Tony
	}
	return ""
}
