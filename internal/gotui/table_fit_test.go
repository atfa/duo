package gotui

import (
	"strings"
	"testing"

	rw "github.com/mattn/go-runewidth"
)

// The user's session 20261025-172648-161a656e showed a table that read as
// "+-| scraps": the greedy column allocator starved two columns at the minimum
// width while feeding the whole remaining budget to the widest one. This
// fixture is that real table (content abbreviated, shape preserved: 3 columns,
// CJK-heavy third column, natural widths roughly (12, 51, 161)).
const tableFitFixture = `| 区块 | 标题 | 主要要点 |
|---|---|---|
| Hero | Think Twice. Build Better. | 两个平级 agent、一个经过验证的结果；v0.14.2、安装命令 |
| Why Duo | An agent reviewing itself is still one perspective. | 结构性独立审查：两个 session、两个 worktree、同一个 commit，避免自审自批 |
| Workflows | Choose the level of rigor. | Fast / Step / Goal 三种协作策略，运行时可切换 |
| How it lands | Four checkpoints between an idea and your branch. | 四个由 Git 证据支撑的检查点；证据不匹配即关卡关闭 |
| Features | Small surface. Serious machinery. | 单个 Go 二进制；驱动 pi/agy/opencode/codex；gotui 仪表盘；任意 Git 仓库 |
| Remote | Steer the session from your phone. | /qrcode 配对；手机看双方实时状态；暂停/继续；切换 mode；密文中继、可自托管 |
| Quick start | Two commands to a second opinion. | 安装 + 在任意 Git 仓库中运行 |
| FAQ | The questions engineers ask first. | 与 subagent 的区别、是否需两套模型、冲突、验证失败、历史/交付、代码是否离开本机 |`

// TestDecoratedGlyphsMeasureOneCell pins the measuring rule where both layers
// read it: go-runewidth's package default, which the width pin in width.go
// forces onto tcell's host-independent rule. On a zh_CN host the unpinned
// default scores the ambiguous glyphs the renderer emits (box drawing, bullets,
// ellipsis) as 2 while the buffer paints them in one cell — the drift that made
// wrapping look random in Chinese locales.
func TestDecoratedGlyphsMeasureOneCell(t *testing.T) {
	glyphs := []string{"│", "─", "•", "…", "☐", "☑", "|", "→"}
	for _, g := range glyphs {
		if got := rw.StringWidth(g); got != 1 {
			t.Errorf("rw.StringWidth(%q) = %d, want 1: the buffer would paint one cell but measure two", g, got)
		}
	}
	// Real wide characters must stay wide on every locale.
	for _, g := range []string{"中", "宽"} {
		if got := rw.StringWidth(g); got != 2 {
			t.Errorf("rw.StringWidth(%q) = %d, want 2", g, got)
		}
	}
}

// TestTableFitDoesNotStarveColumns runs the real fixture across the widths a
// split dashboard can produce and asserts the allocator spreads the budget
// instead of pinning short columns at the floor. Under the greedy allocator
// this replaced, width 96 produced columns (6, 6, 74): the first column lost
// its natural 12 cells, so the pin on that width and column is what makes the
// test red again if the greedy pass ever comes back.
func TestTableFitDoesNotStarveColumns(t *testing.T) {
	tm := newTheme()
	for _, width := range []int{40, 50, 60, 70, 80, 96, 100, 120} {
		lines := wrapRuns(markdownRuns(tableFitFixture, tm.Body, tm, width), width)
		borders, dataRows := 0, 0
		var content strings.Builder
		var border string
		for _, line := range lines {
			text := line.text()
			if line.width() > width {
				t.Fatalf("width %d: line exceeds the pane: %q", width, text)
			}
			// Strip table chrome so content joined across wrapped rows and
			// column borders reads contiguously.
			content.WriteString(strings.Map(func(r rune) rune {
				switch r {
				case ' ', '|', '+', '-':
					return -1
				}
				return r
			}, text))
			trimmed := strings.TrimSpace(text)
			if strings.HasPrefix(trimmed, "+") && strings.Trim(trimmed, "+-") == "" {
				borders++
				if border == "" {
					border = text
				}
				continue
			}
			if strings.HasPrefix(trimmed, "|") {
				dataRows++
			}
		}
		if borders == 0 || dataRows == 0 {
			t.Fatalf("width %d: table not laid out with borders (borders=%d rows=%d):\n%v", width, borders, dataRows, lines)
		}
		// The allocator shares the budget: below width 50 the two long columns
		// compress evenly around the short one; from 50 up the shortest column
		// keeps its natural width and the two long ones stay within one cell
		// of each other. Under the greedy allocator the first column was
		// pinned at 6 while the last absorbed the entire remaining budget.
		parts := strings.Split(strings.Trim(strings.TrimSpace(border), "+"), "+")
		if len(parts) != 3 {
			t.Fatalf("width %d: border %q does not describe three columns", width, border)
		}
		widths := make([]int, len(parts))
		for i, part := range parts {
			widths[i] = rw.StringWidth(part) - 2
		}
		if width >= 50 && widths[0] != 12 {
			t.Fatalf("width %d: first column width = %d, want its natural 12: %v", width, widths[0], widths)
		}
		if diff := widths[1] - widths[2]; diff > 1 || diff < -1 {
			t.Fatalf("width %d: long columns were not shared evenly: %v", width, widths)
		}
		// No column starved: every border must carry three segments of at
		// least 4 dashes when the pane is this wide.
		for _, line := range lines {
			text := strings.TrimSpace(line.text())
			if !strings.HasPrefix(text, "+") || strings.Trim(text, "+-") != "" {
				continue
			}
			for _, seg := range strings.Split(strings.Trim(text, "+"), "+") {
				if n := strings.Count(seg, "-"); n < 4 {
					t.Fatalf("width %d: column starved to %d dashes in %q", width, n, text)
				}
			}
		}
		// Content survives.
		for _, want := range []string{"安装命令", "避免自审自批", "运行时可切换", "关卡关闭", "任意Git仓库", "可自托管", "中运行", "离开本机"} {
			if !strings.Contains(content.String(), want) {
				t.Fatalf("width %d: content %q lost", width, want)
			}
		}
	}
}

// TestTablePlainFallbackKeepsAllCells covers the too-narrow window: below the
// 3-cell-per-column floor the grid reads worse than plain rows, so the fallback
// drops the borders and keeps every cell's text inline. Width 12 is below the
// 3*3 floor (budget 9 < 9 needs width < 19) yet wide enough for the fixture's
// own header cells to keep their text.
func TestTablePlainFallbackKeepsAllCells(t *testing.T) {
	tm := newTheme()
	runs := markdownRuns(tableFitFixture, tm.Body, tm, 12)
	var joined strings.Builder
	for _, line := range wrapRuns(runs, 12) {
		joined.WriteString(strings.Map(func(r rune) rune {
			switch r {
			case ' ', '|', '+', '-':
				return -1
			}
			return r
		}, line.text()))
	}
	for _, want := range []string{"Hero", "WhyDuo", "FAQ", "安装命令", "关卡关闭"} {
		if !strings.Contains(joined.String(), want) {
			t.Fatalf("narrow fallback lost cell content %q:\n%s", want, joined.String())
		}
	}
}
