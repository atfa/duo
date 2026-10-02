package agent

import (
	"strings"
	"testing"
)

// TestFailureTailKeepsTheDriversOwnComplaint covers the reason this exists: a
// driver that refuses to run says why once, on the PTY it was given, and that
// text was reaching nobody. A real failure is reproduced byte for byte, including
// the escape sequences and carriage returns a PTY adds.
func TestFailureTailKeepsTheDriversOwnComplaint(t *testing.T) {
	// What `pi --auto` actually wrote before exiting 1.
	raw := []byte("\x1b[?25lpi\r\n\x1b[1mError: Unknown option: --auto\x1b[0m\r\n")

	got := failureTail(raw)
	if !strings.Contains(got, "Error: Unknown option: --auto") {
		t.Fatalf("driver's reason was lost: %q", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("escape sequences survived into the report: %q", got)
	}
	if strings.Contains(got, "\r") {
		t.Errorf("carriage returns survived into the report: %q", got)
	}
	if strings.Contains(got, "?25l") {
		t.Errorf("a stripped escape left its payload behind: %q", got)
	}

	// Nothing written means nothing to say, rather than an empty error block.
	if got := failureTail(nil); got != "" {
		t.Errorf("failureTail(nil) = %q, want empty", got)
	}
	if got := failureTail([]byte("\x1b[2J\x1b[H   \n\n  \n")); got != "" {
		t.Errorf("blank terminal output should read as empty, got %q", got)
	}
}

// TestFailureTailKeepsTheReasonNotTheBanner checks the failure that produced a
// twenty-minute diagnosis: the reason is a single line at the end, buried under
// whatever the driver painted while starting.
func TestFailureTailKeepsTheReasonNotTheBanner(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString("\x1b[2K\rloading extension ")
		b.WriteString(strings.Repeat("x", 200))
		b.WriteString("\n")
	}
	b.WriteString("Error: unknown model 'opencode/space-bunny-free'\n")

	got := failureTail([]byte(b.String()))
	if !strings.Contains(got, "unknown model 'opencode/space-bunny-free'") {
		t.Fatalf("the reason was truncated away: %q", got)
	}
	// A truncated tail must say so, or it reads as the whole story.
	if !strings.Contains(got, "earlier output omitted") {
		t.Errorf("a truncated tail is not marked as truncated:\n%s", got)
	}
	if lines := strings.Count(got, "\n") + 1; lines > failureTailLines+1 {
		t.Errorf("kept %d lines, want at most %d plus the marker", lines, failureTailLines)
	}
}
