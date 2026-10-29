package gotui

import (
	"image"
	"strings"
	"testing"

	ui "github.com/metaspartan/gotui/v5"
)

func TestTimelineAgentAlignment(t *testing.T) {
	v := newTranscript(newTheme())
	v.conversation = true
	for _, pane := range []string{"Austin", "Tony", "Duo"} {
		block := v.renderEntry(entry{pane: pane, text: "Short line\n" + strings.Repeat("word ", 40)}, 100)
		for _, line := range block.lines {
			if pane == "Tony" {
				if line.header {
					if line.width() != 98 {
						t.Fatalf("Tony header not right aligned: %q", line.text())
					}
				} else if indent := len(line.text()) - len(strings.TrimLeft(line.text(), " ")); indent != 22 || line.width() > 98 {
					t.Fatalf("Tony body not left aligned within right 80%%: %q", line.text())
				}
			} else if pane == "Austin" && line.width() > 80 {
				t.Fatalf("Austin width = %d", line.width())
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
		// Body text starts at a fixed inset and remains clear of the gutter and track.
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
			for _, line := range block.lines {
				if line.header || !tc.multiline {
					if line.width() != 98 {
						t.Fatalf("expected right alignment: %q", line.text())
					}
				} else if indent := len(line.text()) - len(strings.TrimLeft(line.text(), " ")); indent != 22 {
					t.Fatalf("expected fixed left inset: %q", line.text())
				}
			}
		})
	}
}
