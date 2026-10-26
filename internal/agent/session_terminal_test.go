package agent

import (
	"strings"
	"testing"
	"unicode/utf16"
)

// TestEnvironmentBlockIsSortedDeduplicatedAndTerminated pins the encoding
// CreateProcess reads: entries separated by NUL in name order, the later value of a
// duplicated name, and an empty entry at the end. A block that misses the final NUL,
// or keeps two spellings of one name, is what leaves a Windows child unable to
// initialise.
func TestEnvironmentBlockIsSortedDeduplicatedAndTerminated(t *testing.T) {
	block, err := environmentBlock([]string{
		"ZED=last",
		"alpha=first",
		"BETA=middle",
		"alpha=later",
		"=C:=C:\\work", // a hidden per-drive variable, not a NAME=VALUE entry
		"noequals",
	})
	if err != nil {
		t.Fatal(err)
	}

	text := string(utf16.Decode(block))
	want := "alpha=later\x00BETA=middle\x00ZED=last\x00\x00"
	if text != want {
		t.Fatalf("block = %q, want %q", text, want)
	}
	if !strings.HasSuffix(text, "\x00\x00") {
		t.Fatal("block does not end in the empty entry CreateProcess requires")
	}

	// An environment with nothing usable still has to be a valid block rather than
	// a nil pointer, which CreateProcess would read as "inherit the parent's".
	empty, err := environmentBlock(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(utf16.Decode(empty)); got != "\x00\x00" {
		t.Fatalf("empty block = %q, want two NULs", got)
	}
}
