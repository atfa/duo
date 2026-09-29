package tui

import (
	"testing"

	"github.com/atfa/duo/internal/protocol"
)

// TestDisplayWidthCountsEmojiAsTerminalColumns is the regression for emoji
// pushing pane borders right: an emoji the terminal draws two columns wide must
// not be measured as one.
func TestDisplayWidthCountsEmojiAsTerminalColumns(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"ascii", "Austin", 6},
		{"cjk", "中文", 4},
		{"check mark", "✅", 2},
		{"cross mark", "❌", 2},
		{"warning with variation selector", "⚠️", 2},
		{"star", "⭐", 2},
		{"bullseye", "🎯", 2},
		{"thumbs up", "👍", 2},
		{"enclosed alphanumeric", "🅰️", 2},
		{"flag pair", "🇨🇳", 2},
		{"skin tone sequence", "👍🏽", 2},
		{"zero width joiner", "a\u200db", 2},
		{"text symbol stays narrow", "©", 1},
		// U+2197 is Extended_Pictographic but text-presentation, so a terminal
		// draws it one column wide. Counting it as two shifted the pane header.
		{"north east arrow", "↗", 1},
		{"warning without selector", "⚠", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := displayWidth(tc.in); got != tc.want {
				t.Fatalf("displayWidth(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestPaneHeaderDividerAlignsWithContent is the regression for the header
// button (↗) shifting the ┬ away from the │ below it.
func TestPaneHeaderDividerAlignsWithContent(t *testing.T) {
	a := testApp(100, 30)
	a.add(protocol.Austin, "hello")
	a.add(protocol.Tony, "world")
	lines := visibleLines(a.buildFrame(renderNormal))
	split := a.layout().leftW + 1
	if len(lines) < 2 {
		t.Fatalf("frame has %d lines", len(lines))
	}
	title := []rune(lines[0])
	if split >= len(title) || title[split] != '┬' {
		t.Fatalf("title divider column %d = %q, want ┬: %q", split, string(runeAt(title, split)), lines[0])
	}
	content := []rune(lines[1])
	if split >= len(content) || content[split] != '│' {
		t.Fatalf("content divider column %d = %q, want │: %q", split, string(runeAt(content, split)), lines[1])
	}
}

func runeAt(runes []rune, i int) rune {
	if i < 0 || i >= len(runes) {
		return '?'
	}
	return runes[i]
}

// TestFitKeepsColumnWidthWithEmoji proves the padding math agrees with the
// terminal, so a pane line never overflows its fixed width.
func TestFitKeepsColumnWidthWithEmoji(t *testing.T) {
	for _, in := range []string{"✅ 完成", "❌ 失败：注册局保留名", "⚠️ 注意", "🎯 duo.dev"} {
		line := fit(in, 12)
		if got := displayWidth(line); got != 12 {
			t.Fatalf("fit(%q, 12) = %d columns, want 12: %q", in, got, line)
		}
	}
}
