package gotui

import (
	"strings"
	"testing"
)

func TestWrapRunsPreservesMarkdownHardBreaks(t *testing.T) {
	tm := newTheme()
	runs := markdownRuns("# Heading\n\n- first item\n- second item\n\n```\ncode line\n```\n\nparagraph text", tm.Body, tm)
	lines := wrapRuns(runs, 24)

	var texts []string
	for _, line := range lines {
		texts = append(texts, strings.TrimSpace(line.text()))
	}
	joined := strings.Join(texts, "\n")
	for _, want := range []string{"Heading", "• first item", "• second item", "code line", "paragraph text"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("wrapped markdown missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "Heading •") ||
		strings.Contains(joined, "item • second") ||
		strings.Contains(joined, "line paragraph") {
		t.Fatalf("hard line breaks were flattened:\n%s", joined)
	}
}

func TestWrapRunsWrapsLongTextWithinWidth(t *testing.T) {
	tm := newTheme()
	lines := wrapText("one two three four five", tm.Body, 9)
	if len(lines) < 2 {
		t.Fatalf("expected wrapped lines, got %d: %#v", len(lines), lines)
	}
	for _, line := range lines {
		if line.width() > 9 {
			t.Fatalf("line width %d exceeds 9: %q", line.width(), line.text())
		}
	}
}

func TestWrapRunsDropsSpaceAtExactLineBoundary(t *testing.T) {
	tm := newTheme()
	lines := wrapText("12345678 next", tm.Body, 8)
	if len(lines) != 2 || lines[0].text() != "12345678" || lines[1].text() != "next" {
		t.Fatalf("unexpected wrap: %#v", lines)
	}
}

func TestWrapRunsWrapsWideRunesAtCellBoundaries(t *testing.T) {
	tm := newTheme()
	lines := wrapText("你好世界你好世界", tm.Body, 4)
	if len(lines) < 3 {
		t.Fatalf("expected multiple lines, got %d", len(lines))
	}
	for _, line := range lines {
		if line.width() > 4 {
			t.Fatalf("CJK line width %d exceeds 4: %q", line.width(), line.text())
		}
	}
}
