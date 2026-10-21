package gotui

import (
	"fmt"
	"image"
	"strings"
	"unicode"

	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui/v5"
)

type visualRow struct {
	row, start, end int
}

type pasteSpan struct {
	start     int
	end       int
	raw       string
	label     string
	lineCount int
}

type composer struct {
	ui.Block
	lines       [][]rune
	spans       [][]pasteSpan
	row, col    int
	goalCol     int
	scroll      int
	placeholder string
	theme       theme
}

func newComposer(t theme) *composer {
	c := &composer{
		Block:       *ui.NewBlock(),
		lines:       [][]rune{{}},
		spans:       [][]pasteSpan{{}},
		row:         0,
		col:         0,
		placeholder: "Message Duo…  Enter sends · Ctrl+Enter newline",
		theme:       t,
	}
	c.BorderRounded = true
	c.BorderStyle = ui.Style{Fg: t.Border, Bg: t.Panel}
	c.BackgroundColor = t.Panel
	c.Title = " Message "
	c.TitleStyle = ui.Style{Fg: t.Accent, Bg: t.Panel, Modifier: ui.ModifierBold}
	c.PaddingLeft = 1
	c.PaddingRight = 1
	return c
}

func (c *composer) SetText(text string) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	parts := strings.Split(text, "\n")
	c.lines = make([][]rune, len(parts))
	c.spans = make([][]pasteSpan, len(parts))
	for i, part := range parts {
		c.lines[i] = []rune(part)
	}
	c.row = len(c.lines) - 1
	c.col = len(c.lines[c.row])
	c.goalCol = -1
}

func (c *composer) Text() string {
	parts := make([]string, len(c.lines))
	for i, line := range c.lines {
		parts[i] = string(line)
	}
	return strings.Join(parts, "\n")
}

func (c *composer) DisplayText() string {
	return c.Text()
}

func (c *composer) ExpandedText() string {
	parts := make([]string, len(c.lines))
	for i, line := range c.lines {
		if i >= len(c.spans) || len(c.spans[i]) == 0 {
			parts[i] = string(line)
			continue
		}
		var b strings.Builder
		last := 0
		for _, s := range c.spans[i] {
			if s.start > last && s.start <= len(line) {
				b.WriteString(string(line[last:s.start]))
			}
			b.WriteString(s.raw)
			last = s.end
		}
		if last < len(line) {
			b.WriteString(string(line[last:]))
		}
		parts[i] = b.String()
	}
	return strings.Join(parts, "\n")
}

func (c *composer) RawText() string {
	return c.ExpandedText()
}

func (c *composer) PasteBlocks() []pasteSpan {
	var all []pasteSpan
	for _, rowSpans := range c.spans {
		all = append(all, rowSpans...)
	}
	return all
}

func (c *composer) Empty() bool { return len(c.Text()) == 0 }

// Cursor returns the edit position as a row and column.
func (c *composer) Cursor() (row, col int) { return c.row, c.col }

// CurrentLine returns a copy of the line being edited.
func (c *composer) CurrentLine() []rune {
	return append([]rune(nil), c.lines[c.row]...)
}

func (c *composer) spanEndingAt(row, col int) (int, *pasteSpan) {
	if row < 0 || row >= len(c.spans) {
		return -1, nil
	}
	for i := range c.spans[row] {
		s := &c.spans[row][i]
		if s.end == col {
			return i, s
		}
	}
	return -1, nil
}

func (c *composer) spanStartingAt(row, col int) (int, *pasteSpan) {
	if row < 0 || row >= len(c.spans) {
		return -1, nil
	}
	for i := range c.spans[row] {
		s := &c.spans[row][i]
		if s.start == col {
			return i, s
		}
	}
	return -1, nil
}

func (c *composer) spanCovering(row, col int) (int, *pasteSpan) {
	if row < 0 || row >= len(c.spans) {
		return -1, nil
	}
	for i := range c.spans[row] {
		s := &c.spans[row][i]
		if col > s.start && col < s.end {
			return i, s
		}
	}
	return -1, nil
}

func (c *composer) snapCursorOutOfSpans() {
	if _, span := c.spanCovering(c.row, c.col); span != nil {
		if c.col-span.start < span.end-c.col {
			c.col = span.start
		} else {
			c.col = span.end
		}
	}
}

// ReplaceRange replaces a single-line span with text and leaves the cursor at
// the end of the replacement. It is what turns an @ token into a completed path
// without rebuilding the whole composer text.
func (c *composer) ReplaceRange(row, start, end int, text string) bool {
	if row < 0 || row >= len(c.lines) {
		return false
	}
	line := c.lines[row]
	if start < 0 || end < start || end > len(line) || strings.ContainsRune(text, '\n') || strings.ContainsRune(text, '\r') {
		return false
	}
	replacement := []rune(text)
	updated := make([]rune, 0, len(line)-(end-start)+len(replacement))
	updated = append(updated, line[:start]...)
	updated = append(updated, replacement...)
	updated = append(updated, line[end:]...)
	c.lines[row] = updated

	if row < len(c.spans) {
		delta := len(replacement) - (end - start)
		var surviving []pasteSpan
		for _, s := range c.spans[row] {
			if s.end <= start {
				surviving = append(surviving, s)
			} else if s.start >= end {
				s.start += delta
				s.end += delta
				surviving = append(surviving, s)
			}
		}
		c.spans[row] = surviving
	}

	c.row, c.col = row, start+len(replacement)
	c.goalCol = -1
	return true
}

func (c *composer) Clear() {
	c.lines = [][]rune{{}}
	c.spans = [][]pasteSpan{{}}
	c.row, c.col, c.goalCol, c.scroll = 0, 0, -1, 0
}

func (c *composer) insertRune(r rune) {
	c.snapCursorOutOfSpans()
	line := c.lines[c.row]
	line = append(line, 0)
	copy(line[c.col+1:], line[c.col:])
	line[c.col] = r
	c.lines[c.row] = line

	if c.row < len(c.spans) {
		for i := range c.spans[c.row] {
			if c.spans[c.row][i].start >= c.col {
				c.spans[c.row][i].start++
				c.spans[c.row][i].end++
			}
		}
	}

	c.col++
	c.goalCol = -1
}

func (c *composer) InsertString(s string) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	for _, r := range s {
		if r == '\n' {
			c.InsertNewline()
			continue
		}
		if r == '\t' {
			for i := 0; i < 4; i++ {
				c.insertRune(' ')
			}
			continue
		}
		if r < ' ' {
			continue
		}
		c.insertRune(r)
	}
}

func (c *composer) isSuperLong(s string) bool {
	norm := strings.ReplaceAll(s, "\r\n", "\n")
	norm = strings.ReplaceAll(norm, "\r", "\n")
	trimmed := strings.TrimSuffix(norm, "\n")
	if trimmed == "" {
		return false
	}
	lines := strings.Count(trimmed, "\n") + 1
	return lines >= 3 || len([]rune(trimmed)) >= 300
}

func (c *composer) InsertPaste(text string) {
	if c.isSuperLong(text) {
		c.insertPasteBlock(text)
		return
	}
	c.InsertString(text)
}

func (c *composer) insertPasteBlock(text string) {
	norm := strings.ReplaceAll(text, "\r\n", "\n")
	norm = strings.ReplaceAll(norm, "\r", "\n")
	trimmed := strings.TrimSuffix(norm, "\n")
	lineCount := strings.Count(trimmed, "\n") + 1
	if lineCount < 1 {
		lineCount = 1
	}
	label := fmt.Sprintf("[%d Lines]", lineCount)
	labelRunes := []rune(label)

	c.snapCursorOutOfSpans()
	line := c.lines[c.row]
	newLine := make([]rune, 0, len(line)+len(labelRunes))
	newLine = append(newLine, line[:c.col]...)
	newLine = append(newLine, labelRunes...)
	newLine = append(newLine, line[c.col:]...)
	c.lines[c.row] = newLine

	shift := len(labelRunes)
	if c.row < len(c.spans) {
		for i := range c.spans[c.row] {
			if c.spans[c.row][i].start >= c.col {
				c.spans[c.row][i].start += shift
				c.spans[c.row][i].end += shift
			}
		}
	}

	newSpan := pasteSpan{
		start:     c.col,
		end:       c.col + shift,
		raw:       text,
		label:     label,
		lineCount: lineCount,
	}

	spans := c.spans[c.row]
	idx := 0
	for idx < len(spans) && spans[idx].start < newSpan.start {
		idx++
	}
	spans = append(spans, pasteSpan{})
	copy(spans[idx+1:], spans[idx:])
	spans[idx] = newSpan
	c.spans[c.row] = spans

	c.col += shift
	c.goalCol = -1
}

func (c *composer) InsertNewline() {
	c.snapCursorOutOfSpans()
	line := c.lines[c.row]
	left := append([]rune(nil), line[:c.col]...)
	right := append([]rune(nil), line[c.col:]...)
	c.lines[c.row] = left
	c.lines = append(c.lines, nil)
	copy(c.lines[c.row+2:], c.lines[c.row+1:])
	c.lines[c.row+1] = right

	var leftSpans, rightSpans []pasteSpan
	if c.row < len(c.spans) {
		for _, s := range c.spans[c.row] {
			if s.end <= c.col {
				leftSpans = append(leftSpans, s)
			} else if s.start >= c.col {
				s.start -= c.col
				s.end -= c.col
				rightSpans = append(rightSpans, s)
			} else {
				s.end = c.col
				leftSpans = append(leftSpans, s)
			}
		}
	}
	c.spans[c.row] = leftSpans
	c.spans = append(c.spans, nil)
	copy(c.spans[c.row+2:], c.spans[c.row+1:])
	c.spans[c.row+1] = rightSpans

	c.row++
	c.col = 0
	c.goalCol = -1
}

func (c *composer) Backspace() bool {
	idx, span := c.spanEndingAt(c.row, c.col)
	if span == nil {
		idx, span = c.spanCovering(c.row, c.col)
	}
	if span != nil {
		start, end := span.start, span.end
		spanLen := end - start
		line := c.lines[c.row]
		c.lines[c.row] = append(line[:start], line[end:]...)
		c.col = start

		spans := c.spans[c.row]
		spans = append(spans[:idx], spans[idx+1:]...)
		for i := idx; i < len(spans); i++ {
			spans[i].start -= spanLen
			spans[i].end -= spanLen
		}
		c.spans[c.row] = spans
		c.goalCol = -1
		return true
	}

	if c.col > 0 {
		line := c.lines[c.row]
		c.lines[c.row] = append(line[:c.col-1], line[c.col:]...)
		c.col--
		if c.row < len(c.spans) {
			for i := range c.spans[c.row] {
				if c.spans[c.row][i].start > c.col {
					c.spans[c.row][i].start--
					c.spans[c.row][i].end--
				}
			}
		}
		c.goalCol = -1
		return true
	}
	if c.row > 0 {
		prev := c.lines[c.row-1]
		cur := c.lines[c.row]
		prevLen := len(prev)
		c.col = prevLen
		c.lines[c.row-1] = append(prev, cur...)
		c.lines = append(c.lines[:c.row], c.lines[c.row+1:]...)

		if c.row < len(c.spans) {
			for _, s := range c.spans[c.row] {
				s.start += prevLen
				s.end += prevLen
				c.spans[c.row-1] = append(c.spans[c.row-1], s)
			}
			c.spans = append(c.spans[:c.row], c.spans[c.row+1:]...)
		}
		c.row--
		c.goalCol = -1
		return true
	}
	return false
}

func (c *composer) Delete() bool {
	idx, span := c.spanStartingAt(c.row, c.col)
	if span == nil {
		idx, span = c.spanCovering(c.row, c.col)
	}
	if span != nil {
		start, end := span.start, span.end
		spanLen := end - start
		line := c.lines[c.row]
		c.lines[c.row] = append(line[:start], line[end:]...)
		c.col = start

		spans := c.spans[c.row]
		spans = append(spans[:idx], spans[idx+1:]...)
		for i := idx; i < len(spans); i++ {
			spans[i].start -= spanLen
			spans[i].end -= spanLen
		}
		c.spans[c.row] = spans
		c.goalCol = -1
		return true
	}

	line := c.lines[c.row]
	if c.col < len(line) {
		c.lines[c.row] = append(line[:c.col], line[c.col+1:]...)
		if c.row < len(c.spans) {
			for i := range c.spans[c.row] {
				if c.spans[c.row][i].start > c.col {
					c.spans[c.row][i].start--
					c.spans[c.row][i].end--
				}
			}
		}
		c.goalCol = -1
		return true
	}
	if c.row+1 < len(c.lines) {
		curLen := len(line)
		c.lines[c.row] = append(line, c.lines[c.row+1]...)
		c.lines = append(c.lines[:c.row+1], c.lines[c.row+2:]...)

		if c.row+1 < len(c.spans) {
			for _, s := range c.spans[c.row+1] {
				s.start += curLen
				s.end += curLen
				c.spans[c.row] = append(c.spans[c.row], s)
			}
			c.spans = append(c.spans[:c.row+1], c.spans[c.row+2:]...)
		}
		c.goalCol = -1
		return true
	}
	return false
}

func (c *composer) Left() {
	if _, span := c.spanEndingAt(c.row, c.col); span != nil {
		c.col = span.start
		c.goalCol = -1
		return
	}
	if _, span := c.spanCovering(c.row, c.col); span != nil {
		c.col = span.start
		c.goalCol = -1
		return
	}
	if c.col > 0 {
		c.col--
		if _, span := c.spanCovering(c.row, c.col); span != nil {
			c.col = span.start
		}
	} else if c.row > 0 {
		c.row--
		c.col = len(c.lines[c.row])
	}
	c.goalCol = -1
}

func (c *composer) Right() {
	if _, span := c.spanStartingAt(c.row, c.col); span != nil {
		c.col = span.end
		c.goalCol = -1
		return
	}
	if _, span := c.spanCovering(c.row, c.col); span != nil {
		c.col = span.end
		c.goalCol = -1
		return
	}
	if c.col < len(c.lines[c.row]) {
		c.col++
		if _, span := c.spanCovering(c.row, c.col); span != nil {
			c.col = span.end
		}
	} else if c.row+1 < len(c.lines) {
		c.row++
		c.col = 0
	}
	c.goalCol = -1
}

func (c *composer) WordLeft() {
	if c.col == 0 {
		c.Left()
		return
	}
	if _, span := c.spanEndingAt(c.row, c.col); span != nil {
		c.col = span.start
		c.goalCol = -1
		return
	}
	for c.col > 0 && unicode.IsSpace(c.lines[c.row][c.col-1]) {
		c.col--
		if _, span := c.spanEndingAt(c.row, c.col); span != nil {
			c.col = span.start
			c.goalCol = -1
			return
		}
	}
	for c.col > 0 && !unicode.IsSpace(c.lines[c.row][c.col-1]) {
		c.col--
		if _, span := c.spanEndingAt(c.row, c.col); span != nil {
			c.col = span.start
			c.goalCol = -1
			return
		}
	}
	c.snapCursorOutOfSpans()
	c.goalCol = -1
}

func (c *composer) WordRight() {
	line := c.lines[c.row]
	if c.col >= len(line) {
		c.Right()
		return
	}
	if _, span := c.spanStartingAt(c.row, c.col); span != nil {
		c.col = span.end
		c.goalCol = -1
		return
	}
	for c.col < len(line) && !unicode.IsSpace(line[c.col]) {
		c.col++
		if _, span := c.spanStartingAt(c.row, c.col); span != nil {
			c.col = span.end
			c.goalCol = -1
			return
		}
	}
	for c.col < len(line) && unicode.IsSpace(line[c.col]) {
		c.col++
		if _, span := c.spanStartingAt(c.row, c.col); span != nil {
			c.col = span.end
			c.goalCol = -1
			return
		}
	}
	c.snapCursorOutOfSpans()
	c.goalCol = -1
}

func (c *composer) Home() { c.col = 0; c.goalCol = -1 }
func (c *composer) End()  { c.col = len(c.lines[c.row]); c.goalCol = -1 }

func (c *composer) DeleteToStart() {
	line := c.lines[c.row]
	c.lines[c.row] = append([]rune(nil), line[c.col:]...)
	if c.row < len(c.spans) {
		var surviving []pasteSpan
		for _, s := range c.spans[c.row] {
			if s.start >= c.col {
				s.start -= c.col
				s.end -= c.col
				surviving = append(surviving, s)
			}
		}
		c.spans[c.row] = surviving
	}
	c.col = 0
	c.goalCol = -1
}

func (c *composer) DeleteToEnd() {
	c.lines[c.row] = c.lines[c.row][:c.col]
	if c.row < len(c.spans) {
		var surviving []pasteSpan
		for _, s := range c.spans[c.row] {
			if s.end <= c.col {
				surviving = append(surviving, s)
			}
		}
		c.spans[c.row] = surviving
	}
	c.goalCol = -1
}

func (c *composer) DeleteWord() bool {
	if c.col == 0 {
		return c.Backspace()
	}
	if _, span := c.spanEndingAt(c.row, c.col); span != nil {
		return c.Backspace()
	}
	start := c.col
	for start > 0 && unicode.IsSpace(c.lines[c.row][start-1]) {
		start--
	}
	for start > 0 && !unicode.IsSpace(c.lines[c.row][start-1]) {
		start--
	}
	delta := c.col - start
	line := c.lines[c.row]
	c.lines[c.row] = append(c.lines[c.row][:start], line[c.col:]...)

	if c.row < len(c.spans) {
		var surviving []pasteSpan
		for _, s := range c.spans[c.row] {
			if s.end <= start {
				surviving = append(surviving, s)
			} else if s.start >= c.col {
				s.start -= delta
				s.end -= delta
				surviving = append(surviving, s)
			}
		}
		c.spans[c.row] = surviving
	}

	c.col = start
	c.goalCol = -1
	return true
}

func (c *composer) MoveVertical(dy int) {
	rows := c.visualRows(c.textWidth())
	if len(rows) == 0 {
		return
	}
	cur := c.currentVisual(rows)
	if c.goalCol < 0 {
		c.goalCol = c.visualColumn(rows[cur])
	}
	target := cur + dy
	if target < 0 || target >= len(rows) {
		return
	}
	r := rows[target]
	want := c.goalCol
	if want > rw.StringWidth(string(c.lines[r.row][r.start:r.end])) {
		want = rw.StringWidth(string(c.lines[r.row][r.start:r.end]))
	}
	col := r.start
	x := 0
	for col < r.end {
		w := rw.RuneWidth(c.lines[r.row][col])
		if w < 1 {
			w = 0
		}
		if x+w > want {
			break
		}
		x += w
		col++
	}
	c.row, c.col = r.row, col
	c.snapCursorOutOfSpans()
}

func (c *composer) AtFirstVisual() bool {
	rows := c.visualRows(c.textWidth())
	return c.currentVisual(rows) == 0
}

func (c *composer) AtLastVisual() bool {
	rows := c.visualRows(c.textWidth())
	return c.currentVisual(rows) >= len(rows)-1
}

func (c *composer) textWidth() int {
	w := c.Inner.Dx() - 2
	if w < 1 {
		w = 1
	}
	return w
}

func (c *composer) visualRows(width int) []visualRow {
	if width < 1 {
		width = 1
	}
	var out []visualRow
	for row, line := range c.lines {
		if len(line) == 0 {
			out = append(out, visualRow{row: row, start: 0, end: 0})
			continue
		}
		start, cells := 0, 0
		for i, r := range line {
			w := rw.RuneWidth(r)
			if w < 1 {
				w = 0
			}
			if cells > 0 && cells+w > width {
				out = append(out, visualRow{row: row, start: start, end: i})
				start, cells = i, 0
			}
			cells += w
			if cells >= width {
				out = append(out, visualRow{row: row, start: start, end: i + 1})
				start, cells = i+1, 0
			}
		}
		if start < len(line) || len(out) == 0 || out[len(out)-1].row != row {
			out = append(out, visualRow{row: row, start: start, end: len(line)})
		}
		// A line ending exactly at the right edge still needs a cell for the
		// cursor. Give the last line an empty visual row instead of letting the
		// cursor fall back onto its final character. At one row high there is no
		// room for both the text and the cursor row; prepare keeps the cursor in
		// the right padding instead.
		if c.Inner.Dy() > 1 && row == len(c.lines)-1 && len(line) > 0 && start == len(line) && cells == 0 {
			out = append(out, visualRow{row: row, start: start, end: start})
		}
	}
	return out
}

func (c *composer) currentVisual(rows []visualRow) int {
	for i, r := range rows {
		if r.row != c.row {
			continue
		}
		if c.col >= r.start && c.col <= r.end {
			// A soft wrap shares its boundary between two rows. The cursor is
			// after the last cell of this row, so it belongs at the start of the
			// next one rather than visually covering the final character.
			if c.col == r.end && i+1 < len(rows) && rows[i+1].row == c.row && rows[i+1].start == c.col {
				continue
			}
			return i
		}
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].row == c.row {
			return i
		}
	}
	return 0
}

func (c *composer) visualColumn(r visualRow) int {
	return rw.StringWidth(string(c.lines[r.row][r.start:c.col]))
}

// prepare scrolls to the cursor and returns its screen position.
func (c *composer) prepare() (int, int) {
	width, height := c.textWidth(), c.Inner.Dy()
	if height < 1 {
		return c.Inner.Min.X, c.Inner.Min.Y
	}
	rows := c.visualRows(width)
	if len(rows) == 0 {
		return c.Inner.Min.X + 2, c.Inner.Min.Y
	}
	cur := c.currentVisual(rows)
	if cur < c.scroll {
		c.scroll = cur
	}
	if cur >= c.scroll+height {
		c.scroll = cur - height + 1
	}
	if c.scroll > max(0, len(rows)-height) {
		c.scroll = max(0, len(rows)-height)
	}
	if c.scroll < 0 {
		c.scroll = 0
	}
	x := c.Inner.Min.X + 2 + c.visualColumn(rows[cur])
	y := c.Inner.Min.Y + cur - c.scroll
	if x > c.Inner.Max.X {
		x = c.Inner.Max.X
	}
	if y >= c.Inner.Max.Y {
		y = c.Inner.Max.Y - 1
	}
	if y < c.Inner.Min.Y {
		y = c.Inner.Min.Y
	}
	return x, y
}

func (c *composer) Draw(buf *ui.Buffer) {
	c.Title = " " + c.theme.tr("Message") + " "
	c.Block.Draw(buf)
	if c.Inner.Dx() <= 0 || c.Inner.Dy() <= 0 {
		return
	}
	width, height := c.textWidth(), c.Inner.Dy()
	rows := c.visualRows(width)
	if c.Empty() {
		placeholder := c.theme.tr("Message Duo…  Enter sends · Ctrl+Enter newline")
		buf.SetString(fitText(placeholder, width), c.theme.Subtle, image.Pt(c.Inner.Min.X+2, c.Inner.Min.Y))
	}
	for i := 0; i < height; i++ {
		idx := c.scroll + i
		if idx >= len(rows) {
			break
		}
		r := rows[idx]
		y := c.Inner.Min.Y + i
		if i == 0 || r.start == 0 {
			buf.SetString("›", c.accent(), image.Pt(c.Inner.Min.X, y))
		}
		if r.row < len(c.spans) && len(c.spans[r.row]) > 0 {
			c.drawRowWithSpans(buf, r, y)
		} else {
			text := string(c.lines[r.row][r.start:r.end])
			buf.SetString(text, c.theme.Body, image.Pt(c.Inner.Min.X+2, y))
		}
	}
}

func (c *composer) drawRowWithSpans(buf *ui.Buffer, r visualRow, y int) {
	drawX := c.Inner.Min.X + 2
	line := c.lines[r.row]
	spans := c.spans[r.row]

	curr := r.start
	for curr < r.end {
		var inSpan *pasteSpan
		for i := range spans {
			s := &spans[i]
			if curr >= s.start && curr < s.end {
				inSpan = s
				break
			}
		}

		if inSpan != nil {
			next := min(r.end, inSpan.end)
			seg := string(line[curr:next])
			buf.SetString(seg, c.pasteBlockStyle(), image.Pt(drawX, y))
			drawX += rw.StringWidth(seg)
			curr = next
		} else {
			next := r.end
			for i := range spans {
				s := &spans[i]
				if s.start > curr && s.start < next {
					next = s.start
				}
			}
			seg := string(line[curr:next])
			buf.SetString(seg, c.theme.Body, image.Pt(drawX, y))
			drawX += rw.StringWidth(seg)
			curr = next
		}
	}
}

func (c *composer) pasteBlockStyle() ui.Style {
	return ui.Style{
		Fg:       c.theme.Accent,
		Bg:       c.theme.Panel,
		Modifier: ui.ModifierReverse | ui.ModifierBold,
	}
}

// Acco is a small alias so the Draw path cannot accidentally use a different
// accent than the rest of the composer.
func (c *composer) accent() ui.Style { return ui.Style{Fg: c.theme.Accent, Bg: c.theme.Panel} }
