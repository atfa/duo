package coordinator

import (
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
)

// handleUsage records the cost a driver measured for the work it just did. The
// report is gated on the driver's Usage capability, on the same terms as the
// context and rate figures: a cost that cannot be attributed to a driver that
// measures it is a number nobody can check.
//
// The ledger rides the existing checkpoint cadence — the snapshot is composed on
// every project mutation — so a report is durable as soon as the session next
// writes state, and a crash between the two costs at most the last turn.
func (c *Coordinator) handleUsage(agent protocol.AgentID, message protocol.Message) {
	if message.Usage == nil || c.tracker == nil || !c.capabilities(agent).Usage {
		return
	}
	c.tracker.AddUsage(agent, string(c.project.Snapshot().Phase), *message.Usage)
}

// UsageLedger is the session's cost book in the shape the snapshot stores. It
// returns nil when nothing has been measured, so a session whose drivers cannot
// measure cost persists no ledger at all rather than one that reads as a cost of
// zero.
func (c *Coordinator) UsageLedger() *sessionstore.UsageLedger {
	if c.tracker == nil {
		return nil
	}
	byAgent := c.tracker.UsageByAgent()
	if len(byAgent) == 0 {
		return nil
	}
	byPhase := c.tracker.UsageByPhase()
	ledger := &sessionstore.UsageLedger{
		Agents: make(map[protocol.AgentID]sessionstore.UsageTotals, len(byAgent)),
		Phases: make(map[string]sessionstore.UsageTotals, len(byPhase)),
	}
	for agent, totals := range byAgent {
		ledger.Agents[agent] = storedUsage(totals)
	}
	for phase, totals := range byPhase {
		ledger.Phases[phase] = storedUsage(totals)
	}
	return ledger
}

// RestoreUsage seeds the ledger from a persisted one, so a resumed session keeps
// the cost of the work it already did. Reports that arrive after the resume are
// added on top of it.
func (c *Coordinator) RestoreUsage(ledger *sessionstore.UsageLedger) {
	if c.tracker == nil || ledger == nil {
		return
	}
	agents := make(map[protocol.AgentID]harness.UsageTotals, len(ledger.Agents))
	for agent, totals := range ledger.Agents {
		agents[agent] = liveUsage(totals)
	}
	phases := make(map[string]harness.UsageTotals, len(ledger.Phases))
	for phase, totals := range ledger.Phases {
		phases[phase] = liveUsage(totals)
	}
	c.tracker.RestoreUsage(agents, phases)
}

// storedUsage and liveUsage convert between the running totals and the persisted
// ones. The two shapes are deliberately separate: the harness must not import the
// store, so the coordinator that owns both ends does the converting.
func storedUsage(u harness.UsageTotals) sessionstore.UsageTotals {
	return sessionstore.UsageTotals{
		Turns:        u.Turns,
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		CacheRead:    u.CacheRead,
		CacheWrite:   u.CacheWrite,
		TotalTokens:  u.TotalTokens,
		CostUSD:      u.CostUSD,
	}
}

func liveUsage(u sessionstore.UsageTotals) harness.UsageTotals {
	return harness.UsageTotals{
		Turns:        u.Turns,
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		CacheRead:    u.CacheRead,
		CacheWrite:   u.CacheWrite,
		TotalTokens:  u.TotalTokens,
		CostUSD:      u.CostUSD,
	}
}
