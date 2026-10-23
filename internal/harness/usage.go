package harness

import (
	"github.com/atfa/duo/internal/protocol"
)

// UsageTotals is the running sum of measured token and cost usage. The counters
// are additive by construction: a report is folded in, never replaced, because
// the question a human asks at the end of a session is "what did this cost", and
// a figure that can go down is not an answer.
//
// Turns counts completed turns as the driver reported them: a per-turn report adds
// one, an aggregate report adds the number of turns behind its figures.
type UsageTotals struct {
	Turns        int
	InputTokens  int
	OutputTokens int
	CacheRead    int
	CacheWrite   int
	TotalTokens  int
	CostUSD      float64
}

// Empty reports whether nothing has been measured. A ledger with no entries is
// left out of the snapshot entirely, so an old session and a driver that cannot
// measure cost look the same on disk: no ledger, no claim.
func (u UsageTotals) Empty() bool {
	return u.Turns == 0 && u.TotalTokens == 0 && u.CostUSD == 0
}

// addReport folds one measured report into the running totals, deriving the token
// total when the driver did not send one. Providers report input, output and cache
// splits; the total is what the cost panel needs, and summing the parts is the only
// honest way to get it from a driver that has no total of its own.
//
// Turns is added, not counted: the report says how many turns it covers, so a
// driver that can only read usage in aggregate at exit does not reduce a session's
// turn count to 1.
func (u *UsageTotals) addReport(r protocol.Usage) {
	turns := r.Turns
	if turns <= 0 {
		turns = 1
	}
	total := r.TotalTokens
	if total == 0 {
		total = r.InputTokens + r.OutputTokens + r.CacheRead + r.CacheWrite
	}
	u.Turns += turns
	u.InputTokens += r.InputTokens
	u.OutputTokens += r.OutputTokens
	u.CacheRead += r.CacheRead
	u.CacheWrite += r.CacheWrite
	u.TotalTokens += total
	u.CostUSD += r.CostUSD
}

// usageLedger is the tracker's cost book: per agent, and per phase. The phase
// split is what lets a human see where a session's money went, and the phase
// label is the coordinator's, because phases are the coordinator's business.
type usageLedger struct {
	agents map[protocol.AgentID]UsageTotals
	phases map[string]UsageTotals
}

func newUsageLedger() *usageLedger {
	return &usageLedger{
		agents: map[protocol.AgentID]UsageTotals{},
		phases: map[string]UsageTotals{},
	}
}

func (l *usageLedger) add(agent protocol.AgentID, phase string, r protocol.Usage) {
	agentTotals := l.agents[agent]
	agentTotals.addReport(r)
	l.agents[agent] = agentTotals

	phaseTotals := l.phases[phase]
	phaseTotals.addReport(r)
	l.phases[phase] = phaseTotals
}

// AddUsage records one measured report for an agent under the phase the
// coordinator stamped it with. A report with no tokens and no cost is not a
// measurement, so it is dropped: a turn count rising while nothing else moves would
// claim a cost of zero for work that was never priced.
func (t *Tracker) AddUsage(agent protocol.AgentID, phase string, r protocol.Usage) {
	if r.InputTokens <= 0 && r.OutputTokens <= 0 && r.TotalTokens <= 0 && r.CostUSD <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.usage == nil {
		t.usage = newUsageLedger()
	}
	t.usage.add(agent, phase, r)
}

// UsageTotals returns what has been measured for one agent.
func (t *Tracker) UsageTotals(agent protocol.AgentID) UsageTotals {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.usage == nil {
		return UsageTotals{}
	}
	return t.usage.agents[agent]
}

// UsageByAgent returns the per-agent ledger, copied so a caller can render it
// without holding the tracker's lock.
func (t *Tracker) UsageByAgent() map[protocol.AgentID]UsageTotals {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.usage == nil {
		return map[protocol.AgentID]UsageTotals{}
	}
	out := make(map[protocol.AgentID]UsageTotals, len(t.usage.agents))
	for k, v := range t.usage.agents {
		out[k] = v
	}
	return out
}

// UsageByPhase returns the per-phase ledger, copied.
func (t *Tracker) UsageByPhase() map[string]UsageTotals {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.usage == nil {
		return map[string]UsageTotals{}
	}
	out := make(map[string]UsageTotals, len(t.usage.phases))
	for k, v := range t.usage.phases {
		out[k] = v
	}
	return out
}

// RestoreUsage seeds the ledger from a persisted one, so a resumed session keeps
// the cost of the work it already did. Reports that arrive after the resume are
// added on top.
func (t *Tracker) RestoreUsage(agents map[protocol.AgentID]UsageTotals, phases map[string]UsageTotals) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.usage == nil {
		t.usage = newUsageLedger()
	}
	for k, v := range agents {
		t.usage.agents[k] = v
	}
	for k, v := range phases {
		t.usage.phases[k] = v
	}
}
