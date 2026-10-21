package gotui

import (
	"strings"

	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui/v5"
)

type textRun struct {
	text  string
	style ui.Style
	// link is empty for ordinary text. A target survives wrapping because it
	// travels with the run, so a URL split over two rows remains clickable.
	link linkTarget
}

type renderLine struct {
	runs    []textRun
	entry   int
	header  bool
	summary bool
}

func makeLine(text string, style ui.Style, entry int) renderLine {
	return renderLine{runs: []textRun{{text: text, style: style}}, entry: entry}
}

func (l renderLine) text() string {
	var b strings.Builder
	for _, run := range l.runs {
		b.WriteString(run.text)
	}
	return b.String()
}

func (l renderLine) width() int {
	w := 0
	for _, run := range l.runs {
		w += rw.StringWidth(run.text)
	}
	return w
}

// fitText truncates s to width, accounting for double-width runes.
func fitText(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if rw.StringWidth(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return rw.Truncate(s, width, "…")
}

// wrapRuns wraps styled runs to width without losing style boundaries. It
// prefers whitespace breaks but can split an overlong word, because a session
// log contains at least as many commit hashes as prose.
func wrapRuns(runs []textRun, width int) []renderLine {
	if width < 1 {
		return nil
	}
	var out []renderLine
	cur := renderLine{}
	curWidth := 0
	flush := func(hard bool) {
		if len(cur.runs) > 0 || hard {
			out = append(out, cur)
		}
		cur = renderLine{}
		curWidth = 0
	}
	addWord := func(word []textRun, wordWidth int) {
		// A trailing space that would wrap on its own is a break, not a
		// one-cell line that leaves the next word indented.
		spaces := true
		for _, run := range word {
			if strings.TrimSpace(run.text) != "" {
				spaces = false
				break
			}
		}
		if spaces && curWidth > 0 && curWidth+wordWidth > width {
			return
		}
		if curWidth > 0 && curWidth+wordWidth > width {
			flush(false)
		}
		if wordWidth > width {
			// Split a long word at cell boundaries.
			for _, run := range word {
				for _, r := range run.text {
					w := rw.RuneWidth(r)
					if w < 1 {
						w = 0
					}
					if curWidth > 0 && curWidth+w > width {
						flush(false)
					}
					if w == 0 && len(cur.runs) > 0 {
						cur.runs[len(cur.runs)-1].text += string(r)
						continue
					}
					if len(cur.runs) > 0 && cur.runs[len(cur.runs)-1].style == run.style && cur.runs[len(cur.runs)-1].link == run.link {
						cur.runs[len(cur.runs)-1].text += string(r)
					} else {
						cur.runs = append(cur.runs, textRun{string(r), run.style, run.link})
					}
					curWidth += w
				}
			}
			return
		}
		for _, run := range word {
			if run.text == "" {
				continue
			}
			if len(cur.runs) > 0 && cur.runs[len(cur.runs)-1].style == run.style && cur.runs[len(cur.runs)-1].link == run.link {
				cur.runs[len(cur.runs)-1].text += run.text
			} else {
				cur.runs = append(cur.runs, run)
			}
		}
		curWidth += wordWidth
	}

	for _, run := range runs {
		text := strings.ReplaceAll(run.text, "\t", "    ")
		// A newline is a hard break, not a space: Markdown headings, list
		// items, quotes and code lines must not be run together while wrapping.
		for i, segment := range strings.Split(text, "\n") {
			if i > 0 {
				flush(true)
			}
			parts := strings.SplitAfter(segment, " ")
			for _, part := range parts {
				if part == "" {
					continue
				}
				w := rw.StringWidth(part)
				if w > width {
					addWord([]textRun{{strings.TrimRight(part, " "), run.style, run.link}}, rw.StringWidth(strings.TrimRight(part, " ")))
					if strings.HasSuffix(part, " ") {
						addWord([]textRun{{" ", run.style, run.link}}, 1)
					}
					continue
				}
				addWord([]textRun{{part, run.style, run.link}}, w)
			}
		}
	}
	flush(false)
	if len(out) == 0 {
		out = append(out, renderLine{})
	}
	return out
}

func wrapText(text string, style ui.Style, width int) []renderLine {
	return wrapRuns([]textRun{{text, style, linkTarget{}}}, width)
}

// splitCellsAt derives the text starting at column start and ending before
// column end. It is used by mouse selection, which is column-aware even though
// the transcript is stored as styled lines.
func lineSlice(line renderLine, start, end int) string {
	if start < 0 {
		start = 0
	}
	var b strings.Builder
	col := 0
	for _, run := range line.runs {
		for _, r := range run.text {
			w := rw.RuneWidth(r)
			if w == 0 {
				w = 1
			}
			if col >= start && col < end {
				b.WriteRune(r)
			}
			col += w
			if col >= end {
				return b.String()
			}
		}
	}
	return b.String()
}
