package gotui

import (
	"strings"

	"github.com/gdamore/tcell/v3"
	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui/v5"
)

type tableAlign int

const (
	tableLeft tableAlign = iota
	tableCenter
	tableRight
)

// parseTable recognizes a Markdown table beginning at lines[start]. It returns
// the rendered runs and the line after the table. The renderer wraps cells to
// the transcript width before emitting them, so the normal word wrapper cannot
// break a border or destroy column alignment afterwards.
func parseTable(lines []string, start, width int, base ui.Style, t theme) ([]textRun, int, bool) {
	if width < 7 || start+1 >= len(lines) {
		return nil, start, false
	}
	headerLine := strings.TrimSpace(lines[start])
	if !strings.Contains(headerLine, "|") {
		return nil, start, false
	}
	header := splitTableRow(headerLine)
	aligns, ok := tableAlignments(lines[start+1])
	if !ok || len(header) == 0 {
		return nil, start, false
	}

	rows := [][]string{}
	end := start + 2
	for end < len(lines) {
		line := strings.TrimSpace(lines[end])
		if line == "" || !strings.Contains(line, "|") || strings.HasPrefix(line, "```") {
			break
		}
		rows = append(rows, splitTableRow(line))
		end++
	}

	columns := max(len(header), len(aligns))
	for _, row := range rows {
		columns = max(columns, len(row))
	}
	if columns == 0 {
		return nil, start, false
	}
	header = padTable(header, columns)
	for i := range rows {
		rows[i] = padTable(rows[i], columns)
	}
	for len(aligns) < columns {
		aligns = append(aligns, tableLeft)
	}
	aligns = aligns[:columns]

	widths, ok := tableWidths(header, rows, columns, width, base, t)
	if !ok {
		return nil, start, false
	}

	borderStyle := ui.Style{Fg: t.Faint, Bg: base.Bg}
	var out []textRun
	line := func(runs []textRun) {
		out = append(out, runs...)
		if len(runs) == 0 || runs[len(runs)-1].text != "\n" {
			out = append(out, textRun{"\n", base})
		}
	}
	line(tableBorder(widths, "+", "+", "+", borderStyle))
	line(tableRow(header, aligns, widths, base, t, borderStyle, true))
	line(tableBorder(widths, "+", "+", "+", borderStyle))
	for _, row := range rows {
		line(tableRow(row, aligns, widths, base, t, borderStyle, false))
	}
	line(tableBorder(widths, "+", "+", "+", borderStyle))
	return out, end, true
}

func splitTableRow(line string) []string {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "|") {
		line = line[1:]
	}
	if strings.HasSuffix(line, "|") && !strings.HasSuffix(line, `\|`) {
		line = line[:len(line)-1]
	}
	var cells []string
	var cell strings.Builder
	for i := 0; i < len(line); i++ {
		if line[i] == '\\' && i+1 < len(line) && line[i+1] == '|' {
			cell.WriteByte('|')
			i++
			continue
		}
		if line[i] == '|' {
			cells = append(cells, strings.TrimSpace(cell.String()))
			cell.Reset()
			continue
		}
		cell.WriteByte(line[i])
	}
	cells = append(cells, strings.TrimSpace(cell.String()))
	return cells
}

func tableAlignments(line string) ([]tableAlign, bool) {
	cells := splitTableRow(line)
	if len(cells) == 0 {
		return nil, false
	}
	aligns := make([]tableAlign, len(cells))
	for i, cell := range cells {
		cell = strings.TrimSpace(cell)
		left := strings.HasPrefix(cell, ":")
		right := strings.HasSuffix(cell, ":")
		core := strings.Trim(cell, ":")
		if len(core) < 3 || strings.Trim(core, "-") != "" {
			return nil, false
		}
		switch {
		case left && right:
			aligns[i] = tableCenter
		case right:
			aligns[i] = tableRight
		default:
			aligns[i] = tableLeft
		}
	}
	return aligns, true
}

func padTable(row []string, columns int) []string {
	for len(row) < columns {
		row = append(row, "")
	}
	return row
}

func tableWidths(header []string, rows [][]string, columns, width int, base ui.Style, t theme) ([]int, bool) {
	overhead := 3*columns + 1
	if width < overhead+columns {
		return nil, false
	}
	budget := width - overhead
	natural := make([]int, columns)
	measure := func(cell string) int {
		return runsWidth(inlineRuns(cell, base, t))
	}
	for i, cell := range header {
		natural[i] = max(natural[i], measure(cell))
	}
	for _, row := range rows {
		for i, cell := range row {
			natural[i] = max(natural[i], measure(cell))
		}
	}
	for i := range natural {
		natural[i] = max(1, natural[i])
	}

	widths := make([]int, columns)
	total := 0
	for _, w := range natural {
		total += w
	}
	if total <= budget {
		copy(widths, natural)
		return widths, true
	}

	minWidth := 1
	for _, candidate := range []int{6, 4, 2} {
		if candidate*columns <= budget {
			minWidth = candidate
			break
		}
	}
	used := 0
	for i := range widths {
		widths[i] = min(natural[i], minWidth)
		if widths[i] < 1 {
			widths[i] = 1
		}
		used += widths[i]
	}
	remaining := budget - used
	for remaining > 0 {
		best := -1
		bestDeficit := 0
		for i := range widths {
			deficit := natural[i] - widths[i]
			if deficit > bestDeficit {
				best, bestDeficit = i, deficit
			}
		}
		if best < 0 {
			break
		}
		widths[best]++
		remaining--
	}
	return widths, true
}

func tableBorder(widths []int, left, middle, right string, style ui.Style) []textRun {
	var b strings.Builder
	b.WriteString(left)
	for i, width := range widths {
		b.WriteString(strings.Repeat("-", width+2))
		if i < len(widths)-1 {
			b.WriteString(middle)
		}
	}
	b.WriteString(right)
	return []textRun{{b.String(), style}}
}

func tableRow(cells []string, aligns []tableAlign, widths []int, base ui.Style, t theme, borderStyle ui.Style, header bool) []textRun {
	style := base
	if header {
		style.Modifier |= tcell.AttrBold
	}
	cellLines := make([][]renderLine, len(widths))
	height := 1
	for i, width := range widths {
		wrapped := wrapRuns(inlineRuns(cells[i], style, t), width)
		cellLines[i] = wrapped
		height = max(height, len(wrapped))
	}

	var out []textRun
	for line := 0; line < height; line++ {
		out = append(out, textRun{"|", borderStyle})
		for i, width := range widths {
			var runs []textRun
			if line < len(cellLines[i]) {
				runs = cellLines[i][line].runs
			}
			pad := max(0, width-runsWidth(runs))
			left, right := 0, pad
			switch aligns[i] {
			case tableCenter:
				left = pad / 2
				right = pad - left
			case tableRight:
				left = pad
				right = 0
			}
			out = append(out, textRun{" ", base})
			if left > 0 {
				out = append(out, textRun{strings.Repeat(" ", left), base})
			}
			out = append(out, runs...)
			if right > 0 {
				out = append(out, textRun{strings.Repeat(" ", right), base})
			}
			out = append(out, textRun{" ", base}, textRun{"|", borderStyle})
		}
		out = append(out, textRun{"\n", base})
	}
	return out
}

func runsWidth(runs []textRun) int {
	width := 0
	for _, run := range runs {
		width += rw.StringWidth(run.text)
	}
	return width
}
