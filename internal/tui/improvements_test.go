package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// A2/E2: the system log is a real, hittable, scrollable pane.
func TestSystemLogPaneScrollsAndSelects(t *testing.T) {
	a := testApp(80, 24)
	for i := 0; i < 20; i++ {
		a.add(protocol.Duo, "system line")
	}
	l := a.layout()
	if got := a.hitPane(4, l.logFirst); got != protocol.Duo {
		t.Fatalf("hitPane at system log = %q, want Duo", got)
	}
	if got := a.hitPane(4, l.logFirst-1); got == protocol.Duo {
		t.Fatal("the status row above the log must not be part of it")
	}
	a.scrollPane(protocol.Duo, 3)
	if a.duoOffset == 0 {
		t.Fatal("the system log did not scroll")
	}
	point, ok := a.selectionPoint(protocol.Duo, 4, l.logFirst)
	if !ok || point.row != 0 || point.col != 1 {
		t.Fatalf("system log selection point = %+v ok=%v", point, ok)
	}
}

// A3: a scrolled pane keeps its position while output grows, and remembers that
// unseen output arrived below.
func TestStickyScrollKeepsPositionWhileOutputGrows(t *testing.T) {
	offset, seen, fresh := stickyScroll(4, 10, 10, 5, false)
	if offset != 4 || seen != 10 || fresh {
		t.Fatalf("no growth = %d,%d,%v", offset, seen, fresh)
	}
	offset, seen, fresh = stickyScroll(4, 10, 13, 5, false)
	if offset != 7 || seen != 13 || !fresh {
		t.Fatalf("growth = %d,%d,%v", offset, seen, fresh)
	}
	offset, _, fresh = stickyScroll(0, 13, 20, 5, false)
	if offset != 0 || fresh {
		t.Fatalf("pinned pane = %d,%v", offset, fresh)
	}
}

// B1: composer editing keys.
func TestComposerEditingKeys(t *testing.T) {
	a := testApp(80, 24)
	a.input = []byte("hello world")
	a.inputPos = len(a.input)

	a.handleKey("ctrl-w")
	if got := string(a.input); got != "hello " {
		t.Fatalf("ctrl-w = %q", got)
	}
	a.handleKey("home")
	if a.inputPos != 0 {
		t.Fatalf("home = %d", a.inputPos)
	}
	a.handleKey("ctrl-k")
	if got := string(a.input); got != "" {
		t.Fatalf("ctrl-k = %q", got)
	}

	a.input = []byte("one two")
	a.inputPos = len(a.input)
	a.handleKey("alt-left")
	if a.inputPos != len("one ") {
		t.Fatalf("alt-left = %d, want %d", a.inputPos, len("one "))
	}
	a.handleKey("alt-right")
	if a.inputPos != len("one two") {
		t.Fatalf("alt-right = %d", a.inputPos)
	}

	a.input = []byte("ab\ncd")
	a.inputPos = len(a.input)
	if !a.moveComposerLine(-1) || a.inputPos != len("ab") {
		t.Fatalf("line up = %d", a.inputPos)
	}
	if a.moveComposerLine(-1) {
		t.Fatal("line up must stop at the first composer line")
	}

	// Forward delete (Del key) deletes character after cursor
	if got := decodeEscape("\x1b[3~"); got != "delete" {
		t.Fatalf("decodeEscape(\\x1b[3~) = %q, want \"delete\"", got)
	}
	a.input = []byte("hello")
	a.inputPos = 0
	a.handleKey("delete")
	if got := string(a.input); got != "ello" || a.inputPos != 0 {
		t.Fatalf("delete at start = %q (pos %d), want \"ello\" (pos 0)", got, a.inputPos)
	}
	a.inputPos = 1 // between 'e' and 'l'
	a.handleKey("delete")
	if got := string(a.input); got != "elo" || a.inputPos != 1 {
		t.Fatalf("delete in middle = %q (pos %d), want \"elo\" (pos 1)", got, a.inputPos)
	}
	a.inputPos = len(a.input) // at end
	a.handleKey("delete")
	if got := string(a.input); got != "elo" {
		t.Fatalf("delete at end should do nothing, got %q", got)
	}

	// Forward delete with multi-byte UTF-8
	a.input = []byte("你好世界")
	a.inputPos = len("你") // pointing to "好"
	a.handleKey("delete")
	if got := string(a.input); got != "你世界" || a.inputPos != len("你") {
		t.Fatalf("delete utf-8 = %q (pos %d), want \"你世界\"", got, a.inputPos)
	}
}

// B2: submitted tasks are recalled with Up/Down, and the draft is restored.
func TestComposerHistoryRecall(t *testing.T) {
	a := testApp(80, 24)
	a.pushHistory("first task")
	a.pushHistory("second task")
	a.input, a.inputPos = []byte("draft"), len("draft")

	a.historyPrev()
	if got := string(a.input); got != "second task" {
		t.Fatalf("prev = %q", got)
	}
	a.historyPrev()
	if got := string(a.input); got != "first task" {
		t.Fatalf("prev2 = %q", got)
	}
	a.historyNext()
	if got := string(a.input); got != "second task" {
		t.Fatalf("next = %q", got)
	}
	a.historyNext()
	if got := string(a.input); got != "draft" {
		t.Fatalf("restored draft = %q", got)
	}
}

// C1: word wrapping prefers a space so words are not split mid-token.
func TestWordWrapKeepsWordsIntact(t *testing.T) {
	var got []string
	for _, line := range markdownLines("hello world example", 11) {
		for _, wrapped := range wrapMarkdown(line, 11) {
			got = append(got, wrapped.text())
			if w := displayWidth(wrapped.text()); w > 11 {
				t.Fatalf("wrapped line is %d columns: %q", w, wrapped.text())
			}
		}
	}
	if len(got) < 2 || strings.TrimSpace(got[0]) != "hello world" {
		t.Fatalf("word wrap = %q", got)
	}
}

// C2: timestamps are opt-in so they do not cost columns by default.
// The split fallback pane prefix is opt-in; the timeline stamps its message
// headers by default (Ctrl+G), which timeline_test covers.
func TestSplitPaneTimestampsAreOptIn(t *testing.T) {
	a := testApp(100, 30)
	stamp := time.Date(2026, 9, 29, 7, 30, 5, 0, time.UTC)
	a.austin = []entry{{at: stamp, text: "hello"}}
	if strings.Contains(a.paneLines(protocol.Austin, 40)[0].text(), "07:30:05") {
		t.Fatal("timestamps must be off by default")
	}
	a.showTimestamps = true
	if got := a.paneLines(protocol.Austin, 40)[0].text(); !strings.HasPrefix(got, "07:30:05 ") {
		t.Fatalf("timestamped line = %q", got)
	}
}

// C5: a markdown link becomes an OSC 8 hyperlink whose label stays copyable.
func TestMarkdownLinksEmitOSC8(t *testing.T) {
	spans := markdownInline("[docs](https://example.com)")
	if len(spans) == 0 || spans[0].link != "https://example.com" {
		t.Fatalf("link span = %+v", spans)
	}
	painted := paintEntry(paneLine{spans: spans}, 40)
	if !strings.Contains(painted, "\x1b]8;;https://example.com\x1b\\") {
		t.Fatalf("no OSC 8 hyperlink in %q", painted)
	}
	if displayWidth(spans[0].text) != 4 {
		t.Fatalf("label width = %d", displayWidth(spans[0].text))
	}
}

// C4/G1: the session overview carries the full state the status row truncates.
func TestDetailViewShowsFullSessionState(t *testing.T) {
	a := testApp(100, 30)
	state := project.NewStateFor(project.ModeFast)
	if _, _, err := state.SetReady(protocol.Austin, true, "work complete", "abc123"); err != nil {
		t.Fatal(err)
	}
	a.setProject(state)
	joined := strings.Join(a.detailLines(90), "\n")
	for _, want := range []string{"Session", "Worktrees", "Verification", "head: abc123", "Delivery"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("detail view missing %q:\n%s", want, joined)
		}
	}
	a.view = viewDetail
	frame := ansiPattern.ReplaceAllString(a.buildFrame(renderNormal), "")
	if !strings.Contains(frame, "Duo Session") {
		t.Fatalf("detail frame = %q", frame)
	}
}

// H1: wrapped pane lines are cached until the entries or the width change.
func TestPaneLinesAreCachedUntilEntriesChange(t *testing.T) {
	a := testApp(80, 24)
	a.add(protocol.Austin, "one")
	first := a.paneLines(protocol.Austin, 30)
	second := a.paneLines(protocol.Austin, 30)
	if &first[0] != &second[0] {
		t.Fatal("pane lines were rebuilt without a change")
	}
	a.add(protocol.Austin, "two")
	third := a.paneLines(protocol.Austin, 30)
	if &third[0] == &first[0] {
		t.Fatal("pane lines were not invalidated after an append")
	}
}

// G2/D2: delivery state stays visible and severity is carried by text, not just
// colour, so the UI is readable in a monochrome terminal.
func TestStatusLabelsAreColorIndependent(t *testing.T) {
	a := testApp(100, 30)
	plain := ansiPattern.ReplaceAllString(a.buildFrame(renderNormal), "")
	if !strings.Contains(plain, "Delivery:") {
		t.Fatalf("delivery state is not always visible:\n%s", plain)
	}
	a.route(events.Event{Kind: events.KindError, Agent: protocol.Austin, Text: "429"})
	if got := a.austin[len(a.austin)-1].text; !strings.HasPrefix(got, "ERROR:") {
		t.Fatalf("error label = %q", got)
	}
	a.route(events.Event{Kind: events.KindVerdict, Agent: protocol.Tony, Peer: protocol.Austin, Text: "gap"})
	if got := a.tony[len(a.tony)-1].text; !strings.HasPrefix(got, "VERIFY") {
		t.Fatalf("verdict label = %q", got)
	}
}

func TestTimelineKeyboardPageUpDown(t *testing.T) {
	a := testApp(80, 24)
	a.timeline = true
	// Add enough entries to be scrollable
	for i := 0; i < 40; i++ {
		a.add(protocol.Austin, "conversation line")
	}

	act := a.handleKey("page-up")
	if act.kind != actionScrollPane || act.agent != protocol.Duo || act.delta <= 0 {
		t.Fatalf("page-up action = %+v, want actionScrollPane on Duo with positive delta", act)
	}
	a.scrollPane(act.agent, act.delta)
	if a.duoOffset == 0 {
		t.Fatal("timeline offset did not increase on page-up")
	}

	actDown := a.handleKey("page-down")
	if actDown.kind != actionScrollPane || actDown.agent != protocol.Duo || actDown.delta >= 0 {
		t.Fatalf("page-down action = %+v, want actionScrollPane on Duo with negative delta", actDown)
	}
	a.scrollPane(actDown.agent, actDown.delta)
	if a.duoOffset != 0 {
		t.Fatalf("timeline offset after page-down = %d, want 0", a.duoOffset)
	}
}

func TestOSC52SequenceGeneration(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TERM", "xterm-256color")
	seq := osc52Sequence("hello world")
	if !strings.HasPrefix(seq, "\x1b]52;c;") || !strings.HasSuffix(seq, "\x07") {
		t.Fatalf("unexpected standard OSC 52 sequence: %q", seq)
	}

	t.Setenv("TMUX", "1")
	seqTmux := osc52Sequence("hello world")
	if !strings.HasPrefix(seqTmux, "\x1bPtmux;\x1b\x1b]52;c;") || !strings.HasSuffix(seqTmux, "\x1b\\") {
		t.Fatalf("unexpected tmux OSC 52 sequence: %q", seqTmux)
	}
}

func TestPersistentComposerHistory(t *testing.T) {
	temp := t.TempDir()
	histFile := filepath.Join(temp, "history")
	t.Setenv("DUO_HISTORY_FILE", histFile)

	a := testApp(80, 24)
	a.pushHistory("task 1")
	a.pushHistory("task 2")

	// Verify file was written
	loaded := loadComposerHistory()
	if len(loaded) != 2 || loaded[0] != "task 1" || loaded[1] != "task 2" {
		t.Fatalf("unexpected loaded history: %v", loaded)
	}

	// Verify multiline task is escaped on single line and restored
	a.pushHistory("task 3\nline 2")
	loaded2 := loadComposerHistory()
	if len(loaded2) != 3 || loaded2[2] != "task 3\nline 2" {
		t.Fatalf("unexpected multiline history: %v", loaded2)
	}
}

func TestDetailViewChangesSummary(t *testing.T) {
	a := testApp(100, 30)
	lines := a.detailLines(90)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Changes (Austin vs base)") {
		t.Fatalf("detailLines missing changes header:\n%s", joined)
	}
	if !strings.Contains(joined, "(no changes vs base)") {
		t.Fatalf("detailLines missing clean note:\n%s", joined)
	}

	a.cachedChanges = []string{"file1.go | 2 +-", "Untracked:", "  ? new.txt"}
	lines2 := a.detailLines(90)
	joined2 := strings.Join(lines2, "\n")
	if !strings.Contains(joined2, "file1.go | 2 +-") || !strings.Contains(joined2, "? new.txt") {
		t.Fatalf("detailLines missing cached changes:\n%s", joined2)
	}
}

func TestDetailViewToggleWithCtrlO(t *testing.T) {
	a := testApp(80, 24)
	if a.view != viewMain {
		t.Fatalf("initial view = %v, want viewMain", a.view)
	}

	ctx := context.Background()
	act := a.handleKey("ctrl-o")
	if act.kind != actionToggleDetail {
		t.Fatalf("handleKey(ctrl-o) = %+v, want actionToggleDetail", act)
	}
	a.applyAction(ctx, act)
	if a.view != viewDetail {
		t.Fatalf("view after first ctrl-o = %v, want viewDetail", a.view)
	}

	actClose := a.handleKey("ctrl-o")
	if actClose.kind != actionToggleDetail {
		t.Fatalf("handleKey(ctrl-o) in viewDetail = %+v, want actionToggleDetail", actClose)
	}
	a.applyAction(ctx, actClose)
	if a.view != viewMain {
		t.Fatalf("view after second ctrl-o = %v, want viewMain", a.view)
	}
}
