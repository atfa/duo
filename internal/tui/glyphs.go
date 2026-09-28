package tui

// Glyph whitelist (R-7).
//
// Every glyph the TUI draws must come from this file. The rule is not aesthetic:
// runeWidth() is our own column model, and it disagrees with what terminals
// actually paint for emoji.
//
//	⏳ U+23F3  runeWidth -> 1   but Emoji_Presentation=Yes, terminals draw 2
//	⏸ U+23F8  runeWidth -> 1   same problem
//	🚚 U+1F69A runeWidth -> 2   but font fallback in tmux/mosh can draw 1
//	⚿ U+26BF  no glyph in most fonts -> tofu
//
// Either direction of that mismatch breaks frame alignment, and it breaks it on
// every single persistent line. So: only BMP glyphs that runeWidth agrees with,
// and that render in ordinary monospace fonts.

// Whitelisted glyphs. Each must satisfy runeWidth(r) == 1.
const (
	glyphWorking  = "●" // U+25CF filled circle - actively producing
	glyphStream   = "◐" // U+25D0 left half circle - streaming partial output
	glyphIdle     = "◦" // U+25E6 small white circle - connected, quiet
	glyphExited   = "○" // U+25CB white circle - process gone
	glyphOK       = "✓" // U+2713 check mark
	glyphFail     = "✗" // U+2717 ballot x
	glyphWarn     = "▲" // U+25B2 up triangle - needs attention
	glyphNote     = "▼" // U+25BC down triangle - unread / scroll down
	glyphLine     = "─" // U+2500 horizontal box drawing
	glyphBar      = "│" // U+2502 vertical box drawing
	glyphDot      = "·" // U+00B7 middle dot - separator
)

// attachedLabel is used instead of a glyph for "a human is attached to this
// agent's native session". There is no BMP glyph that reads clearly for this,
// so we spend the columns on words rather than gamble on a symbol.
const attachedLabel = "[attached]"

// bannedGlyphs are glyphs we must never introduce, with the reason recorded so
// nobody re-adds one thinking it looks nicer.
//
// Each entry is (rune, why).
var bannedGlyphs = map[rune]string{
	0x23F3: "hourglass: Emoji_Presentation=Yes, drawn 2 cols but runeWidth says 1",
	0x23F8: "pause: Emoji_Presentation=Yes, drawn 2 cols but runeWidth says 1",
	0x26A0: "warning sign: Emoji_Presentation=Yes, same width split",
	0x26BF: "squared keycap: absent from most monospace fonts, renders tofu",
	0x1F69A: "truck: runeWidth says 2, font fallback can draw 1",
}

// whitelistedGlyphs lists every glyph constant above. glyphs_test.go walks this
// and asserts the width contract holds, so adding a constant without adding it
// here fails the build-time test rather than shipping a broken frame.
var whitelistedGlyphs = []string{
	glyphWorking,
	glyphStream,
	glyphIdle,
	glyphExited,
	glyphOK,
	glyphFail,
	glyphWarn,
	glyphNote,
	glyphLine,
	glyphBar,
	glyphDot,
}

// isEmojiRange reports whether r falls in a range we refuse to draw.
//
// U+1F300-U+1FAFF is caught by runeWidth as width 2, which is exactly why it
// is unsafe here: our frame math and a terminal's font fallback do not have to
// agree across tmux/mosh.
func isEmojiRange(r rune) bool {
	return (r >= 0x1F300 && r <= 0x1FAFF) ||
		(r >= 0x1F000 && r <= 0x1F2FF) ||
		r == 0x23F3 || r == 0x23F8 || r == 0x26A0 || r == 0x26BF
}
