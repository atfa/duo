package tui

import (
	"testing"
	"unicode/utf8"
)

func TestChineseInputWaitsForCompleteRune(t *testing.T) {
	a := testApp(80, 24)

	// Terminal reads may split a UTF-8 rune. Do not insert an invalid prefix,
	// because Backspace would then delete only part of the character.
	for i, b := range []byte("中") {
		a.handleByte(b)
		if i < 2 && len(a.input) != 0 {
			t.Fatalf("partial rune inserted after byte %d: %q", i, a.input)
		}
	}
	if got := string(a.input); got != "中" {
		t.Fatalf("complete rune = %q", got)
	}

	// An interrupted rune must not become a half-character. Backspace still
	// deletes the previous complete rune.
	for _, b := range []byte("文")[:2] {
		a.handleByte(b)
	}
	a.handleByte(127)
	if got := string(a.input); got != "" || !utf8.Valid(a.input) {
		t.Fatalf("backspace after partial rune = %q (valid %v)", got, utf8.Valid(a.input))
	}

	for _, b := range []byte("中文") {
		a.handleByte(b)
	}
	a.handleByte(127)
	if got := string(a.input); got != "中" {
		t.Fatalf("backspace complete rune = %q", got)
	}

	// Complete runes also arrive as one key from escape-sequence decoding.
	a.handleKey("文")
	if got := string(a.input); got != "中文" {
		t.Fatalf("complete key = %q", got)
	}
}
