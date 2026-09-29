package tui

import (
	"strings"

	"github.com/atfa/duo/internal/protocol"
)

type selectionPoint struct{ row, col int }

type paneSelection struct {
	active bool
	moved  bool
	agent  protocol.AgentID
	start  selectionPoint
	end    selectionPoint
}

func (a *App) selectionPoint(agent protocol.AgentID, x, y int) (selectionPoint, bool) {
	if a.hitPane(x, y) != agent {
		return selectionPoint{}, false
	}
	l := a.layout()
	switch agent {
	case protocol.Austin:
		return selectionPoint{row: y - 2, col: maxInt(x-2, 0)}, true
	case protocol.Tony:
		return selectionPoint{row: y - 2, col: maxInt(x-(l.leftW+3), 0)}, true
	case protocol.Duo:
		if a.timeline {
			return selectionPoint{row: y - 2, col: maxInt(x-2, 0)}, true
		}
		return selectionPoint{row: y - l.logFirst, col: maxInt(x-3, 0)}, true
	}
	return selectionPoint{}, false
}

func (a *App) startSelection(mouse mouseEvent) {
	agent := a.hitPane(mouse.x, mouse.y)
	point, ok := a.selectionPoint(agent, mouse.x, mouse.y)
	if !ok {
		return
	}
	a.selection = paneSelection{active: true, agent: agent, start: point, end: point}
}

func (a *App) moveSelection(mouse mouseEvent) bool {
	point, ok := a.selectionPoint(a.selection.agent, mouse.x, mouse.y)
	if !ok {
		return false
	}
	a.selection.end = point
	a.selection.moved = a.selection.moved || point != a.selection.start
	return true
}

func (a *App) selectionColumns(agent protocol.AgentID, row int) (int, int, bool) {
	s := a.selection
	if !s.active || s.agent != agent {
		return 0, 0, false
	}
	start, end := s.start, s.end
	if end.row < start.row || (end.row == start.row && end.col < start.col) {
		start, end = end, start
	}
	if row < start.row || row > end.row {
		return 0, 0, false
	}
	from, to := 0, int(^uint(0)>>1)
	if row == start.row {
		from = start.col
	}
	if row == end.row {
		to = end.col + 1
	}
	return from, to, true
}

func (a *App) selectedText() string {
	s := a.selection
	if !s.active || !s.moved {
		return ""
	}
	l := a.layout()
	var lines []paneLine
	switch s.agent {
	case protocol.Austin:
		lines = a.styledPane(protocol.Austin, l.leftW, l.content, a.austinOffset)
	case protocol.Tony:
		lines = a.styledPane(protocol.Tony, l.rightW, l.content, a.tonyOffset)
	case protocol.Duo:
		if a.timeline {
			lines = a.styledPane(protocol.Duo, l.timelineW, l.content, a.duoOffset)
		} else {
			lines = a.styledPane(protocol.Duo, l.logW, duoLogRows, a.duoOffset)
		}
	default:
		return ""
	}
	selected := make([]string, 0, len(lines))
	for row, line := range lines {
		from, to, ok := a.selectionColumns(s.agent, row)
		if ok {
			selected = append(selected, strings.TrimRight(sliceColumns(line.text(), from, to), " "))
		}
	}
	return strings.TrimSpace(strings.Join(selected, "\n"))
}

// sliceColumns returns runes touched by [from, to), using terminal display
// columns so a wide rune is never cut in half.
func sliceColumns(s string, from, to int) string {
	runes := []rune(s)
	var b strings.Builder
	col := 0
	for i, r := range runes {
		next := col + runeWidthNear(r, runeAfter(runes, i))
		if next > from && col < to {
			b.WriteRune(r)
		}
		col = next
	}
	return b.String()
}
