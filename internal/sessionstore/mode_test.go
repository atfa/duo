package sessionstore

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// TestLegacySnapshotWithoutModeLoadsAsGoal is the compatibility guarantee for
// every state.json written before modes existed: no mode key at all, so the
// session must remain Goal and keep its existing delivery rules.
func TestLegacySnapshotWithoutModeLoadsAsGoal(t *testing.T) {
	raw := `{"schemaVersion":1,"sessionId":"legacy","phase":"INTEGRATE","ready":{"Austin":true,"Tony":true}}`
	var snap Snapshot
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		t.Fatal(err)
	}
	if got := snap.EffectiveMode(); got != project.ModeGoal {
		t.Fatalf("legacy mode = %q, want goal", got)
	}
	if snap.Verification != nil {
		t.Fatalf("a legacy snapshot must have no verification, got %+v", snap.Verification)
	}
	if !snap.NeedsDelivery() {
		t.Fatal("a legacy dual-signed INTEGRATE must still need delivery")
	}
}

func TestGoalNeedsDeliveryIgnoresFastPath(t *testing.T) {
	snap := testSnapshot("goal")
	snap.Mode = project.ModeGoal
	snap.Phase = "VERIFY"
	snap.Verification = &Verification{Status: string(project.VerificationPassed), Head: "h"}
	if snap.NeedsDelivery() {
		t.Fatal("Goal mode must not use the Fast VERIFY delivery path")
	}

	snap.Phase = "INTEGRATE"
	snap.Ready = map[protocol.AgentID]bool{protocol.Austin: true}
	if snap.NeedsDelivery() {
		t.Fatal("a single INTEGRATE signature must not trigger delivery")
	}
	snap.Ready[protocol.Tony] = true
	if !snap.NeedsDelivery() {
		t.Fatal("a dual-signed INTEGRATE must trigger delivery")
	}
}

func TestFastNeedsDeliveryRequiresPassedVerification(t *testing.T) {
	base := func() Snapshot {
		return Snapshot{SchemaVersion: SchemaVersion, Mode: project.ModeFast, Phase: string(project.PhaseVerify)}
	}

	snap := base()
	if snap.NeedsDelivery() {
		t.Fatal("VERIFY without a verdict must not deliver")
	}
	snap.Verification = &Verification{Status: string(project.VerificationIssueFound), Head: "h"}
	if snap.NeedsDelivery() {
		t.Fatal("VERIFY with issue_found must not deliver")
	}
	snap.Verification = &Verification{Status: string(project.VerificationPassed), Head: "h"}
	if !snap.NeedsDelivery() {
		t.Fatal("VERIFY with a passed verification must deliver")
	}
	snap.Delivery = Delivery{Status: DeliveryApplied}
	if snap.NeedsDelivery() {
		t.Fatal("an applied delivery must never be re-run")
	}

	// RUNNING is never deliverable, even with a stale pass recorded.
	snap.Delivery = Delivery{}
	snap.Phase = string(project.PhaseRunning)
	if snap.NeedsDelivery() {
		t.Fatal("RUNNING must never deliver")
	}
}

func TestVerificationRoundTripsAndOmittedForGoal(t *testing.T) {
	goal := testSnapshot("goal")
	goal.Mode = project.ModeGoal
	raw, err := json.Marshal(goal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "verification") {
		t.Fatalf("a Goal snapshot must not carry verification noise: %s", raw)
	}
	if !strings.Contains(string(raw), `"mode":"goal"`) {
		t.Fatalf("a Goal snapshot must record its mode: %s", raw)
	}

	fast := testSnapshot("fast")
	fast.Mode = project.ModeFast
	fast.Phase = string(project.PhaseVerify)
	fast.Verification = &Verification{Status: string(project.VerificationPassed), Head: "abc123", Note: "checked"}
	raw, err = json.Marshal(fast)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"verification":{"status":"passed","head":"abc123","note":"checked"}`) {
		t.Fatalf("fast snapshot JSON = %s", raw)
	}

	var back Snapshot
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	got := back.VerificationResult()
	if got.Status != project.VerificationPassed || got.Head != "abc123" || got.Note != "checked" {
		t.Fatalf("verification round trip = %+v", got)
	}
	if !got.Passed("abc123") || got.Passed("other") {
		t.Fatalf("a restored pass must stay bound to its exact HEAD: %+v", got)
	}
}
