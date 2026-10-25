package gotui

import (
	"image"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v3"
	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui/v5"
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

// TestMarkdownInlineStylesComeFromTheParser pins the emphasis the AST parser
// reports: the renderer must style runs instead of printing the delimiters.
func TestMarkdownInlineStylesComeFromTheParser(t *testing.T) {
	tm := newTheme()
	runs := markdownRuns("**bold** *it* `code` ~~gone~~", tm.Body, tm, 40)
	styles := map[string]ui.Style{}
	for _, run := range runs {
		if strings.TrimSpace(run.text) != "" {
			styles[run.text] = run.style
		}
	}
	if styles["bold"].Modifier&tcell.AttrBold == 0 {
		t.Fatalf("bold run lost AttrBold: %#v", styles["bold"])
	}
	if styles["it"].Modifier&tcell.AttrItalic == 0 {
		t.Fatalf("italic run lost AttrItalic: %#v", styles["it"])
	}
	if styles["gone"].Modifier&tcell.AttrStrikeThrough == 0 {
		t.Fatalf("strikethrough run lost AttrStrikeThrough: %#v", styles["gone"])
	}
	if styles["code"] != tm.Code {
		t.Fatalf("code span style = %#v, want the code style %#v", styles["code"], tm.Code)
	}
	for text := range styles {
		if strings.ContainsAny(text, "*`~") {
			t.Fatalf("markdown delimiters were left in the output: %q", text)
		}
	}
}

// TestMarkdownTableKeepsEscapedPipe checks the parser, not a column splitter, owns
// cell boundaries: `\|` is one cell with a literal pipe.
func TestMarkdownTableKeepsEscapedPipe(t *testing.T) {
	tm := newTheme()
	runs := markdownRuns("| a \\| b | c |\n|---|---|\n| 1 | 2 |", tm.Body, tm, 40)
	var joined string
	for _, line := range wrapRuns(runs, 40) {
		joined += line.text() + "\n"
	}
	if !strings.Contains(joined, "a | b") {
		t.Fatalf("escaped pipe was not rendered literally:\n%s", joined)
	}
	if !strings.Contains(joined, "| a | b | c |") {
		t.Fatalf("escaped pipe split the cell into three columns:\n%s", joined)
	}
}

// TestMarkdownTableAlignsColumns covers GFM alignment markers end to end.
func TestMarkdownTableAlignsColumns(t *testing.T) {
	tm := newTheme()
	runs := markdownRuns("| left | mid | right |\n|:--|:-:|--:|\n| 1 | 2 | 3 |", tm.Body, tm, 40)
	lines := wrapRuns(runs, 40)
	if len(lines) < 5 {
		t.Fatalf("table rendered %d lines, want header, borders and one row", len(lines))
	}
	row := lines[3].text()
	if row != "| 1    |  2  |     3 |" {
		t.Fatalf("row alignment = %q, want %q", row, "| 1    |  2  |     3 |")
	}
}

// TestMarkdownNestedListIndentsDeeper keeps a nested item visually subordinate.
func TestMarkdownNestedListIndentsDeeper(t *testing.T) {
	tm := newTheme()
	runs := markdownRuns("- outer\n  - inner", tm.Body, tm, 40)
	var texts []string
	for _, line := range wrapRuns(runs, 40) {
		texts = append(texts, line.text())
	}
	if len(texts) != 2 {
		t.Fatalf("nested list rendered %d lines: %q", len(texts), texts)
	}
	if !strings.HasPrefix(texts[0], "• outer") || !strings.HasPrefix(texts[1], "  • inner") {
		t.Fatalf("nested item was not indented: %q", texts)
	}
}

// TestMarkdownTaskListUsesCheckbox covers the GFM task list extension.
func TestMarkdownTaskListUsesCheckbox(t *testing.T) {
	tm := newTheme()
	runs := markdownRuns("- [x] done\n- [ ] todo", tm.Body, tm, 40)
	var joined string
	for _, line := range wrapRuns(runs, 40) {
		joined += line.text() + "\n"
	}
	for _, want := range []string{"☑ done", "☐ todo"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("task list missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "[x]") || strings.Contains(joined, "[ ]") {
		t.Fatalf("task list syntax was left unrendered:\n%s", joined)
	}
}

// TestMarkdownBlockquoteKeepsGutterOnEveryLine checks the quote gutter survives
// blank lines and nested blocks instead of ending at the first paragraph.
func TestMarkdownBlockquoteKeepsGutterOnEveryLine(t *testing.T) {
	tm := newTheme()
	runs := markdownRuns("> first\n>\n> second\n> - item", tm.Body, tm, 40)
	var texts []string
	for _, line := range wrapRuns(runs, 40) {
		texts = append(texts, line.text())
	}
	for _, text := range texts {
		if !strings.HasPrefix(text, "│") {
			t.Fatalf("quote line without a gutter: %q in %q", text, texts)
		}
	}
	joined := strings.Join(texts, "\n")
	for _, want := range []string{"first", "second", "• item"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("quote content missing %q:\n%s", want, joined)
		}
	}
}

// TestMarkdownSoftBreakReflowsAsSpace pins CommonMark's soft break: a wrapped
// source paragraph is one logical line for the transcript wrapper.
func TestMarkdownSoftBreakReflowsAsSpace(t *testing.T) {
	tm := newTheme()
	runs := markdownRuns("one two\nthree four", tm.Body, tm, 40)
	lines := wrapRuns(runs, 40)
	if len(lines) != 1 || lines[0].text() != "one two three four" {
		t.Fatalf("soft break did not reflow: %q", renderLineTexts(lines))
	}
}

// The host locale must not decide how many cells a glyph takes. gotui's buffer
// and this renderer both read go-runewidth's global, and on a zh_CN host that
// global made the bullet, the quote gutter, the rule and the ellipsis two cells
// wide while the terminal paints them in one — every decorated line then carried
// a blank cell and shifted the text after it.
func TestWidthTableIgnoresTheHostLocale(t *testing.T) {
	if rw.EastAsianWidth {
		t.Fatal("go-runewidth still follows the host locale; bullets and rules will leave a gap")
	}
	for _, glyph := range []string{"•", "│", "─", "…", "▸", "▾", "|", "-", "+"} {
		if got := rw.StringWidth(glyph); got != 1 {
			t.Fatalf("%q measures %d cells, want the one cell a terminal paints it in", glyph, got)
		}
	}
	for _, glyph := range []string{"中", "文", "宽"} {
		if got := rw.StringWidth(glyph); got != 2 {
			t.Fatalf("%q measures %d cells, want the two a terminal paints it in", glyph, got)
		}
	}
}

// Drawing the decorated glyphs must leave no blank cell between them: the old
// width table advanced two cells for each of them and the terminal painted one,
// so a bullet or a rule was followed by an empty cell and everything after it
// shifted right.
func TestDecoratedGlyphsLeaveNoGapWhenDrawn(t *testing.T) {
	tm := newTheme()
	text := "•│─…▸▾"
	buf := ui.NewBuffer(image.Rect(0, 0, 16, 1))
	buf.SetString(text, tm.Body, image.Pt(0, 0))

	glyphs := []rune(text)
	if got := rw.StringWidth(text); got != len(glyphs) {
		t.Fatalf("%q measures %d cells, want one per glyph (%d)", text, got, len(glyphs))
	}
	for x := range glyphs {
		if got := buf.GetCell(image.Pt(x, 0)).Rune; got != glyphs[x] {
			t.Fatalf("cell %d holds %q, want %q: the drawing and the measurement disagree on %q", x, string(got), string(glyphs[x]), text)
		}
	}
}

// The table in the report a session produced: one long column must not starve the
// others. The greedy pass this pins against dropped every column to a six-cell
// floor and then handed the rest to the widest one.
func TestTableColumnsShareTheWidthInsteadOfStarving(t *testing.T) {
	rows := []string{
		"| 区块 | 标题 | 主要要点 |",
		"|---|---|---|",
		"| Hero | Think Twice. Build Better. | 两个平级 agent、一个经过验证的结果；v0.14.2、安装命令 |",
		"| Why Duo | An agent reviewing itself is still one perspective. | 结构性独立审查：两个 session、两个 worktree、同一个 commit，避免自审自批 |",
		"| How it lands | Four checkpoints between an idea and your branch. | 四个由 Git 证据支撑的检查点；证据不匹配即关卡关闭 |",
		"| FAQ | The questions engineers ask first. | 与 subagent 的区别、是否需两套模型、冲突、验证失败、历史/交付、代码是否离开本机、MIT 开源 |",
	}
	tm := newTheme()
	const width = 96
	lines := wrapRuns(markdownRuns(strings.Join(rows, "\n"), tm.Body, tm, width), width)

	var border string
	for _, line := range lines {
		if line.width() > width {
			t.Fatalf("table line is %d cells wide, want at most %d: %q", line.width(), width, line.text())
		}
		if strings.HasPrefix(line.text(), "+") && border == "" {
			border = line.text()
		}
	}
	if border == "" {
		t.Fatalf("no table border was rendered:\n%s", renderLineTexts(lines))
	}

	// Column widths are the runs between the border's '+' joints.
	parts := strings.Split(strings.Trim(border, "+"), "+")
	if len(parts) != 3 {
		t.Fatalf("border %q does not describe three columns", border)
	}
	widths := make([]int, len(parts))
	for i, part := range parts {
		widths[i] = rw.StringWidth(part) - 2
	}
	// The shortest column keeps the width its own content needs; the two long
	// ones split what is left, so they may differ by at most one cell.
	if widths[0] != 12 {
		t.Fatalf("first column width = %d, want its natural 12: %v", widths[0], widths)
	}
	if diff := widths[1] - widths[2]; diff > 1 || diff < -1 {
		t.Fatalf("long columns were not shared evenly: %v", widths)
	}
}
