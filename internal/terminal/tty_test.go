package terminal

import (
	"strings"
	"testing"
)

func TestResetOuterModes(t *testing.T) {
	for _, sequence := range []string{
		"\x1b[<u", "\x1b[>4;0m", "\x1b[?2004l",
		"\x1b[?1000l", "\x1b[?1002l", "\x1b[?1003l", "\x1b[?1004l", "\x1b[?1006l",
		"\x1b[?7h", "\x1b[?25h",
	} {
		if !strings.Contains(ResetOuterModes, sequence) {
			t.Errorf("ResetOuterModes missing %q", sequence)
		}
	}
}
