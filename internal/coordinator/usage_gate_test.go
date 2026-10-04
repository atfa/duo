package coordinator

import (
	"testing"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// TestUsageFiguresAreGatedOnTheCapability covers the promise contextUsage and
// tokenRate actually make. Core used to record whatever an activity message
// carried, so a driver reporting a window it could not maintain put a number on
// screen that the operator had no way to distrust — which is precisely what a
// capability exists to prevent. Declaring neither leaves the header as it was.
func TestUsageFiguresAreGatedOnTheCapability(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps driver.Capabilities
		want bool
	}{
		{"promised", driver.Capabilities{Resume: driver.ResumeClient, ContextUsage: true, TokenRate: true}, true},
		{"context only", driver.Capabilities{Resume: driver.ResumeClient, ContextUsage: true}, true},
		{"neither promised", driver.Capabilities{Resume: driver.ResumeClient}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			coord := New(nil, project.NewStateFor(project.ModeFast), harness.NewTracker(), nil, nil)
			mgr := agent.NewManager()
			mgr.Add(newMockDriver(protocol.Austin, "gated", tc.caps))
			coord.SetAgents(mgr)

			coord.handleActivity(protocol.Austin, protocol.Message{
				ContextTokens: 45000, ContextWindow: 200000, TokensPerSecond: 30,
			})

			rt := coord.tracker.Snapshot(protocol.Austin)
			contextShown := rt.ContextWindow > 0
			rateShown := rt.TokensPerSecond > 0
			wantContext := tc.want && tc.caps.ContextUsage
			wantRate := tc.want && tc.caps.TokenRate
			if contextShown != wantContext {
				t.Errorf("context figure shown = %v, want %v", contextShown, wantContext)
			}
			if rateShown != wantRate {
				t.Errorf("rate shown = %v, want %v", rateShown, wantRate)
			}
		})
	}
}

// TestAnUnknownDriverGetsNoUsageFigure is the fail-closed case the zero
// Capabilities set has to cover: with no driver registered at all, the most
// restricted path is the one taken rather than the most capable.
func TestAnUnknownDriverGetsNoUsageFigure(t *testing.T) {
	coord := New(nil, project.NewStateFor(project.ModeFast), harness.NewTracker(), nil, nil)
	coord.handleActivity(protocol.Austin, protocol.Message{ContextTokens: 45000, ContextWindow: 200000, TokensPerSecond: 30})

	rt := coord.tracker.Snapshot(protocol.Austin)
	if rt.ContextWindow > 0 || rt.TokensPerSecond > 0 {
		t.Errorf("a driver Duo knows nothing about showed usage: ctx=%d rate=%v", rt.ContextWindow, rt.TokensPerSecond)
	}
}
