package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
)

func TestSessionStatusReportsAStepAwaitingDelivery(t *testing.T) {
	step := func(recordHead string) sessionstore.Summary {
		return sessionstore.Summary{
			SessionID: "s",
			Dir:       t.TempDir(),
			Snapshot: sessionstore.Snapshot{
				SessionID: "s",
				Mode:      project.ModeStep,
				Phase:     string(project.PhaseIntegrate),
				Step:      2,
				Steps: project.Steps{
					{Title: "one", Done: true, Head: "landed0", DeliveryHead: "landed0"},
					{Title: "two", Head: "head-two"},
				},
				Ready: map[protocol.AgentID]bool{protocol.Austin: true, protocol.Tony: true},
				Delivery: sessionstore.Delivery{
					Status: sessionstore.DeliveryApplied, FinalHead: recordHead, AppliedHead: recordHead,
				},
			},
		}
	}

	// The record is step 1's landing and step 2 is submitted: the session is
	// waiting to deliver, which is what `duo apply` is offered.
	if got := sessionStatus(step("landed0")); got != "pending" {
		t.Fatalf("status = %q, want pending for a step submitted after the last landing", got)
	}
	// The record is step 2's own landing: nothing is left to deliver.
	if got := sessionStatus(step("head-two")); got != "delivered" {
		t.Fatalf("status = %q, want delivered for the step whose landing is recorded", got)
	}

	// The other modes keep their meaning: a dual-signed INTEGRATE is pending, and
	// a DONE session with its artifact applied is delivered.
	goal := sessionstore.Summary{
		SessionID: "g",
		Dir:       t.TempDir(),
		Snapshot: sessionstore.Snapshot{
			SessionID: "g",
			Mode:      project.ModeGoal,
			Phase:     string(project.PhaseIntegrate),
			Ready:     map[protocol.AgentID]bool{protocol.Austin: true, protocol.Tony: true},
		},
	}
	if got := sessionStatus(goal); got != "pending" {
		t.Fatalf("status = %q, want pending for a dual-signed INTEGRATE", got)
	}

	done := goal.Snapshot
	done.Phase = "DONE"
	done.Delivery = sessionstore.Delivery{Status: sessionstore.DeliveryApplied, FinalHead: "h", AppliedHead: "h"}
	if got := sessionStatus(sessionstore.Summary{SessionID: "g", Dir: t.TempDir(), Snapshot: done}); got != "delivered" {
		t.Fatalf("status = %q, want delivered for a DONE session whose artifact landed", got)
	}
}

func TestSessionIsActiveOnlyWhenLockIsContended(t *testing.T) {
	dir := t.TempDir()
	if sessionIsActive(dir) {
		t.Fatal("missing lock file reported active")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := sessionstore.New(filepath.Dir(dir), filepath.Base(dir), "s")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := store.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if !sessionIsActive(store.Dir()) {
		t.Fatal("contended lock not reported active")
	}
	if sessionIsActive(filepath.Join(dir, "not-a-directory")) {
		t.Fatal("lock open failure reported active")
	}
}

// TestSchemaLabelReportsMigration keeps the listing honest about the schema a
// session was read with: a session this Duo had to upgrade says so instead of
// looking like it was written by this binary.
func TestSchemaLabelReportsMigration(t *testing.T) {
	label := func(persisted int) string {
		return schemaLabel(sessionstore.Snapshot{PersistedSchemaVersion: persisted})
	}
	if got := label(sessionstore.SchemaVersion); got != fmt.Sprintf("%d", sessionstore.SchemaVersion) {
		t.Fatalf("current schema label = %q", got)
	}
	if got := label(sessionstore.SchemaVersion - 1); got != fmt.Sprintf("%d→%d", sessionstore.SchemaVersion-1, sessionstore.SchemaVersion) {
		t.Fatalf("migrated schema label = %q, want the upgrade it took", got)
	}
	if got := label(0); got != "-" {
		t.Fatalf("in-memory snapshot label = %q, want unknown", got)
	}
}
