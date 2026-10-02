package agent

import (
	"regexp"
	"strings"
)

// A CLI driver launched into a PTY merges its stderr into the same stream as its
// output, and a PTY is a terminal, so what lands there is padded with escape
// sequences and carriage returns rather than clean lines. A driver that refuses
// to run says why exactly once, on that stream: "Error: Unknown option: --auto",
// "Error: unknown model". Nothing else in Duo carries it, so the session log ends
// up with only "exit status 1" and the operator is left guessing.
var (
	// CSI sequences, which cover colour, cursor movement and erase, plus the
	// two-character and charset-select escapes.
	failureANSI = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]|\x1b[()][A-Za-z0-9]|\x1b[@-Z\\\\-_]")
	// Anything left that still looks like an escape, so a partial sequence from a
	// truncated buffer cannot reach the log.
	failureStrayEscape = regexp.MustCompile("\x1b")
)

// failureTailLines is how much of a failed process's output is kept. A driver
// prints a handful of lines before giving up; keeping more buries the reason in
// whatever it rendered while starting.
const failureTailLines = 8

// failureTail extracts the last few meaningful lines a process wrote.
//
// It is deliberately lossy. The goal is the driver's own complaint, not a
// faithful transcript, because this text is shown in the interface as an error
// and written to the session log for someone reading it after the fact.
func failureTail(recent []byte) string {
	if len(recent) == 0 {
		return ""
	}
	text := string(failureANSI.ReplaceAll(recent, nil))
	text = failureStrayEscape.ReplaceAllString(text, "")
	// A PTY rewrites one line with \r; only the final state of each line is
	// worth keeping.
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	lines := strings.Split(text, "\n")
	kept := make([]string, 0, failureTailLines)
	for _, line := range lines {
		// Terminals pad with spaces to the window width.
		line = strings.TrimSpace(strings.TrimRight(line, "\x00"))
		if line == "" {
			continue
		}
		kept = append(kept, line)
	}
	if len(kept) > failureTailLines {
		// Mark that the reason is further up, so a truncated tail is not mistaken
		// for the whole story.
		kept = append([]string{"... earlier output omitted ..."}, kept[len(kept)-failureTailLines:]...)
	}
	return strings.Join(kept, "\n")
}
