package gotui

import (
	"testing"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
)

// The final report is marked by the coordinator, not by what Austin wrote: the
// timeline must badge exactly the message Duo asked for, so the flag travels on
// the event instead of being guessed from the text.
func TestAssistantEventSummaryFlagMarksTheReport(t *testing.T) {
	fake := newFakeSession()
	app := New(fake)
	// New already announced the startup state; the report is what comes after it.
	before := len(app.entries)

	app.route(events.Event{Time: time.Now(), Kind: events.KindAssistant, Agent: protocol.Austin,
		Text: "Delivered the renderer; every gate passes.", Summary: true})
	app.route(events.Event{Time: time.Now(), Kind: events.KindAssistant, Agent: protocol.Austin,
		Text: "just a normal reply"})

	if got := len(app.entries) - before; got != 2 {
		t.Fatalf("timeline gained %d entries, want 2", got)
	}
	if !app.entries[before].summary {
		t.Fatal("the report Duo asked for was not marked as the final summary")
	}
	if app.entries[before+1].summary {
		t.Fatal("an ordinary reply inherited the final-summary mark")
	}
}

// A second frontend records the very line the terminal drew. The terminal must
// not draw that entry event again, or every conversation line appears twice.
func TestTimelineEntryEventIsNotDrawnASecondTime(t *testing.T) {
	fake := newFakeSession()
	app := New(fake)
	before := len(app.entries)

	app.route(events.Event{Time: time.Now(), Kind: events.KindTimeline, Agent: protocol.Tony, Text: "tests now pass"})

	if got := len(app.entries) - before; got != 0 {
		t.Fatalf("timeline gained %d entries from a timeline entry event, want 0", got)
	}
	if len(fake.recorded) != 0 {
		t.Fatalf("the interface recorded %d entries it did not draw, want 0", len(fake.recorded))
	}
}

// A reopened session reads the mark back from the log, so the badge survives the
// interface being restarted.
func TestRestoredEntryKeepsTheSummaryMark(t *testing.T) {
	fake := newFakeSession()
	app := New(fake)
	before := len(app.entries)

	app.restoreEntry(sessionstore.TUIEntry{Text: "Delivered the renderer.", Pane: string(protocol.Austin), Summary: true})
	app.restoreEntry(sessionstore.TUIEntry{Text: "just a normal reply", Pane: string(protocol.Austin)})

	if got := len(app.entries) - before; got != 2 {
		t.Fatalf("timeline gained %d restored entries, want 2", got)
	}
	if !app.entries[before].summary {
		t.Fatal("the restored report lost its summary mark")
	}
	if app.entries[before+1].summary {
		t.Fatal("a restored ordinary reply was marked as the report")
	}
}
