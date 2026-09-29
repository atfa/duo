package tui

import (
	"strings"
	"testing"

	"github.com/atfa/duo/internal/protocol"
)

// The preview band is the only always-visible view of what an agent is doing
// while a long turn runs, so it must show the tool, a failure and the streamed
// text, and must not break the frame's exact geometry.
func TestWorkPreviewShowsToolFailureAndStreamTail(t *testing.T) {
	a := testApp(100, 30)
	a.tracker.Handle(protocol.Austin, protocol.ActivityAgentStart)
	a.tracker.Handle(protocol.Austin, protocol.ActivityToolStart)
	a.tracker.Note(protocol.Austin, protocol.ActivityToolStart, "bash", "go test ./...")
	a.tracker.Handle(protocol.Tony, protocol.ActivityProviderStart)
	a.tracker.Note(protocol.Tony, protocol.ActivityStream, "", "checking the diff on Tony's branch")
	a.tracker.NoteError(protocol.Austin, "provider 429")

	frame := a.buildFrame(renderNormal)
	plain := ansiPattern.ReplaceAllString(frame, "")
	for _, want := range []string{
		"Austin preview",
		"Tony preview",
		"▶ bash · go test ./...",
		"✗ provider 429",
		"▸ checking the diff on Tony's branch",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("preview missing %q:\n%s", want, plain)
		}
	}

	// buildFrame deliberately leaves the last terminal row unused.
	lines := strings.Split(plain, "\r\n")
	if len(lines) != a.height-1 {
		t.Fatalf("frame has %d rows, want %d", len(lines), a.height-1)
	}
	for i, line := range lines {
		if got := displayWidth(line); got != a.width {
			t.Errorf("row %d is %d columns, want %d: %q", i, got, a.width, line)
		}
	}
}

func TestPreviewCanBeHiddenAndIsSkippedWhenShort(t *testing.T) {
	a := testApp(100, 30)
	if a.layoutFor(a.width, a.height).preview == 0 {
		t.Fatal("preview should be visible at 100x30")
	}
	a.handleKey("ctrl-p")
	if !a.hidePreview {
		t.Fatal("ctrl-p did not collapse the preview")
	}
	if got := a.layoutFor(a.width, a.height).preview; got != 0 {
		t.Fatalf("hidden preview still reports %d rows", got)
	}
	if strings.Contains(ansiPattern.ReplaceAllString(a.buildFrame(renderNormal), ""), "Austin preview") {
		t.Fatal("hidden preview is still rendered")
	}

	// A short terminal keeps usable panes instead of a preview band.
	short := testApp(100, 24)
	if got := short.layoutFor(short.width, short.height).preview; got != 0 {
		t.Fatalf("24-row terminal got a %d-row preview", got)
	}
	if got := short.layoutFor(short.width, short.height).content; got < 6 {
		t.Fatalf("24-row terminal has only %d pane rows", got)
	}
}

// The band's rows and the panes' rows are complementary: shrinking one must give
// the lost rows to the other so the frame always exactly fills the terminal.
func TestPreviewRowsTradeWithPaneRows(t *testing.T) {
	a := testApp(100, 40)
	shown := a.layoutFor(a.width, a.height)
	a.hidePreview = true
	hidden := a.layoutFor(a.width, a.height)

	gained := hidden.content - shown.content
	if gained != shown.preview+1 {
		t.Fatalf("hiding the preview gave %d pane rows, want %d", gained, shown.preview+1)
	}
}
