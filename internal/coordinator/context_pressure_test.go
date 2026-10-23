package coordinator

import (
	"strings"
	"testing"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// warningRecorder counts the context-pressure warnings the coordinator announces.
// It drains on read: an emit is synchronous, so a background reader would race the
// assertion that follows the action.
func warningRecorder(t *testing.T, bus *events.Bus) func() int {
	t.Helper()
	ch, cancel := bus.Subscribe(256)
	t.Cleanup(cancel)

	count := 0
	return func() int {
		for {
			select {
			case event := <-ch:
				if event.Kind == events.KindSystem && strings.Contains(event.Text, "context at") {
					count++
					continue
				}
			default:
				return count
			}
		}
	}
}

func contextCoordinator(t *testing.T, percent int) (*Coordinator, *harness.Tracker, func() int) {
	t.Helper()
	bus := events.NewBus()
	tracker := harness.NewTracker()
	coord := New(nil, project.NewStateFor(project.ModeGoal), tracker, nil, bus)
	mgr := agent.NewManager()
	mgr.Add(newMockDriver(protocol.Austin, "ctx", driver.Capabilities{Resume: driver.ResumeClient, ContextUsage: true}))
	coord.SetAgents(mgr)
	coord.SetContextWarnPercent(percent)
	return coord, tracker, warningRecorder(t, bus)
}

// TestContextPressureWarnsOnceAtThreshold is the whole point of the warning: an
// agent that runs out of context mid-task loses the plan it was working from, and
// compaction arrives after the damage. A driver reports context on every provider
// call, so the warning has to be one per crossing — and a compaction that frees the
// window and a later refill is a genuinely new event, so it warns again.
func TestContextPressureWarnsOnceAtThreshold(t *testing.T) {
	coord, tracker, warnings := contextCoordinator(t, 85)
	report := func(tokens int) {
		coord.handleActivity(protocol.Austin, protocol.Message{
			Activity:      protocol.ActivityStream,
			ContextTokens: tokens, ContextWindow: 200000,
		})
	}

	report(90000) // 45%: room left, nobody is told anything
	if warnings() != 0 || tracker.Snapshot(protocol.Austin).ContextWarned {
		t.Fatalf("below the threshold the agent was warned: warnings=%d", warnings())
	}

	report(180000) // 90%: the crossing
	if warnings() != 1 || !tracker.Snapshot(protocol.Austin).ContextWarned {
		t.Fatalf("the crossing did not warn: warnings=%d", warnings())
	}

	report(185000) // 92%: the same crossing, reported again
	report(190000)
	if warnings() != 1 {
		t.Fatalf("the agent was warned %d times for one crossing, want 1", warnings())
	}

	report(100000) // 50%: compaction freed the window, so re-arm
	if tracker.Snapshot(protocol.Austin).ContextWarned {
		t.Fatal("falling below the threshold must clear the warning")
	}

	report(180000) // 90% again: a new crossing is a new warning
	if warnings() != 2 {
		t.Fatalf("a second crossing warned %d times in total, want 2", warnings())
	}
}

// TestContextPressureIsOffWhenTheThresholdIsZero covers the operator's switch: 0
// means "never tell me", and a driver that reports no window is never judged.
func TestContextPressureIsOffWhenTheThresholdIsZero(t *testing.T) {
	coord, tracker, warnings := contextCoordinator(t, 0)

	coord.handleActivity(protocol.Austin, protocol.Message{
		Activity: protocol.ActivityStream, ContextTokens: 199000, ContextWindow: 200000,
	})

	if warnings() != 0 || tracker.Snapshot(protocol.Austin).ContextWarned {
		t.Fatal("a threshold of 0 must never warn")
	}

	enabled, tracker, warnings := contextCoordinator(t, 85)
	enabled.handleActivity(protocol.Austin, protocol.Message{
		Activity: protocol.ActivityStream, ContextTokens: 190000,
	})
	if warnings() != 0 || tracker.Snapshot(protocol.Austin).ContextWarned {
		t.Fatal("a report with no window has no occupancy to judge")
	}
}

// TestContextPressureIgnoresADriverThatCannotReportContext: the figures are gated
// on the capability that promises them, so a driver that guesses its window cannot
// trigger a warning built on the guess.
func TestContextPressureIgnoresADriverThatCannotReportContext(t *testing.T) {
	bus := events.NewBus()
	tracker := harness.NewTracker()
	coord := New(nil, project.NewStateFor(project.ModeGoal), tracker, nil, bus)
	mgr := agent.NewManager()
	mgr.Add(newMockDriver(protocol.Austin, "silent", driver.Capabilities{Resume: driver.ResumeClient}))
	coord.SetAgents(mgr)
	coord.SetContextWarnPercent(85)
	warnings := warningRecorder(t, bus)

	coord.handleActivity(protocol.Austin, protocol.Message{
		Activity: protocol.ActivityStream, ContextTokens: 190000, ContextWindow: 200000,
	})

	if warnings() != 0 || tracker.Snapshot(protocol.Austin).ContextWarned {
		t.Fatal("a driver that did not promise context usage must not trigger the warning")
	}
}
