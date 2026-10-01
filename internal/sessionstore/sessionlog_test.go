package sessionstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLogWriterSplitsTranscriptByAgent pins the two properties the feature
// exists for: the full transcript keeps every displayed entry in order, and each
// agent's own transcript holds exactly that agent's pane. It also covers the
// details that would otherwise only show up as a wrong file: a plain notice with
// no label, an error marker, and Markdown surviving the round trip.
func TestLogWriterSplitsTranscriptByAgent(t *testing.T) {
	repo := t.TempDir()
	const id = "20261001-233448-e4182840"

	w, err := NewLogWriter(repo, Transcript{
		SessionID:  id,
		Mode:       "goal",
		Repository: repo,
		Branch:     "main",
		BaseCommit: "242ba05",
	})
	if err != nil {
		t.Fatalf("NewLogWriter: %v", err)
	}

	base := time.Date(2026, 10, 1, 16, 35, 2, 0, time.UTC)
	// The interface files a message under its speaker's pane, so the human's
	// task lands in Duo's pane even though it is addressed to Austin.
	w.Append(TUIEntry{Time: base, Pane: "Duo", Label: "Human → Austin", Text: "Add a greeting package."})
	w.Append(TUIEntry{Time: base.Add(30 * time.Second), Pane: "Austin", Label: "Austin → Tony", Text: "I'll take `Greet`.\n\n```go\nfunc Greet() {}\n```"})
	w.Append(TUIEntry{Time: base.Add(time.Minute), Pane: "Duo", Label: "Tony → Austin", Text: "Taking Farewell."})
	w.Append(TUIEntry{Time: base.Add(90 * time.Second), Pane: "Duo", Text: "plan updated to v1"})
	w.Append(TUIEntry{Time: base.Add(2 * time.Minute), Pane: "Austin", Label: "Austin → Human", Text: "restart failed", Error: true})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	dir := filepath.Join(repo, ".duo", LogsDirName)
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(b)
	}

	full := read(id + ".md")
	austin := read(id + "-austin.md")
	tony := read(id + "-tony.md")

	// The header identifies the session and where it ran.
	for _, want := range []string{"# Duo session " + id, "Mode: goal", "Branch: main", "Base commit: 242ba05", "## Timeline"} {
		if !strings.Contains(full, want) {
			t.Errorf("full transcript missing %q\n%s", want, full)
		}
	}

	// Every displayed entry is present, in the order it arrived.
	var order []string
	for _, line := range strings.Split(full, "\n") {
		if strings.HasPrefix(line, "### ") {
			order = append(order, line)
		}
	}
	want := []string{
		"### " + base.Local().Format("15:04:05") + " · Human → Austin",
		"### " + base.Add(30*time.Second).Local().Format("15:04:05") + " · Austin → Tony",
		"### " + base.Add(time.Minute).Local().Format("15:04:05") + " · Tony → Austin",
		// A plain notice has no label and falls back to the speaker.
		"### " + base.Add(90*time.Second).Local().Format("15:04:05") + " · Duo",
		"### " + base.Add(2*time.Minute).Local().Format("15:04:05") + " · Austin → Human · error",
	}
	if len(order) != len(want) {
		t.Fatalf("full transcript has %d entries, want %d:\n%v", len(order), len(want), order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("entry %d:\n got %q\nwant %q", i, order[i], want[i])
		}
	}

	// Markdown written by an agent is preserved, not escaped or flattened.
	if !strings.Contains(full, "```go\nfunc Greet() {}\n```") {
		t.Errorf("agent code block not preserved:\n%s", full)
	}

	// Austin's log is everything he was told and everything he said. Both were
	// filed under another speaker's pane, so selecting by pane would lose them.
	if !strings.Contains(austin, "Add a greeting package.") {
		t.Errorf("Austin transcript missing the human's task:\n%s", austin)
	}
	if !strings.Contains(austin, "Taking Farewell.") {
		t.Errorf("Austin transcript missing Tony's reply to him:\n%s", austin)
	}
	if !strings.Contains(austin, "restart failed") {
		t.Errorf("Austin transcript missing his own error entry:\n%s", austin)
	}
	if strings.Contains(tony, "Add a greeting package.") {
		t.Errorf("Tony transcript leaked a Human → Austin entry:\n%s", tony)
	}
	if !strings.Contains(tony, "I'll take `Greet`.") {
		t.Errorf("Tony transcript missing Austin's offer to him:\n%s", tony)
	}
	if !strings.Contains(tony, "Taking Farewell.") {
		t.Errorf("Tony transcript missing his own entry:\n%s", tony)
	}
	if !strings.Contains(tony, "Agent: Tony") {
		t.Errorf("Tony transcript missing its agent header:\n%s", tony)
	}
	if strings.Contains(austin, "plan updated to v1") {
		t.Errorf("Duo notices belong only in the full transcript:\n%s", austin)
	}
}

// TestLogWriterIsolatedFromRepository covers the promise that writing a
// transcript cannot dirty the repository: the files live under the .duo
// directory that is already hidden, and a nil writer is inert so a session that
// could not create the directory still runs.
func TestLogWriterIsolatedFromRepository(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".duo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".duo", "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	w, err := NewLogWriter(repo, Transcript{SessionID: "20261001-000000-000000", Mode: "fast", Repository: repo})
	if err != nil {
		t.Fatalf("NewLogWriter: %v", err)
	}
	w.Append(TUIEntry{Pane: "austin", Label: "Human → Austin", Text: "hello"})
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The existing project config is untouched.
	cfg, err := os.ReadFile(filepath.Join(repo, ".duo", "config.json"))
	if err != nil || string(cfg) != "{}" {
		t.Errorf("config.json was modified: %q %v", cfg, err)
	}

	// A nil writer must be a no-op, not a panic: NewLogWriter returns nil when
	// there is no repository, and the TUI calls Append unconditionally.
	var nilWriter *LogWriter
	nilWriter.Append(TUIEntry{Pane: "austin", Text: "ignored"})
	if err := nilWriter.Close(); err != nil {
		t.Errorf("nil Close: %v", err)
	}
}
