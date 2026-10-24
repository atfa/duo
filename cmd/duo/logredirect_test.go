package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// redirectTo captures the standard logger for one test and restores it after.
func redirectTo(t *testing.T, w *bytes.Buffer) {
	t.Helper()
	previous := log.Writer()
	log.SetOutput(w)
	t.Cleanup(func() { log.SetOutput(previous) })
}

// The interface draws on the terminal's own rows, so a log line that reaches
// stderr wraps the last row and scrolls the whole frame up: exactly what a
// repeated /qrcode used to do. While the interface owns the terminal the
// standard logger must write to the session's log file and come back afterwards.
func TestRedirectLogKeepsTheTerminalFree(t *testing.T) {
	var terminal bytes.Buffer
	redirectTo(t, &terminal)

	repo := t.TempDir()
	restore := redirectLog(repo, "sess-1")
	log.Printf("Duo remote: connected to room ABC")
	restore()

	if terminal.Len() != 0 {
		t.Fatalf("stderr received %q while the interface owned the terminal", terminal.String())
	}
	path := filepath.Join(repo, ".duo", "logs", "sess-1.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session log: %v", err)
	}
	if !strings.Contains(string(data), "connected to room ABC") {
		t.Fatalf("session log = %q, want the log line", data)
	}

	// A resumed session appends: the earlier run's lines stay readable.
	restore = redirectLog(repo, "sess-1")
	log.Printf("Duo remote: resumed")
	restore()
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session log after resume: %v", err)
	}
	for _, want := range []string{"connected to room ABC", "resumed"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("session log = %q, want it to keep %q", data, want)
		}
	}

	// Restoring puts the previous writer back, so a failure after the interface
	// closed is still reported where the human can see it.
	log.Printf("after the interface")
	if !strings.Contains(terminal.String(), "after the interface") {
		t.Fatalf("stderr = %q, want the restored writer", terminal.String())
	}
}

// A log file that cannot be opened must not hand the logger back to stderr: a
// stray line on the terminal is the bug, and a lost diagnostic is not worse.
func TestRedirectLogDiscardsWhenTheLogCannotBeOpened(t *testing.T) {
	var terminal bytes.Buffer
	redirectTo(t, &terminal)

	blocker := filepath.Join(t.TempDir(), "repo")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	restore := redirectLog(blocker, "sess-1")
	log.Printf("Duo remote: this must not reach the terminal")
	restore()

	if terminal.Len() != 0 {
		t.Fatalf("stderr received %q after the log file failed to open", terminal.String())
	}
}

// A session id can arrive from `duo resume <id>`, so it is reduced to a file
// name inside the log directory instead of being joined as a path.
func TestLogFileNameStaysInsideTheLogDirectory(t *testing.T) {
	cases := map[string]string{
		"20261024-075647-1c3b0802": "20261024-075647-1c3b0802.log",
		"../../etc/passwd":         "------etc-passwd.log",
		"":                         "duo.log",
		"  ":                       "duo.log",
	}
	for in, want := range cases {
		if got := logFileName(in); got != want {
			t.Errorf("logFileName(%q) = %q, want %q", in, got, want)
		}
	}
}
