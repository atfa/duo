package session

import (
	"strings"
	"testing"

	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
)

// TestRecentActivityFreshSessionShowsRecordedLines proves the ring is filled
// live, not only on resume: a fresh session records entries and RecentActivity
// returns them oldest first. This is the snapshot half of the remote Activity
// tab fix, which previously read a field only a resume ever filled.
func TestRecentActivityFreshSessionShowsRecordedLines(t *testing.T) {
	svc := New(Options{Mode: "fast"})
	for i := 0; i < 3; i++ {
		svc.RecordEntry(sessionstore.TUIEntry{Pane: string(protocol.Austin), Text: strings.Repeat("line ", i+1)})
	}
	got := svc.RecentActivity(10)
	if len(got) != 3 {
		t.Fatalf("RecentActivity returned %d entries, want 3", len(got))
	}
	if !strings.HasPrefix(got[0].Text, "line ") || len(got[0].Text) >= len(got[2].Text) {
		t.Errorf("entries are not oldest first: first %q, last %q", got[0].Text, got[2].Text)
	}
	if limited := svc.RecentActivity(2); len(limited) != 2 || len(limited[1].Text) != len(got[2].Text) {
		t.Errorf("RecentActivity(2) did not keep the newest entries: %+v", limited)
	}
}

// TestRecentActivityRingBoundAndIsolation proves the ring never grows past its
// cap and that the returned slice is a copy the caller cannot mutate.
func TestRecentActivityRingBoundAndIsolation(t *testing.T) {
	svc := New(Options{Mode: "fast"})
	for i := 0; i < maxRecentActivity+10; i++ {
		svc.RecordEntry(sessionstore.TUIEntry{Pane: string(protocol.Tony), Text: strings.Repeat("x", i+1)})
	}
	got := svc.RecentActivity(0)
	if len(got) != maxRecentActivity {
		t.Fatalf("ring kept %d entries, want %d", len(got), maxRecentActivity)
	}
	n := len(got[len(got)-1].Text)
	if n != maxRecentActivity+10 {
		t.Errorf("ring kept the wrong end: newest text length %d", n)
	}
	got[0].Text = "mutated"
	again := svc.RecentActivity(0)
	if again[0].Text == "mutated" {
		t.Error("RecentActivity returned the ring itself, not a copy")
	}
}

// TestRecordEntrySurvivesAClosedSession guards the regression the ring fix
// could introduce: RecordEntry appends to the ring under recentMu, so a call
// after Close (a frontend writing a final line) must not panic.
func TestRecordEntrySurvivesAClosedSession(t *testing.T) {
	svc := New(Options{Mode: "fast"})
	svc.RecordEntry(sessionstore.TUIEntry{Pane: string(protocol.Austin), Text: "before close"})
	svc.Close()
	svc.RecordEntry(sessionstore.TUIEntry{Pane: string(protocol.Austin), Text: "after close"})
	if got := svc.RecentActivity(10); len(got) != 2 {
		t.Fatalf("RecentActivity after close returned %d entries, want 2", len(got))
	}
}
