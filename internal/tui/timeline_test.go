package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
)

// The main view is a single chronological conversation: Austin's messages on
// the left, Tony's indented so their blocks read on the right, and every line
// still exactly terminal width.
func TestTimelineMainViewAlignsSpeakersAndKeepsGeometry(t *testing.T) {
	a := testApp(100, 30)
	a.timeline = true
	a.route(events.Event{Kind: events.KindUser, Agent: protocol.Duo, Text: "fix the cache bug"})
	a.route(events.Event{Kind: events.KindPeer, Agent: protocol.Austin, Peer: protocol.Tony, Text: "please check the cache path"})
	a.route(events.Event{Kind: events.KindPeer, Agent: protocol.Tony, Peer: protocol.Austin, Text: "the retry never happens"})
	a.route(events.Event{Kind: events.KindAssistant, Agent: protocol.Austin, Text: "done, the commit is abc123"})

	// The whole content area is one selectable, scrollable timeline.
	if got := a.hitPane(4, 4); got != protocol.Duo {
		t.Fatalf("timeline hitPane = %q, want Duo", got)
	}

	plain := ansiPattern.ReplaceAllString(a.buildFrame(renderNormal), "")
	for _, want := range []string{"Human → Austin", "Austin → Tony", "Tony → Austin", "Austin → Human"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("timeline missing %q:\n%s", want, plain)
		}
	}

	lines := strings.Split(plain, "\r\n")
	if len(lines) != a.height-1 {
		t.Fatalf("frame has %d rows, want %d", len(lines), a.height-1)
	}
	for i, line := range lines {
		if got := displayWidth(line); got != a.width {
			t.Fatalf("row %d is %d columns, want %d: %q", i, got, a.width, line)
		}
	}

	// Tony's side is indented; his label must not start at the left margin.
	for _, line := range lines {
		if at := strings.Index(line, "Tony → Austin"); at >= 0 {
			if at < a.width/4 {
				t.Fatalf("Tony label is not right-shifted (col %d): %q", at, line)
			}
			return
		}
	}
	t.Fatal("Tony → Austin label not found")
}

// The timeline is one scrollable, selectable region keyed to the existing Duo
// scroll/selection plumbing.
func TestTimelineScrollsAndSelects(t *testing.T) {
	a := testApp(80, 20)
	a.timeline = true
	for i := 0; i < 30; i++ {
		a.addLabeled(protocol.Austin, "Austin → Tony", "message body", false, false)
	}
	a.scrollPane(protocol.Duo, 5)
	if a.duoOffset == 0 {
		t.Fatal("timeline did not scroll")
	}

	point, ok := a.selectionPoint(protocol.Duo, 4, 3)
	if !ok || point.row != 1 || point.col != 2 {
		t.Fatalf("timeline selection point = %+v ok=%v", point, ok)
	}

	plain := ansiPattern.ReplaceAllString(a.buildFrame(renderNormal), "")
	lines := strings.Split(plain, "\r\n")
	if len(lines) != a.height-1 {
		t.Fatalf("frame has %d rows, want %d", len(lines), a.height-1)
	}
	for i, line := range lines {
		if got := displayWidth(line); got != a.width {
			t.Fatalf("row %d is %d columns, want %d: %q", i, got, a.width, line)
		}
	}
}

// Both speakers share one bubble width, so Austin's text is not wider than
// Tony's; peer headers stay dim but keep the speaker's own hue, so the sides are
// told apart by anchoring and colour rather than by weight.
func TestTimelineSpeakersShareHeaderStyleAndBubbleWidth(t *testing.T) {
	const width = 100
	body := strings.Repeat("x", 500)
	austin := timelineEntryLines(timelineEntry{origin: protocol.Austin, entry: entry{label: "Austin → Tony", text: body}}, width, false)
	tony := timelineEntryLines(timelineEntry{origin: protocol.Tony, entry: entry{label: "Tony → Austin", text: body}}, width, false)

	for _, tc := range []struct {
		name  string
		lines []paneLine
		want  string
	}{
		{"Austin", austin, ansiAustinDim},
		{"Tony", tony, ansiTonyDim},
	} {
		if got := tc.lines[0].spans[0].style; got != tc.want {
			t.Fatalf("%s header style = %q, want %q", tc.name, got, tc.want)
		}
	}

	indent := timelineIndent(width)
	austinBubble := displayWidth(austin[1].text())
	tonyBubble := displayWidth(tony[1].text()) - indent
	if austinBubble != tonyBubble {
		t.Fatalf("bubble widths differ: Austin %d, Tony %d", austinBubble, tonyBubble)
	}
	if austinBubble >= width {
		t.Fatalf("Austin bubble %d spans the full %d-column row", austinBubble, width)
	}
	if lead := displayWidth(tony[1].text()) - displayWidth(strings.TrimLeft(tony[1].text(), " ")); lead != indent {
		t.Fatalf("Tony body indent = %d, want %d", lead, indent)
	}
}

// The timeline header time is what Ctrl+G toggles, and it is on by default so
// the chat transcript is readable without a hidden switch.
func TestTimelineTimestampsFollowCtrlG(t *testing.T) {
	a := testApp(100, 30)
	a.timeline = true
	a.showTimestamps = true
	a.route(events.Event{Kind: events.KindPeer, Agent: protocol.Austin, Peer: protocol.Tony, Text: "check the cache"})

	plain := ansiPattern.ReplaceAllString(a.buildFrame(renderNormal), "")
	if !strings.Contains(plain, "Austin → Tony · ") {
		t.Fatalf("timeline header is not stamped:\n%s", plain)
	}

	a.handleKey("ctrl-g")
	if !a.showTimestamps {
		t.Fatal("ctrl-g action is not bound to the timestamp toggle")
	}
	a.applyAction(context.Background(), inputAction{kind: actionToggleTimestamps})
	if a.showTimestamps {
		t.Fatal("ctrl-g did not toggle timestamps off")
	}
	plain = ansiPattern.ReplaceAllString(a.buildFrame(renderNormal), "")
	if !strings.Contains(plain, "Austin → Tony") || strings.Contains(plain, "Austin → Tony · ") {
		t.Fatalf("ctrl-g did not remove the timeline stamp:\n%s", plain)
	}
}

// A Tony message that fits on one line hugs the right edge like a short chat
// bubble; a message that wraps — several body lines, or one long line the
// bubble has to break — stays left-anchored at the indent so its lines share
// one margin. Austin's one-liners stay on the left either way.
func TestTimelineShortTonyMessageHugsRightEdge(t *testing.T) {
	const width = 60
	indent := timelineIndent(width)

	short := timelineEntryLines(timelineEntry{origin: protocol.Tony, entry: entry{text: "Tony connected"}}, width, false)
	if len(short) != 2 {
		t.Fatalf("short message has %d lines, want header + one body line", len(short))
	}
	body := short[1].text()
	if got := displayWidth(body); got != width {
		t.Fatalf("short body row is %d columns, want %d: %q", got, width, body)
	}
	if !strings.HasSuffix(body, "Tony connected") {
		t.Fatalf("short body does not end at the right edge: %q", body)
	}

	for name, text := range map[string]string{
		"multiline": "first\nsecond",
		"long line": strings.Repeat("word ", 40),
	} {
		lines := timelineEntryLines(timelineEntry{origin: protocol.Tony, entry: entry{text: text}}, width, false)
		if len(lines) < 3 {
			t.Fatalf("%s: got %d lines, want a wrapped body", name, len(lines))
		}
		first := lines[1].text()
		if lead := displayWidth(first) - displayWidth(strings.TrimLeft(first, " ")); lead != indent {
			t.Fatalf("%s: first body line indent = %d, want %d: %q", name, lead, indent, first)
		}
	}

	austin := timelineEntryLines(timelineEntry{origin: protocol.Austin, entry: entry{text: "done"}}, width, false)
	if got := austin[1].text(); got != "done" {
		t.Fatalf("Austin short body = %q, want left-anchored", got)
	}
}

// Every timeline header names a direction, never a bare speaker: system,
// harness, error and verdict entries name their target — a peer, or the human.
func TestTimelineHeadersAreDirectedPairs(t *testing.T) {
	a := &App{timeline: true}
	a.route(events.Event{Kind: events.KindSystem, Agent: protocol.Tony, Text: "Tony connected"})
	a.route(events.Event{Kind: events.KindSystem, Agent: protocol.Duo, Peer: protocol.Tony, Text: "sent resume wake to Tony"})
	a.route(events.Event{Kind: events.KindHarness, Agent: protocol.Duo, Text: "both idle"})
	a.route(events.Event{Kind: events.KindError, Agent: protocol.Austin, Text: "429"})
	a.route(events.Event{Kind: events.KindVerdict, Agent: protocol.Tony, Peer: protocol.Austin, Text: "gap"})

	labels := []string{a.tony[0].label, a.duo[0].label, a.duo[1].label, a.austin[0].label, a.tony[1].label, a.duo[2].label, a.duo[3].label}
	want := []string{"Tony → Human", "Duo → Tony", "Duo → Human", "Austin → Human", "Tony → Austin", "Duo → Human", "Duo → Human"}
	for i := range want {
		if labels[i] != want[i] {
			t.Fatalf("label %d = %q, want %q", i, labels[i], want[i])
		}
	}

	// A legacy entry with no label still renders as a pair, not a bare name.
	legacy := timelineEntryLines(timelineEntry{origin: protocol.Duo, entry: entry{text: "old note"}}, 60, false)
	if got := strings.TrimSpace(legacy[0].text()); got != "Duo → Human" {
		t.Fatalf("legacy header = %q, want Duo → Human", got)
	}

	// A resumed journal that recorded a bare speaker name is normalized on replay.
	restored := &App{}
	restored.restoreEntry(sessionstore.TUIEntry{Pane: "Tony", Text: "old", Label: "Tony"})
	if got := restored.tony[0].label; got != "Tony → Human" {
		t.Fatalf("restored legacy label = %q, want Tony → Human", got)
	}
}

// A header addressed to the human is the human's mail, so it is bold in the
// speaker's own hue; agent-to-agent traffic and system notices stay dim.
func TestTimelineHumanHeadersAreHighlighted(t *testing.T) {
	cases := []struct {
		label string
		want  string
	}{
		{"Austin → Human", ansiAustinBold},
		{"Tony → Human", ansiTonyBold},
		{"Duo → Human", ansiDuoBold},
		{"Human → Austin", ansiHumanDim},
		{"Austin → Tony", ansiAustinDim},
		{"Tony → Austin", ansiTonyDim},
		{"Duo → Tony", ansiDuoDim},
		{"legacy bare label", ansiHint},
	}
	for _, tc := range cases {
		lines := timelineEntryLines(timelineEntry{origin: protocol.Austin, entry: entry{label: tc.label, text: "x"}}, 60, false)
		if got := lines[0].spans[0].style; got != tc.want {
			t.Fatalf("label %q style = %q, want %q", tc.label, got, tc.want)
		}
	}

	// The stamp is appended after the target is parsed, so a stamped header keeps
	// its highlight.
	stamped := timelineEntryLines(timelineEntry{origin: protocol.Tony, entry: entry{label: "Tony → Human", at: time.Now(), text: "x"}}, 60, true)
	if got := stamped[0].spans[0].style; got != ansiTonyBold {
		t.Fatalf("stamped header style = %q, want %q", got, ansiTonyBold)
	}
}

func TestNewEnablesStampedTimeline(t *testing.T) {
	a := New(nil, nil, nil, nil, nil, nil, nil, "test", nil, nil)
	if !a.timeline {
		t.Fatal("New must enable the timeline main view")
	}
	if !a.showTimestamps {
		t.Fatal("New must stamp timeline headers by default")
	}
}
