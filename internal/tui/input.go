package tui

import (
	"strconv"
	"strings"

	"github.com/atfa/duo/internal/protocol"
)

type inputAction struct {
	submit  bool
	quit    bool
	attach  protocol.AgentID
	restart protocol.AgentID
}

func (a *App) handleByte(b byte) inputAction {
	if len(a.escBuf) > 0 || b == 0x1b {
		a.escBuf = append(a.escBuf, b)
		if len(a.escBuf) > 64 {
			a.escBuf = nil
			return inputAction{}
		}
		if b == 'M' || b == 'm' || b == '~' || (len(a.escBuf) >= 3 && ((b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z'))) {
			seq := string(a.escBuf)
			a.escBuf = nil
			if x, y, ok := parseMouse(seq); ok {
				return inputAction{attach: a.hitNativeButton(x, y)}
			}
		}
		return inputAction{}
	}

	switch b {
	case 1: // Ctrl+A
		return inputAction{attach: protocol.Austin}
	case 20: // Ctrl+T
		return inputAction{attach: protocol.Tony}
	case 18: // Ctrl+R: restart Austin (failed/exited only)
		return inputAction{restart: protocol.Austin}
	case 25: // Ctrl+Y: restart Tony (failed/exited only)
		return inputAction{restart: protocol.Tony}
	case 17: // Ctrl+Q
		return inputAction{quit: true}
	case 13, 10:
		return inputAction{submit: true}
	case 127, 8:
		a.input = popRune(a.input)
	default:
		if b >= 32 || b >= 0x80 {
			a.input = append(a.input, b)
			a.status = ""
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
	x, y = xv, yv
	return x, y, true
}

func (a *App) hitNativeButton(x, y int) protocol.AgentID {
	if y != 1 || a.width < 20 {
		return ""
	}
	leftW := (a.width - 1) / 2
	// The whole header is deliberately clickable in v0.3.1; [↗] is the visual affordance.
	if x >= 2 && x <= leftW {
		return protocol.Austin
	}
	if x >= leftW+2 && x <= a.width-1 {
		return protocol.Tony
	}
	return ""
}
