package sessionstore

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/protocol"
)

// TestUsageLedgerRoundTrips is the durability half of T9: a session's cost has to
// outlive the process that measured it, or the question "what did this session
// cost" can only be answered while it is running.
func TestUsageLedgerRoundTrips(t *testing.T) {
	store := newTestStore(t, "session-usage")

	snap := testSnapshot("session-usage")
	snap.Usage = &UsageLedger{
		Agents: map[protocol.AgentID]UsageTotals{
			protocol.Austin: {Turns: 6, InputTokens: 12000, OutputTokens: 2400, CacheRead: 9000, TotalTokens: 23400, CostUSD: 0.42},
			protocol.Tony:   {Turns: 2, InputTokens: 3000, OutputTokens: 600, TotalTokens: 3600, CostUSD: 0.08},
		},
		Phases: map[string]UsageTotals{
			"PLAN":    {Turns: 3, TotalTokens: 9000, CostUSD: 0.2},
			"EXECUTE": {Turns: 5, TotalTokens: 18000, CostUSD: 0.3},
		},
	}
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Usage == nil {
		t.Fatal("the cost ledger did not survive the checkpoint")
	}
	austin := got.Usage.Agents[protocol.Austin]
	if austin.Turns != 6 || austin.CacheRead != 9000 || austin.CostUSD != 0.42 {
		t.Fatalf("Austin's entry lost: %+v", austin)
	}
	if got.Usage.Phases["EXECUTE"].CostUSD != 0.3 {
		t.Fatalf("phase split lost: %+v", got.Usage.Phases)
	}

	// The session total is derived from the parts, so it cannot disagree with them.
	total := got.Usage.Total()
	if total.Turns != 8 || total.TotalTokens != 27000 || total.CostUSD != 0.5 {
		t.Fatalf("session total = %+v, want the sum of the two agents", total)
	}
}

// TestUsageLedgerAbsentIsNotWritten pins the shape of "nothing measured": a driver
// that cannot measure cost stores no ledger, so an old snapshot and a session with
// an unmeasuring driver are indistinguishable — and neither claims a cost of zero.
func TestUsageLedgerAbsentIsNotWritten(t *testing.T) {
	store := newTestStore(t, "session-usage-none")

	snap := testSnapshot("session-usage-none")
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}
	raw, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if raw.Usage != nil {
		t.Fatalf("a session that measured nothing must persist no ledger, got %+v", raw.Usage)
	}

	encoded, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"usage"`) {
		t.Error("the empty ledger must be omitted from the file, not written as a zero claim")
	}
}

// TestUsageLedgerOfAnOldSnapshotLoads: a snapshot written before the ledger existed
// must load unchanged, which is why the schema version stayed where it was.
func TestUsageLedgerOfAnOldSnapshotLoads(t *testing.T) {
	const legacy = `{
		"schemaVersion": 2,
		"sessionId": "old",
		"phase": "EXECUTE",
		"ready": {"Austin": true},
		"notes": {},
		"evidence": {},
		"worktrees": {},
		"integration": {},
		"delivery": {}
	}`
	var snap Snapshot
	if err := json.Unmarshal([]byte(legacy), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Usage != nil {
		t.Fatalf("a pre-ledger snapshot must load with no ledger, got %+v", snap.Usage)
	}
	if got := snap.Usage.Total(); got.Turns != 0 || got.CostUSD != 0 {
		t.Fatal("Total of a missing ledger must be the zero value, not a panic")
	}
}

// TestCostLineIsOneSentenceForEveryView: the cost panel, the overview and the final
// summary quote this sentence, so a human reads one phrasing of one number rather
// than three that have to be reconciled by hand.
func TestCostLineIsOneSentenceForEveryView(t *testing.T) {
	total := UsageTotals{Turns: 15, TotalTokens: 20000, CostUSD: 0.55}
	if got := total.CostLine(false); got != "Session cost: $0.55 · 20k tokens · 15 turns" {
		t.Errorf("en cost line = %q", got)
	}
	if got := total.CostLine(true); got != "会话成本：$0.55 · 20k tokens · 15 轮" {
		t.Errorf("zh cost line = %q", got)
	}
	if got := (UsageTotals{TotalTokens: 950}).CostLine(false); !strings.Contains(got, "950 tokens") {
		t.Errorf("a small token count must stay plain: %q", got)
	}
}
