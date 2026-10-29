package gotui

import (
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
				if line.width() != 100 || !strings.HasPrefix(line.text(), "                    ") {
					t.Fatalf("Tony line not aligned: %q", line.text())
				}
			} else if pane == "Austin" && line.width() > 80 {
				t.Fatalf("Austin width = %d", line.width())
			}
		}
	}
}
