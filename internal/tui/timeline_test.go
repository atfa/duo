package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/protocol"
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

func TestNewEnablesStampedTimeline(t *testing.T) {
	a := New(nil, nil, nil, nil, nil, nil, nil, "test", nil, nil)
	if !a.timeline {
		t.Fatal("New must enable the timeline main view")
	}
	if !a.showTimestamps {
		t.Fatal("New must stamp timeline headers by default")
	}
}
