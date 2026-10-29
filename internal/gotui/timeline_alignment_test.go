package gotui

import (
	ui "github.com/metaspartan/gotui/v5"
	"image"
	"strings"
	"testing"
)

func TestTimelineAgentAlignment(t *testing.T) {
	v := newTranscript(newTheme())
	v.conversation = true
	for _, pane := range []string{"Austin", "Tony", "Duo"} {
		block := v.renderEntry(entry{pane: pane, text: strings.Repeat("word ", 40)}, 100)
		for _, line := range block.lines {
			if pane == "Tony" {
				if line.width() != 98 || !strings.HasPrefix(line.text(), "                    ") {
					t.Fatalf("Tony line not aligned: %q", line.text())
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
		// Every visible body ends with its final letter before the gutter and track.
		line := v.lines[v.offset+y-v.Inner.Min.Y]
		if strings.HasSuffix(line.text(), "Tony connected") {
			cell := buf.GetCell(image.Pt(v.Inner.Max.X-3, y))
			if cell.Rune != 'd' {
				t.Fatalf("last letter overwritten: %q", cell.Rune)
			}
		}
	}
}
