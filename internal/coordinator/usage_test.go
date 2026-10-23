package coordinator

import (
	"testing"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/recovery"
	"github.com/atfa/duo/internal/sessionstore"
)

func usageCoordinator(t *testing.T, caps driver.Capabilities) (*Coordinator, *harness.Tracker) {
	t.Helper()
	tracker := harness.NewTracker()
	coord := New(nil, project.NewStateFor(project.ModeGoal), tracker, nil, nil)
	mgr := agent.NewManager()
	mgr.Add(newMockDriver(protocol.Austin, "measuring", caps))
	coord.SetAgents(mgr)
	return coord, tracker
}

// TestUsageReportIsRecordedUnderTheCurrentPhase is the ledger's attribution rule:
// the phase is stamped by the coordinator, which owns phases, and it is what lets a
// human see where a session's cost went.
func TestUsageReportIsRecordedUnderTheCurrentPhase(t *testing.T) {
	coord, tracker := usageCoordinator(t, driver.Capabilities{Resume: driver.ResumeClient, Usage: true})

	coord.handleUsage(protocol.Austin, protocol.Message{Usage: &protocol.Usage{
		InputTokens: 5000, OutputTokens: 900, CostUSD: 0.12,
	}})

	totals := tracker.UsageTotals(protocol.Austin)
	if totals.Turns != 1 || totals.TotalTokens != 5900 || totals.CostUSD != 0.12 {
		t.Fatalf("recorded totals = %+v", totals)
	}
	if tracker.UsageByPhase()[string(project.PhasePlan)].CostUSD != 0.12 {
		t.Fatalf("the report was not filed under the phase in force: %+v", tracker.UsageByPhase())
	}
}

// TestUsageReportFromADriverThatCannotMeasureIsIgnored: a cost is only recorded
// when the driver declares it measures one, on the same terms as the context and
// rate figures. A driver that sends figures it cannot maintain must not put a
// number in the user's cost ledger.
func TestUsageReportFromADriverThatCannotMeasureIsIgnored(t *testing.T) {
	coord, tracker := usageCoordinator(t, driver.Capabilities{Resume: driver.ResumeClient})

	coord.handleUsage(protocol.Austin, protocol.Message{Usage: &protocol.Usage{InputTokens: 5000, CostUSD: 0.12}})

	if len(tracker.UsageByAgent()) != 0 {
		t.Fatal("a driver that did not promise to measure cost must not create a ledger")
	}
	if coord.UsageLedger() != nil {
		t.Fatal("no ledger means no ledger in the snapshot, not an empty one")
	}
}

// TestUsageReportWithoutAMeasurementIsIgnored covers the message shape: a usage
// message that carries no usage is nothing, and must not be recorded as a turn.
func TestUsageReportWithoutAMeasurementIsIgnored(t *testing.T) {
	coord, tracker := usageCoordinator(t, driver.Capabilities{Resume: driver.ResumeClient, Usage: true})

	coord.handleUsage(protocol.Austin, protocol.Message{})

	if len(tracker.UsageByAgent()) != 0 {
		t.Fatal("a usage message with no usage must be ignored")
	}
}

// TestUsageLedgerReachesTheCheckpointAndResumes is the round trip the user pays
// for: the ledger is composed into the snapshot, and a coordinator that loads that
// snapshot continues from the cost already spent.
func TestUsageLedgerReachesTheCheckpointAndResumes(t *testing.T) {
	coord, tracker := usageCoordinator(t, driver.Capabilities{Resume: driver.ResumeClient, Usage: true})
	coord.handleUsage(protocol.Austin, protocol.Message{Usage: &protocol.Usage{InputTokens: 4000, OutputTokens: 1000, CostUSD: 0.2}})

	ledger := coord.UsageLedger()
	if ledger == nil {
		t.Fatal("a measured session must produce a ledger")
	}
	if ledger.Agents[protocol.Austin].CostUSD != 0.2 {
		t.Fatalf("ledger = %+v", ledger.Agents)
	}
	snap := recovery.Compose(recovery.ComposeInput{Usage: ledger})
	raw, err := saveAndLoad(t, snap)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Usage == nil || raw.Usage.Agents[protocol.Austin].CostUSD != 0.2 {
		t.Fatalf("the checkpoint lost the ledger: %+v", raw.Usage)
	}

	// A resumed session: a fresh coordinator, the persisted ledger, and the next
	// turn added on top of what was already spent.
	resumed, resumedTracker := usageCoordinator(t, driver.Capabilities{Resume: driver.ResumeClient, Usage: true})
	resumed.RestoreUsage(raw.Usage)
	resumed.handleUsage(protocol.Austin, protocol.Message{Usage: &protocol.Usage{InputTokens: 1000, OutputTokens: 250, CostUSD: 0.05}})

	totals := resumedTracker.UsageTotals(protocol.Austin)
	if totals.Turns != 2 || totals.TotalTokens != 6250 || totals.CostUSD < 0.24 || totals.CostUSD > 0.26 {
		t.Fatalf("totals after resume = %+v, want the persisted cost plus the new turn", totals)
	}
	if tracker.UsageTotals(protocol.Austin).Turns != 1 {
		t.Fatal("restoring into one coordinator must not disturb another")
	}
}

// saveAndLoad round-trips a snapshot through the encoding the store uses, so the
// test proves the ledger survives the file format and not just the Go value.
func saveAndLoad(t *testing.T, snap sessionstore.Snapshot) (sessionstore.Snapshot, error) {
	t.Helper()
	store, err := sessionstore.New(t.TempDir(), "repo-usage", "usage-test")
	if err != nil {
		return sessionstore.Snapshot{}, err
	}
	snap.SessionID = "usage-test"
	snap.RepoID = "repo-usage"
	if err := store.Save(snap); err != nil {
		return sessionstore.Snapshot{}, err
	}
	return store.Load()
}
