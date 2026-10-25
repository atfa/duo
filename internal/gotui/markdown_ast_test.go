package gotui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v3"
)

// joinLines flattens markdownRuns output the way the transcript renders it:
// hard breaks as newlines, everything else concatenated.
func joinLines(t *testing.T, runs []textRun) string {
	t.Helper()
	var b strings.Builder
	for _, run := range runs {
		if run.text == "\n" {
			b.WriteString("\n")
			continue
		}
		if strings.Contains(run.text, "\x1b") {
			t.Fatalf("markdownRuns emitted raw ANSI: %q", run.text)
		}
		b.WriteString(run.text)
	}
	return b.String()
}

func TestMarkdownASTNestedListInsideQuote(t *testing.T) {
	tm := newTheme()
	runs := markdownRuns("> - outer\n>   - inner\n> - second", tm.Body, tm, 60)
	joined := joinLines(t, runs)
	for _, want := range []string{"│ • outer", "│   • inner", "│ • second"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("nested list inside quote missing %q:\n%s", want, joined)
		}
	}
}

func TestMarkdownASTOrderedAndTaskLists(t *testing.T) {
	tm := newTheme()
	runs := markdownRuns("1. first\n2. second\n\n- [ ] todo\n- [x] done", tm.Body, tm, 60)
	joined := joinLines(t, runs)
	if !strings.Contains(joined, "1. first") || !strings.Contains(joined, "2. second") {
		t.Fatalf("ordered list numbering lost:\n%s", joined)
	}
	if !strings.Contains(joined, "☐") || !strings.Contains(joined, "☑") {
		t.Fatalf("task list markers lost:\n%s", joined)
	}
}

func TestMarkdownASTTableAlignments(t *testing.T) {
	tm := newTheme()
	runs := markdownRuns("| left | mid | right |\n|:--|:-:|--:|\n| L | M | R |", tm.Body, tm, 40)
	lines := wrapRuns(runs, 40)
	var dataRow string
	for _, line := range lines {
		if text := line.text(); strings.Contains(text, "| L ") && strings.Contains(text, "R |") {
			dataRow = text
		}
	}
	if dataRow == "" {
		t.Fatalf("table data row missing:\n%v", lines)
	}
	// Wide headers make padding observable: left cell touches its left
	// border, center cell keeps symmetric padding, right cell touches the
	// right border.
	if want := "| L    |  M  |     R |"; dataRow != want {
		t.Fatalf("alignment row mismatch:\n got %q\nwant %q", dataRow, want)
	}
}

func TestMarkdownASTTableEscapedPipeAndInlineCode(t *testing.T) {
	tm := newTheme()
	runs := markdownRuns("| expr | out |\n|---|---|\n| `a\\|b` | \\| |", tm.Body, tm, 40)
	joined := joinLines(t, runs)
	if strings.Contains(joined, "\\|") {
		t.Fatalf("escaped pipe leaked into rendered table:\n%s", joined)
	}
	if !strings.Contains(joined, "a|b") {
		t.Fatalf("inline code pipe lost:\n%s", joined)
	}
}

func TestMarkdownASTTableWideCJKCellWraps(t *testing.T) {
	tm := newTheme()
	long := strings.Repeat("宽", 30)
	runs := markdownRuns("| k | v |\n|---|---|\n| x | "+long+" |", tm.Body, tm, 30)
	lines := wrapRuns(runs, 30)
	var cell strings.Builder
	for _, line := range lines {
		if line.width() > 30 {
			t.Fatalf("line exceeds width: %q", line.text())
		}
		// Second column of each table row, across wrapped lines. Border
		// rows hold no pipe and contribute nothing.
		if parts := strings.Split(line.text(), "|"); len(parts) >= 3 {
			cell.WriteString(strings.TrimSpace(parts[2]))
		}
	}
	// cells[0] is the header's second column ("v"); body rows follow it.
	cells := strings.Fields(cell.String())
	got := strings.Join(cells[min(1, len(cells)):], "")
	if got != long {
		t.Fatalf("wide CJK cell content lost or reordered:\n got %q\nwant %q", got, long)
	}
}

func TestMarkdownASTFenceTableNotParsed(t *testing.T) {
	tm := newTheme()
	runs := markdownRuns("```\n| a | b |\n|---|---|\n```", tm.Body, tm, 40)
	joined := joinLines(t, runs)
	if strings.Contains(joined, "+---+") {
		t.Fatalf("fenced table rendered as table:\n%s", joined)
	}
	if !strings.Contains(joined, "| a | b |") {
		t.Fatalf("fence content lost:\n%s", joined)
	}
}

func TestMarkdownASTIndentedCodeBlock(t *testing.T) {
	tm := newTheme()
	runs := markdownRuns("para\n\n    indented code\n    more", tm.Body, tm, 60)
	joined := joinLines(t, runs)
	code := false
	for _, run := range runs {
		if strings.Contains(run.text, "indented code") && run.style == tm.Code {
			code = true
		}
	}
	if !code || !strings.Contains(joined, "indented code") {
		t.Fatalf("indented code block lost or unstyled:\n%s\n%#v", joined, runs)
	}
}

func TestMarkdownASTStrikethroughAndEntities(t *testing.T) {
	tm := newTheme()
	runs := markdownRuns("~~gone~~ &amp; &#65;", tm.Body, tm, 60)
	joined := joinLines(t, runs)
	if strings.Contains(joined, "~~") {
		t.Fatalf("strikethrough markers left raw:\n%s", joined)
	}
	if strings.Contains(joined, "&amp;") || !strings.Contains(joined, "&") || !strings.Contains(joined, "A") {
		t.Fatalf("entity resolution wrong:\n%s", joined)
	}
}

func TestMarkdownASTAutoLink(t *testing.T) {
	tm := newTheme()
	runs := markdownRuns("see https://example.com/x", tm.Body, tm, 60)
	joined := joinLines(t, runs)
	if !strings.Contains(joined, "https://example.com/x") {
		t.Fatalf("autolink text lost:\n%s", joined)
	}
	for _, run := range runs {
		if strings.Contains(run.text, "example.com") && run.link.kind == linkURL {
			return
		}
	}
	t.Fatalf("autolink not clickable:\n%#v", runs)
}

func TestMarkdownASTHardLineBreak(t *testing.T) {
	tm := newTheme()
	runs := markdownRuns("line one  \nline two", tm.Body, tm, 80)
	var hard int
	for _, run := range runs {
		if run.text == "\n" {
			hard++
		}
	}
	if hard == 0 {
		t.Fatalf("hard line break not preserved: %#v", runs)
	}
}

func TestMarkdownASTEmphasisRuns(t *testing.T) {
	tm := newTheme()
	runs := markdownRuns("**bold** and *it* and `code`", tm.Body, tm, 60)
	var bold, italic, code bool
	boldMod := tcell.AttrBold
	italicMod := tcell.AttrItalic
	for _, run := range runs {
		switch run.text {
		case "bold":
			bold = run.style.Modifier&boldMod != 0
		case "it":
			italic = run.style.Modifier&italicMod != 0
		case "code":
			code = run.style == tm.Code
		}
	}
	if !bold || !italic || !code {
		t.Fatalf("inline styles lost (bold=%v italic=%v code=%v): %#v", bold, italic, code, runs)
	}
}

func TestMarkdownASTOutputWithinWidth(t *testing.T) {
	tm := newTheme()
	doc := "| a | b |\n|---|---|\n| hello | world table |\n\nplain paragraph text here"
	runs := markdownRuns(doc, tm.Body, tm, 30)
	for _, line := range wrapRuns(runs, 30) {
		if line.width() > 30 {
			t.Fatalf("line exceeds width: %q", line.text())
		}
	}
}

func TestMarkdownASTLinkKeepsLabelAndURL(t *testing.T) {
	tm := newTheme()
	runs := markdownRuns("[docs](https://example.com)", tm.Body, tm, 60)
	joined := joinLines(t, runs)
	if !strings.Contains(joined, "docs") || !strings.Contains(joined, "https://example.com") {
		t.Fatalf("link label/url lost:\n%s", joined)
	}
}

func TestMarkdownASTNoANSIAnyInput(t *testing.T) {
	tm := newTheme()
	inputs := []string{
		"# h\n\n| a |\n|---|\n| 1 |",
		"> quote\n\n- list\n\n```go\ncode\n```",
		"**b** *i* `c` [l](https://e.com) ~~s~~",
	}
	for _, in := range inputs {
		for _, run := range markdownRuns(in, tm.Body, tm, 40) {
			if strings.Contains(run.text, "\x1b") {
				t.Fatalf("ANSI in output for %q: %q", in, run.text)
			}
		}
	}
}
