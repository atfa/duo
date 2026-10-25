package gotui

import (
	"fmt"
	"image"
	"strings"
	"time"

	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui/v5"
)

type entry struct {
	at      time.Time
	pane    string
	text    string
	err     bool
	warn    bool
	summary bool
	label   string
	seq     int
}

type selection struct {
	active       bool
	startLine    int
	startCol     int
	endLine      int
	endCol       int
	dragging     bool
	anchorX      int
	anchorY      int
	lastCopyText string
}

type transcript struct {
	ui.Block
	theme          theme
	entries        []entry
	lines          []renderLine
	width          int
	height         int
	showTimestamps bool
	follow         bool
	offset         int
	newLines       int
	emptyText      string
	pathRoots      map[string][]string
	sel            selection
	// Search hits are cell-column ranges on the rendered lines and the index of
	// the emphasised one. The app owns the query and computes the hits; the
	// transcript only draws them, so a search never becomes a second source of
	// transcript state.
	searchHits []searchHit
	searchHit  int
	entrySeq   int
}

func newTranscript(t theme) *transcript {
	v := &transcript{
		Block:          *ui.NewBlock(),
		theme:          t,
		follow:         true,
		emptyText:      "No messages yet.",
		showTimestamps: true,
	}
	v.BorderRounded = true
	v.BorderStyle = ui.Style{Fg: t.Border, Bg: t.Bg}
	v.BackgroundColor = t.Bg
	v.TitleStyle = ui.Style{Fg: t.Muted, Bg: t.Bg}
	v.PaddingLeft = 1
	v.PaddingRight = 1
	return v
}

func (v *transcript) SetEntries(entries []entry, showTimestamps bool) {
	if showTimestamps == v.showTimestamps && len(entries) == len(v.entries) {
		same := true
		for i := range entries {
			if entries[i].seq != v.entries[i].seq {
				same = false
				break
			}
		}
		if same {
			return
		}
	}
	v.entries = entries
	v.showTimestamps = showTimestamps
	v.width, v.height = -1, -1
	v.rebuild()
}

func (v *transcript) SetEmptyText(text string) { v.emptyText = text }

func (v *transcript) SetPathRoots(roots map[string][]string) {
	if samePathRoots(v.pathRoots, roots) {
		return
	}
	v.pathRoots = clonePathRoots(roots)
	v.width = -1
	v.rebuild()
}

func samePathRoots(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, left := range a {
		right, ok := b[key]
		if !ok || len(left) != len(right) {
			return false
		}
		for i := range left {
			if left[i] != right[i] {
				return false
			}
		}
	}
	return true
}

func clonePathRoots(roots map[string][]string) map[string][]string {
	if roots == nil {
		return nil
	}
	out := make(map[string][]string, len(roots))
	for key, values := range roots {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func (v *transcript) rebuild() {
	width := v.Inner.Dx()
	height := v.Inner.Dy()
	if width == v.width && height == v.height && v.lines != nil {
		return
	}
	oldBottom := len(v.lines)
	v.width, v.height = width, height
	v.rebuildLines(width)
	if v.follow {
		v.offset = max(0, len(v.lines)-height)
		v.newLines = 0
	} else {
		// Keep the same content in view when the width changes; the number of
		// rows may grow because text reflows.
		if oldBottom > 0 && len(v.lines) > oldBottom {
			v.offset += len(v.lines) - oldBottom
		}
		v.clampOffset()
	}
}

func (v *transcript) Rebuild(force bool) {
	if force {
		v.width, v.height = -1, -1
	}
	v.rebuild()
}

func (v *transcript) rebuildLines(width int) {
	v.lines = nil
	if width < 4 {
		width = 4
	}
	bodyWidth := max(1, width-2)
	for _, e := range v.entries {
		if strings.TrimSpace(e.text) == "" {
			continue
		}
		label := e.label
		if label == "" {
			label = e.pane
		}
		if label == "" {
			label = "Duo"
		}
		headerStyle := v.theme.agentStyle(e.pane)
		if e.err {
			headerStyle = v.theme.Error
		} else if e.warn {
			headerStyle = v.theme.Warning
		}
		header := label
		if e.summary {
			header += "  · " + v.theme.tr("FINAL REPORT")
		}
		var headerRuns []textRun
		if v.showTimestamps {
			headerRuns = append(headerRuns, textRun{e.at.Format("15:04"), v.theme.Timestamp, linkTarget{}}, textRun{"  ", v.theme.Timestamp, linkTarget{}})
		}
		headerRuns = append(headerRuns, textRun{header, headerStyle, linkTarget{}})
		if e.summary {
			headerRuns = append(headerRuns, textRun{"  ✓", v.theme.Success, linkTarget{}})
		}
		wrappedHeader := wrapRuns(headerRuns, width)
		for _, line := range wrappedHeader {
			line.entry = e.seq
			line.header = true
			line.summary = e.summary
			v.lines = append(v.lines, line)
		}
		base := v.theme.Body
		if e.err {
			base = v.theme.Error
		} else if e.warn {
			base = v.theme.Warning
		}
		runs := markdownRuns(e.text, base, v.theme, bodyWidth)
		runs = linkifyRuns(runs, v.pathRoots[e.pane], v.theme)
		wrappedBody := wrapRuns(runs, bodyWidth)
		for _, line := range wrappedBody {
			line.entry = e.seq
			line.runs = append([]textRun{{"  ", base, linkTarget{}}}, line.runs...)
			v.lines = append(v.lines, line)
		}
		v.lines = append(v.lines, renderLine{entry: e.seq})
	}
	if len(v.lines) > 0 && v.lines[len(v.lines)-1].text() == "" {
		v.lines = v.lines[:len(v.lines)-1]
	}
}

func (v *transcript) clampOffset() {
	maxOffset := max(0, len(v.lines)-v.Inner.Dy())
	if v.offset > maxOffset {
		v.offset = maxOffset
	}
	if v.offset < 0 {
		v.offset = 0
	}
}

func (v *transcript) atBottom() bool {
	return v.offset >= max(0, len(v.lines)-v.Inner.Dy())
}

func (v *transcript) Scroll(delta int) {
	if len(v.lines) <= v.Inner.Dy() {
		return
	}
	v.offset += delta
	v.clampOffset()
	v.follow = v.atBottom()
	if v.follow {
		v.newLines = 0
	}
}

func (v *transcript) Draw(buf *ui.Buffer) {
	v.Block.Draw(buf)
	if v.Inner.Dx() <= 1 || v.Inner.Dy() <= 0 {
		return
	}
	v.rebuild()
	if len(v.lines) == 0 {
		if v.emptyText != "" {
			buf.SetString(fitText(v.theme.tr(v.emptyText), v.Inner.Dx()), v.theme.Subtle, image.Pt(v.Inner.Min.X, v.Inner.Min.Y))
		}
		return
	}
	v.clampOffset()
	height := v.Inner.Dy()
	for y := 0; y < height; y++ {
		idx := v.offset + y
		if idx >= len(v.lines) {
			break
		}
		line := v.lines[idx]
		x := v.Inner.Min.X
		hits, hitBase := v.searchHitsOnLine(idx)
		hit := 0
		for _, run := range line.runs {
			for _, r := range run.text {
				w := rw.RuneWidth(r)
				if w < 1 {
					w = 0
				}
				if x+w > v.Inner.Max.X {
					break
				}
				col := x - v.Inner.Min.X
				style := run.style
				for hit < len(hits) && hits[hit].end <= col {
					hit++
				}
				if hit < len(hits) && col >= hits[hit].col {
					style = v.theme.searchStyle(hitBase+hit == v.searchHit)
				}
				// A text selection outranks a search highlight: the human marking
				// text to copy must see exactly what will be copied.
				if v.sel.active && selectedCell(v.sel, idx, col) {
					style = v.theme.Selected
				}
				buf.SetCell(ui.NewCell(r, style), image.Pt(x, v.Inner.Min.Y+y))
				x += w
			}
			if x >= v.Inner.Max.X {
				break
			}
		}
	}
	v.drawScrollbar(buf)
	if !v.follow {
		label := fmt.Sprintf(" ↓ %d %s ", max(v.newLines, len(v.lines)-(v.offset+height)), v.theme.tr("new"))
		if v.newLines <= 0 {
			label = " ↓ " + v.theme.tr("more") + " "
		}
		x := max(v.Inner.Min.X, v.Inner.Max.X-rw.StringWidth(label))
		buf.SetString(fitText(label, v.Inner.Dx()), ui.Style{Fg: v.theme.Bg, Bg: v.theme.Accent, Modifier: ui.ModifierBold}, image.Pt(x, v.Inner.Max.Y-1))
	}
}

func (v *transcript) drawScrollbar(buf *ui.Buffer) {
	if len(v.lines) <= v.Inner.Dy() || v.Inner.Dx() < 8 {
		return
	}
	x := v.Inner.Max.X - 1
	track := v.Inner.Dy()
	size := max(1, track*track/len(v.lines))
	if size > track {
		size = track
	}
	pos := 0
	if maxOffset := len(v.lines) - track; maxOffset > 0 {
		pos = v.offset * (track - size) / maxOffset
	}
	for y := 0; y < track; y++ {
		ch := '│'
		style := ui.Style{Fg: v.theme.Faint, Bg: v.theme.Bg}
		if y >= pos && y < pos+size {
			ch = '┃'
			style = ui.Style{Fg: v.theme.Muted, Bg: v.theme.Bg}
		}
		buf.SetCell(ui.NewCell(ch, style), image.Pt(x, v.Inner.Min.Y+y))
	}
}

func selectedCell(sel selection, line, col int) bool {
	if !sel.active {
		return false
	}
	sl, sc, el, ec := sel.startLine, sel.startCol, sel.endLine, sel.endCol
	if sl > el || (sl == el && sc > ec) {
		sl, sc, el, ec = el, ec, sl, sc
	}
	if line < sl || line > el {
		return false
	}
	if sl == el {
		return col >= sc && col < ec
	}
	if line == sl {
		return col >= sc
	}
	if line == el {
		return col < ec
	}
	return true
}

func (v *transcript) ClearSelection() { v.sel = selection{} }

func (v *transcript) StartSelection(x, y int) bool {
	line, col, ok := v.point(x, y)
	if !ok {
		return false
	}
	v.sel = selection{active: true, startLine: line, startCol: col, endLine: line, endCol: col, dragging: true, anchorX: x, anchorY: y}
	return true
}

func (v *transcript) DragSelection(x, y int) {
	if !v.sel.dragging {
		return
	}
	line, col, ok := v.pointClamped(x, y)
	if !ok {
		return
	}
	v.sel.endLine, v.sel.endCol = line, col
	v.sel.active = true
}

func (v *transcript) FinishSelection() (string, bool) {
	if !v.sel.dragging {
		return "", false
	}
	v.sel.dragging = false
	text := v.SelectionText()
	if strings.TrimSpace(text) == "" {
		v.sel.active = false
		return "", false
	}
	v.sel.lastCopyText = text
	return text, true
}

func (v *transcript) SelectionText() string {
	if !v.sel.active {
		return ""
	}
	sl, sc, el, ec := v.sel.startLine, v.sel.startCol, v.sel.endLine, v.sel.endCol
	if sl > el || (sl == el && sc > ec) {
		sl, sc, el, ec = el, ec, sl, sc
	}
	var b strings.Builder
	for line := sl; line <= el && line < len(v.lines); line++ {
		start, end := 0, 1<<30
		if line == sl {
			start = sc
		}
		if line == el {
			end = ec
		}
		text := lineSlice(v.lines[line], start, end)
		if line > sl {
			b.WriteByte('\n')
		}
		b.WriteString(strings.TrimRight(text, " "))
	}
	return strings.TrimSpace(b.String())
}

func (v *transcript) point(x, y int) (int, int, bool) {
	if x < v.Inner.Min.X || x >= v.Inner.Max.X || y < v.Inner.Min.Y || y >= v.Inner.Max.Y {
		return 0, 0, false
	}
	line := v.offset + (y - v.Inner.Min.Y)
	if line < 0 || line >= len(v.lines) {
		return 0, 0, false
	}
	return line, x - v.Inner.Min.X, true
}

func (v *transcript) pointClamped(x, y int) (int, int, bool) {
	if len(v.lines) == 0 || v.Inner.Dx() <= 0 || v.Inner.Dy() <= 0 {
		return 0, 0, false
	}
	if x < v.Inner.Min.X {
		x = v.Inner.Min.X
	}
	if x >= v.Inner.Max.X {
		x = v.Inner.Max.X - 1
	}
	if y < v.Inner.Min.Y {
		y = v.Inner.Min.Y
	}
	if y >= v.Inner.Max.Y {
		y = v.Inner.Max.Y - 1
	}
	line := v.offset + (y - v.Inner.Min.Y)
	if line < 0 {
		line = 0
	}
	if line >= len(v.lines) {
		line = len(v.lines) - 1
	}
	return line, x - v.Inner.Min.X, true
}

func (v *transcript) Contains(x, y int) bool {
	return image.Pt(x, y).In(v.Inner)
}
