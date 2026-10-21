package gotui

import (
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/protocol"
)

// An observation is advice for the human. It belongs in the timeline under Duo,
// marked as an observation, and styled as neither an error nor a warning: the
// observer owns no workflow state, so the interface must never make its text
// look like a verdict or a failure.
func TestObserverObservationRendersAsDuoAdvice(t *testing.T) {
	fake := newFakeSession()
	app := New(fake)

	app.route(events.Event{
		Time:  time.Now(),
		Kind:  events.KindObserver,
		Agent: protocol.Duo,
		Text:  "Austin claimed a green run without running the tests.",
	})

	recorded := fake.snapshotRecorded()
	if len(recorded) != 1 {
		t.Fatalf("recorded %d entries, want exactly 1: %+v", len(recorded), recorded)
	}
	entry := recorded[0]
	if entry.Pane != string(protocol.Duo) {
		t.Fatalf("observation pane = %q, want %q", entry.Pane, protocol.Duo)
	}
	if entry.Error || entry.Warning {
		t.Fatalf("observation styled as error/warning: %+v", entry)
	}
	if !strings.HasPrefix(entry.Text, "Observer: ") {
		t.Fatalf("observation text = %q, want an %q prefix", entry.Text, "Observer: ")
	}
	if !strings.Contains(entry.Text, "without running the tests") {
		t.Fatalf("observation text lost the model's words: %q", entry.Text)
	}
}
