package tui

import (
	"testing"
)

// TestGlyphWhitelistWidth locks R-7: every glyph we draw must be exactly one
// column according to runeWidth, because our frame math is built on that model.
//
// This is the regression guard for the ⏳/⏸ class of bug: those read as width 1
// here while Emoji_Presentation makes terminals paint 2, which misaligns every
// persistent line in the UI.
func TestGlyphWhitelistWidth(t *testing.T) {
	for _, g := range whitelistedGlyphs {
		runes := []rune(g)
		if len(runes) != 1 {
			t.Errorf("glyph %q is not a single rune (%d runes)", g, len(runes))
			continue
		}
		if w := runeWidth(runes[0]); w != 1 {
			t.Errorf("glyph %q (U+%04X) has runeWidth %d, want 1", g, runes[0], w)
		}
	}
}

// TestGlyphWhitelistIsBMP asserts no whitelisted glyph is outside the BMP.
// Everything above U+FFFF is where emoji width disagreement lives.
func TestGlyphWhitelistIsBMP(t *testing.T) {
	for _, g := range whitelistedGlyphs {
		for _, r := range g {
			if r > 0xFFFF {
				t.Errorf("glyph %q contains non-BMP rune U+%04X", g, r)
			}
			if isEmojiRange(r) {
				t.Errorf("glyph %q contains banned rune U+%04X", g, r)
			}
		}
	}
}

// TestBannedGlyphsActuallyBanned guards against someone moving a banned rune
// into the whitelist without deleting it from the ban list.
func TestBannedGlyphsActuallyBanned(t *testing.T) {
	for banned := range bannedGlyphs {
		if !isEmojiRange(banned) {
			t.Errorf("banned rune U+%04X is not reported by isEmojiRange", banned)
		}
		for _, g := range whitelistedGlyphs {
			for _, r := range g {
				if r == banned {
					t.Errorf("banned rune U+%04X appears in whitelisted glyph %q", banned, g)
				}
			}
		}
	}
}

// TestBannedGlyphWidthsAreWrong documents *why* the banned set exists: the
// width model and the emoji presentation class genuinely disagree for these.
func TestBannedGlyphWidthsAreWrong(t *testing.T) {
	// U+23F3 and U+23F8 are the dangerous pair: our model says 1 column,
	// Emoji_Presentation says 2. If runeWidth is ever "fixed" to return 2,
	// these become safe and this test should be updated deliberately, not
	// silently.
	for _, r := range []rune{0x23F3, 0x23F8} {
		if w := runeWidth(r); w != 1 {
			t.Logf("note: U+%04X now reports runeWidth %d (was 1); re-evaluate the ban", r, w)
		}
	}
	// U+1F69A is the mirror case: model says 2, font fallback may draw 1.
	if w := runeWidth(0x1F69A); w != 2 {
		t.Errorf("U+1F69A runeWidth = %d, expected 2 (mirror-case reasoning)", w)
	}
}

// TestAttachedLabelIsPlainASCII pins the decision that "attached" is spelled out
// rather than drawn with a symbol.
func TestAttachedLabelIsPlainASCII(t *testing.T) {
	for _, r := range attachedLabel {
		if r > 127 {
			t.Errorf("attachedLabel contains non-ASCII rune U+%04X; it must be plain text", r)
		}
	}
}
