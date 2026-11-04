package project

import (
	"testing"
	"time"

	"github.com/atfa/duo/internal/protocol"
)

func TestLoopEventsSurviveRestoreUntilAcknowledged(t *testing.T) {
	s := NewStateFor(ModeLoop)
	deadline := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if _, err := s.LoopWait(protocol.Tony, deadline, "check", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.LoopSend(protocol.Austin, "hello"); err != nil {
		t.Fatal(err)
	}
	restored := NewStateFor(ModeGoal)
	if err := restored.Restore(s.Snapshot()); err != nil {
		t.Fatal(err)
	}
	events, err := restored.LoopWait(protocol.Tony, time.Time{}, "check", "")
	if err != nil || len(events) != 1 || events[0].Text != "hello" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	if got := restored.Snapshot().Loop.Waiting[protocol.Tony].Until; !got.Equal(deadline) {
		t.Fatalf("deadline lost: %v", got)
	}
	// A lost response replays the event, including its original ID.
	again, err := restored.LoopWait(protocol.Tony, time.Time{}, "check", "")
	if err != nil || len(again) != 1 || again[0].ID != events[0].ID {
		t.Fatalf("replay=%+v err=%v", again, err)
	}
	if _, err = restored.LoopWait(protocol.Tony, deadline, "check", events[0].ID); err != nil {
		t.Fatal(err)
	}
	restored.LoopTimers(deadline)
	timers := restored.Snapshot().Loop.Events[protocol.Tony]
	if len(timers) != 1 || timers[0].From != protocol.Duo {
		t.Fatalf("timer=%+v", timers)
	}
	restored.LoopTimers(deadline.Add(time.Hour))
	if len(restored.Snapshot().Loop.Events[protocol.Tony]) != 1 {
		t.Fatal("timer fired twice")
	}
	if done, err := restored.LoopFinish(protocol.Austin, "done"); err != nil || done {
		t.Fatalf("first finish %v %v", done, err)
	}
	if err := restored.LoopSend(protocol.Austin, "more work"); err != nil {
		t.Fatal(err)
	}
	if done, err := restored.LoopFinish(protocol.Tony, "done"); err != nil || done {
		t.Fatalf("stale approval %v %v", done, err)
	}
	if done, err := restored.LoopFinish(protocol.Austin, "done"); err != nil || !done || restored.Snapshot().Phase != PhaseDone {
		t.Fatalf("finish %v %v", done, err)
	}
}
