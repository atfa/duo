package tui

import (
	"strconv"
	"strings"

	"github.com/atfa/duo/internal/protocol"
)

type actionKind int

const (
	actionNone actionKind = iota
	actionSubmit
	actionQuit
	actionToggleHelp
	actionCloseHelp
	actionScrollUp
	actionScrollDown
	actionPageUp
	actionPageDown
	actionHome
	actionEnd
	actionAttach
	actionRestart
)

type inputAction struct {
	kind  actionKind
	agent protocol.AgentID
}

func (a *App) handleByte(b byte) inputAction {
	if len(a.escBuf) > 0 || b == 0x1b {
		a.escBuf = append(a.escBuf, b)
		if len(a.escBuf) > 64 {
			a.escBuf = nil
			return inputAction{}
		}
		if b == 'M' || b == 'm' || b == '~' || b == 'u' || (len(a.escBuf) >= 3 && ((b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z'))) {
			seq := string(a.escBuf)
			a.escBuf = nil
			if x, y, ok := parseMouse(seq); ok {
				if a.view == viewMain {
					return inputAction{kind: actionAttach, agent: a.hitNativeButton(x, y)}
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
	case 13, 10:
		return "enter"
	case 127, 8:
		return "backspace"
	}
	return string([]byte{b})
}

func decodeEscape(seq string) string {
	switch seq {
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
	case "\x1b[47;5u", "\x1b[27;5;47~":
		return "ctrl-slash"
	}
	return ""
}

func (a *App) handleKey(key string) inputAction {
	if key == "ctrl-q" {
		return inputAction{kind: actionQuit}
	}
	if a.view == viewHelp {
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
	case "enter":
		return inputAction{kind: actionSubmit}
	case "backspace":
		a.input = popRune(a.input)
	default:
		if (len(key) == 1 && key[0] >= 32) || (len(key) == 1 && key[0] >= 0x80) {
			a.input = append(a.input, key...)
			a.setStatus("", false)
		}
	}
	return inputAction{}
}

func parseMouse(seq string) (x, y int, ok bool) {
	if !strings.HasPrefix(seq, "\x1b[<") || len(seq) < 7 {
		return 0, 0, false
	}
	body := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b[<"), "M"), "m")
	parts := strings.Split(body, ";")
	if len(parts) != 3 {
		return 0, 0, false
	}
	button, err1 := strconv.Atoi(parts[0])
	xv, err2 := strconv.Atoi(parts[1])
	yv, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || button != 0 {
		return 0, 0, false
	}
	return xv, yv, true
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
