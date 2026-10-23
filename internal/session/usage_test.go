package session

import (
	"testing"

	"github.com/atfa/duo/internal/coordinator"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// TestUsageLedgerReachesTheView is the contract the cost panel builds on: the
// frontend reads the ledger from the view and never accumulates figures of its
// own, so a dashboard, a CLI listing and a resumed session all report one number.
func TestUsageLedgerReachesTheView(t *testing.T) {
	tracker := harness.NewTracker()
	tracker.AddUsage(protocol.Austin, "EXECUTE", protocol.Usage{InputTokens: 1000, OutputTokens: 200, CostUSD: 0.02})
	coord := coordinator.New(nil, project.NewStateFor(project.ModeGoal), tracker, nil, nil)

	view := (&Service{coord: coord}).view()
	if view.Usage == nil {
		t.Fatal("a session with measured usage must expose a ledger")
	}
	if got := view.Usage.Agents[protocol.Austin]; got.CostUSD != 0.02 || got.TotalTokens != 1200 {
		t.Fatalf("view ledger = %+v", got)
	}
	if got := view.Usage.Total(); got.CostUSD != 0.02 {
		t.Fatalf("view total = %+v", got)
	}
}

// TestViewCarriesTheContextThresholdSoTheInterfaceDrawsTheNumberCoreJudged: the
// threshold is resolved once, in configuration, and reaches the frontend as a
// number. A sidebar that guessed its own constant would mark red at a point the
// agent was never warned at.
func TestViewCarriesTheContextThreshold(t *testing.T) {
	if got := (&Service{opts: Options{}}).view().ContextWarnPercent; got != 0 {
		t.Fatalf("an option set that says nothing must not invent a threshold, got %d", got)
	}
	if got := (&Service{opts: Options{ContextWarnPercent: 85}}).view().ContextWarnPercent; got != 85 {
		t.Fatalf("View.ContextWarnPercent = %d, want 85", got)
	}
}

// TestViewHasNoUsageBeforeAnythingIsMeasured: no measurement is not a cost of zero.
func TestViewHasNoUsageBeforeAnythingIsMeasured(t *testing.T) {
	coord := coordinator.New(nil, project.NewStateFor(project.ModeGoal), harness.NewTracker(), nil, nil)
	if got := (&Service{coord: coord}).view().Usage; got != nil {
		t.Fatalf("an unmeasured session must expose no ledger, got %+v", got)
	}
	if got := (&Service{}).view().Usage; got != nil {
		t.Fatal("a session with no coordinator has no cost to report")
	}
}
