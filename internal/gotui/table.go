package gotui

import (
	"strings"

	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui/v5"
)

type tableAlign int

const (
	tableLeft tableAlign = iota
	tableCenter
	tableRight
)

// tableRuns lays out a GFM table the parser already recognised. Cells arrive as
// styled runs, so a bold header, inline code and links survive the layout. The
// columns are sized here because only the renderer knows the transcript width,
// and each cell is wrapped to its column before the border is drawn, so the
// ordinary line wrapper can never break a border or destroy the alignment.
func tableRuns(header [][]textRun, rows [][][]textRun, aligns []tableAlign, width int, base ui.Style, t theme) []textRun {
	columns := len(header)
	for _, row := range rows {
		columns = max(columns, len(row))
	}
	if columns == 0 {
		return nil
	}
	header = padTable(header, columns)
	for i := range rows {
		rows[i] = padTable(rows[i], columns)
	}
	for len(aligns) < columns {
		aligns = append(aligns, tableLeft)
	}
	aligns = aligns[:columns]

	borderStyle := ui.Style{Fg: t.Faint, Bg: base.Bg}
	widths, ok := tableWidths(header, rows, columns, width)
	if !ok {
		return tablePlain(header, rows, base, borderStyle)
	}

	var out []textRun
	line := func(runs []textRun) {
		out = append(out, runs...)
		if len(runs) == 0 || runs[len(runs)-1].text != "\n" {
			out = append(out, textRun{"\n", base, linkTarget{}})
		}
	}
	line(tableBorder(widths, "+", "+", "+", borderStyle))
	line(tableRow(header, aligns, widths, base, borderStyle))
	line(tableBorder(widths, "+", "+", "+", borderStyle))
	for _, row := range rows {
		line(tableRow(row, aligns, widths, base, borderStyle))
	}
	// The header separator already closes a table that has no body rows.
	if len(rows) > 0 {
		line(tableBorder(widths, "+", "+", "+", borderStyle))
	}
	return out
}

// tablePlain is the fallback for a table too narrow for borders: the cells keep
// their separators so no content is lost and the line wrapper can reflow them.
func tablePlain(header [][]textRun, rows [][][]textRun, base ui.Style, borderStyle ui.Style) []textRun {
	var out []textRun
	emit := func(cells [][]textRun) {
		for i, cell := range cells {
			if i > 0 {
				out = append(out, textRun{" | ", borderStyle, linkTarget{}})
			}
			out = append(out, cell...)
		}
		out = append(out, textRun{"\n", base, linkTarget{}})
	}
	emit(header)
	for _, row := range rows {
		emit(row)
	}
	return out
}

func padTable(row [][]textRun, columns int) [][]textRun {
	for len(row) < columns {
		row = append(row, nil)
	}
	return row
}

func tableWidths(header [][]textRun, rows [][][]textRun, columns, width int) ([]int, bool) {
	overhead := 3*columns + 1
	if width < overhead+columns {
		return nil, false
	}
	budget := width - overhead
	natural := make([]int, columns)
	for i, cell := range header {
		natural[i] = max(natural[i], runsWidth(cell))
	}
	for _, row := range rows {
		for i, cell := range row {
			natural[i] = max(natural[i], runsWidth(cell))
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
	return []textRun{{b.String(), style, linkTarget{}}}
}

func tableRow(cells [][]textRun, aligns []tableAlign, widths []int, base ui.Style, borderStyle ui.Style) []textRun {
	cellLines := make([][]renderLine, len(widths))
	height := 1
	for i, width := range widths {
		wrapped := wrapRuns(cells[i], width)
		cellLines[i] = wrapped
		height = max(height, len(wrapped))
	}

	var out []textRun
	for line := 0; line < height; line++ {
		out = append(out, textRun{"|", borderStyle, linkTarget{}})
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
			out = append(out, textRun{" ", base, linkTarget{}})
			if left > 0 {
				out = append(out, textRun{strings.Repeat(" ", left), base, linkTarget{}})
			}
			out = append(out, runs...)
			if right > 0 {
				out = append(out, textRun{strings.Repeat(" ", right), base, linkTarget{}})
			}
			out = append(out, textRun{" ", base, linkTarget{}}, textRun{"|", borderStyle, linkTarget{}})
		}
		out = append(out, textRun{"\n", base, linkTarget{}})
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
