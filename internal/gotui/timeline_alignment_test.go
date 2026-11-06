package gotui

import (
	"image"
	"strings"
	"testing"

	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui/v5"
)

// tonyInset is the shared left inset of one Tony message: the block is anchored
// to the right of the pane, so the inset is whatever the widest of its own lines
// leaves free. It is read from the padding run, not from the rendered text,
// because a body line starts with a two-cell gutter of its own.
func tonyInset(block entryLines) (int, bool) {
	if len(block.lines) == 0 {
		return 0, false
	}
	pad := block.lines[0].runs[0].text
	if strings.Trim(pad, " ") != "" {
		return 0, false
	}
	for _, line := range block.lines {
		if line.runs[0].text != pad {
			return 0, false
		}
	}
	return rw.StringWidth(pad), true
}

// requireTonyBlock fails unless every line of a message is inset by the same
// padding and the block's widest line reaches the right edge. A message from Tony
// mirrors one from Austin: header and body share the left edge, the block hugs
// the right.
func requireTonyBlock(t *testing.T, block entryLines, fullWidth int) {
	t.Helper()
	inset, ok := tonyInset(block)
	if !ok {
		t.Fatalf("lines of one message must share one inset: %v", block.lines)
	}
	// line.width() already counts the shared padding, so the block's own content
	// width is what remains after it.
	widest := 0
	for _, line := range block.lines {
		widest = max(widest, line.width()-inset)
	}
	if inset+widest != fullWidth {
		t.Fatalf("block ends at column %d, want the right edge %d (inset %d, widest %d)", inset+widest, fullWidth, inset, widest)
	}
}

func TestTimelineAgentAlignment(t *testing.T) {
	v := newTranscript(newTheme())
	v.conversation = true
	for _, pane := range []string{"Austin", "Tony", "Duo"} {
		block := v.renderEntry(entry{pane: pane, text: "Short line\n" + strings.Repeat("word ", 40)}, 100)
		if pane == "Tony" {
			requireTonyBlock(t, block, 98)
			continue
		}
		for _, line := range block.lines {
			// Austin's messages take 80% of the pane, Duo's and the human's the
			// whole width; neither is inset, so both stay flush left.
			if pane == "Austin" && line.width() > 80 {
				t.Fatalf("%s width = %d, want it to stay left aligned within the pane", pane, line.width())
			}
			// Only a body line carries the two-cell gutter; nothing else is inset.
			if text := line.text(); len(text)-len(strings.TrimLeft(text, " ")) > 2 {
				t.Fatalf("%s line is inset: %q", pane, text)
			}
		}
	}
}

func TestTimelineTonyClearsScrollbar(t *testing.T) {
	v := newTranscript(newTheme())
	v.conversation = true
	v.SetRect(0, 0, 60, 8)
	v.SetEntries([]entry{{pane: "Tony", text: strings.Repeat("Tony connected\n", 20)}}, true)
	buf := ui.NewBuffer(v.GetRect())
	v.Draw(buf)
	for y := v.Inner.Min.Y; y < v.Inner.Max.Y; y++ {
		// Body text starts at the block's inset and stays clear of the gutter and track.
		line := v.lines[v.offset+y-v.Inner.Min.Y]
		if strings.HasSuffix(line.text(), "Tony connected") {
			cell := buf.GetCell(image.Pt(v.Inner.Min.X+line.width()-1, y))
			if cell.Rune != 'd' {
				t.Fatalf("last letter overwritten: %q", cell.Rune)
			}
		}
	}
}

func TestTimelineTonyBodyAlignmentByRenderedLines(t *testing.T) {
	v := newTranscript(newTheme())
	v.conversation = true
	for _, tc := range []struct {
		name, text string
		multiline  bool
	}{
		{"short", "Tony connected", false},
		{"wrapped", strings.Repeat("word ", 30), true},
		{"paragraphs", "first\n\nsecond", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			block := v.renderEntry(entry{pane: "Tony", text: tc.text}, 100)
			requireTonyBlock(t, block, 98)
			if len(block.lines) < 2 && tc.multiline {
				t.Fatalf("%q rendered %d lines, want a wrapped body", tc.text, len(block.lines))
			}
		})
	}
}
