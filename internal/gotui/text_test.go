package gotui

import (
	"strings"
	"testing"
)

func TestWrapRunsPreservesMarkdownHardBreaks(t *testing.T) {
	tm := newTheme()
	runs := markdownRuns("# Heading\n\n- first item\n- second item\n\n```\ncode line\n```\n\nparagraph text", tm.Body, tm, 80)
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

func TestMarkdownTableRendersAlignedBorders(t *testing.T) {
	tm := newTheme()
	runs := markdownRuns("| 检查项 | 结果 |\n|---|---|\n| 全文汉字 | 7,723 |", tm.Body, tm, 40)
	lines := wrapRuns(runs, 40)
	var joined string
	for _, line := range lines {
		joined += line.text() + "\n"
		if line.width() > 40 {
			t.Fatalf("table line width %d exceeds 40: %q", line.width(), line.text())
		}
		if strings.TrimSpace(line.text()) == "" {
			t.Fatalf("rendered table contains a blank line:\n%s", joined)
		}
	}
	for _, want := range []string{"+", "| 检查项", "| 全文汉字", "| 7,723"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rendered table missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "| 检查项 |") {
		t.Fatalf("table pipes were left unrendered:\n%s", joined)
	}
}

func TestMarkdownTableWrapsWideCellsWithoutTruncating(t *testing.T) {
	tm := newTheme()
	long := strings.Repeat("很长的内容 ", 8) + "ENDMARKER"
	runs := markdownRuns("| 项目 | 说明 |\n|---|---|\n| X | "+long+" |", tm.Body, tm, 32)
	lines := wrapRuns(runs, 32)
	var joined string
	for _, line := range lines {
		joined += line.text()
		if line.width() > 32 {
			t.Fatalf("wrapped table line width %d exceeds 32: %q", line.width(), line.text())
		}
	}
	if !strings.Contains(strings.ReplaceAll(joined, " ", ""), "ENDMARKER") {
		t.Fatalf("wide table cell was truncated instead of wrapped:\n%s", joined)
	}
}

func TestMarkdownTableInsideCodeBlockIsNotRendered(t *testing.T) {
	tm := newTheme()
	runs := markdownRuns("```\n| a | b |\n|---|---|\n```", tm.Body, tm, 40)
	var joined string
	for _, line := range wrapRuns(runs, 40) {
		joined += line.text() + "\n"
	}
	if strings.Contains(joined, "---") && strings.Contains(joined, "+") {
		t.Fatalf("a fenced code block was rendered as a table:\n%s", joined)
	}
	if !strings.Contains(joined, "| a | b |") {
		t.Fatalf("fenced table text was lost:\n%s", joined)
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
