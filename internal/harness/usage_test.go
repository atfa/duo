package harness

import (
	"testing"

	"github.com/atfa/duo/internal/protocol"
)

// TestUsageAggregatesPerAgentAndPhase is the ledger's whole point: a human asks
// what a session cost, and the answer has to say who spent it and during which
// phase. A single total answers neither question.
func TestUsageAggregatesPerAgentAndPhase(t *testing.T) {
	tracker := NewTracker()
	tracker.AddUsage(protocol.Austin, "PLAN", protocol.Usage{InputTokens: 1000, OutputTokens: 200, CostUSD: 0.03})
	tracker.AddUsage(protocol.Austin, "PLAN", protocol.Usage{InputTokens: 500, OutputTokens: 100, CostUSD: 0.01})
	tracker.AddUsage(protocol.Austin, "EXECUTE", protocol.Usage{InputTokens: 2000, OutputTokens: 400, CostUSD: 0.06})
	tracker.AddUsage(protocol.Tony, "REVIEW", protocol.Usage{InputTokens: 300, OutputTokens: 50, CostUSD: 0.005})

	austin := tracker.UsageTotals(protocol.Austin)
	if austin.Turns != 3 || austin.InputTokens != 3500 || austin.OutputTokens != 700 || austin.TotalTokens != 4200 {
		t.Fatalf("Austin's totals = %+v", austin)
	}
	if austin.CostUSD < 0.099 || austin.CostUSD > 0.101 {
		t.Fatalf("Austin's cost = %v, want 0.1", austin.CostUSD)
	}

	phases := tracker.UsageByPhase()
	if phases["PLAN"].Turns != 2 || phases["PLAN"].TotalTokens != 1800 {
		t.Fatalf("PLAN totals = %+v", phases["PLAN"])
	}
	if phases["EXECUTE"].TotalTokens != 2400 || phases["REVIEW"].TotalTokens != 350 {
		t.Fatalf("phase totals = %+v", phases)
	}
}

// TestUsageTurnsComeFromTheReport pins the field's meaning: a report says how many
// turns it covers, so a driver that can only read usage in aggregate at exit does
// not reduce a session's turn count to 1.
func TestUsageTurnsComeFromTheReport(t *testing.T) {
	tracker := NewTracker()
	tracker.AddUsage(protocol.Austin, "EXECUTE", protocol.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: 0.01})
	tracker.AddUsage(protocol.Austin, "EXECUTE", protocol.Usage{Turns: 7, InputTokens: 70, OutputTokens: 35, CostUSD: 0.07})

	totals := tracker.UsageTotals(protocol.Austin)
	if totals.Turns != 8 {
		t.Fatalf("turns = %d, want 8 (one per-turn report plus one covering seven)", totals.Turns)
	}
}

// TestUsageTotalIsDerivedWhenTheDriverHasNone: providers report the parts, and a
// total nobody measured is worse than a total summed from what was measured.
func TestUsageTotalIsDerivedWhenTheDriverHasNone(t *testing.T) {
	tracker := NewTracker()
	tracker.AddUsage(protocol.Austin, "PLAN", protocol.Usage{InputTokens: 100, OutputTokens: 20, CacheRead: 40, CacheWrite: 10})
	if got := tracker.UsageTotals(protocol.Austin).TotalTokens; got != 170 {
		t.Fatalf("total tokens = %d, want the sum of the reported parts", got)
	}
}

// TestUnmeasuredUsageReportIsDropped keeps a turn count from rising while nothing
// else moves: a report with no tokens and no cost is not a measurement, and a
// session that stores no ledger makes no cost claim at all.
func TestUnmeasuredUsageReportIsDropped(t *testing.T) {
	tracker := NewTracker()
	tracker.AddUsage(protocol.Austin, "PLAN", protocol.Usage{Turns: 3})
	if len(tracker.UsageByAgent()) != 0 {
		t.Fatal("an unmeasured report must not create a ledger entry")
	}
	if !tracker.UsageTotals(protocol.Austin).Empty() {
		t.Fatal("Austin's totals must stay empty")
	}
}

// TestRestoreUsageSeedsTheLedger is the resume contract: the cost of work already
// done survives, and reports that arrive afterwards add on top of it.
func TestRestoreUsageSeedsTheLedger(t *testing.T) {
	tracker := NewTracker()
	tracker.RestoreUsage(
		map[protocol.AgentID]UsageTotals{protocol.Austin: {Turns: 4, InputTokens: 400, OutputTokens: 80, TotalTokens: 480, CostUSD: 0.4}},
		map[string]UsageTotals{"PLAN": {Turns: 4, TotalTokens: 480, CostUSD: 0.4}},
	)
	tracker.AddUsage(protocol.Austin, "EXECUTE", protocol.Usage{InputTokens: 100, OutputTokens: 20, CostUSD: 0.1})

	totals := tracker.UsageTotals(protocol.Austin)
	if totals.Turns != 5 || totals.TotalTokens != 600 || totals.CostUSD < 0.49 || totals.CostUSD > 0.51 {
		t.Fatalf("totals after resume = %+v, want the restored figures plus the new turn", totals)
	}
	if tracker.UsageByPhase()["PLAN"].Turns != 4 {
		t.Fatal("the restored phase split must survive")
	}
}
