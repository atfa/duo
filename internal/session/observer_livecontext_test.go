package session

import (
	"context"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// TestSnapshotLiveBlockCarriesPlanAndSteps pins the input the Progress section
// of a report is written from: the live block has to carry the shared plan text
// and the step queue with its landed/submitted markers, because "how was the
// task decomposed and how far has it come" cannot be answered from the log.
func TestSnapshotLiveBlockCarriesPlanAndSteps(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	ctx := context.Background()
	round := startSessionRound(t, ctx, project.ModeStep)
	austin := round.dial(t, protocol.Austin)
	tony := round.dial(t, protocol.Tony)
	round.waitConnected(t)

	austin.mustRequest(t, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetPlan, Plan: "Add two files, one step each"})
	austin.mustRequest(t, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetSteps, Steps: "- [ ] add first.md\n- [ ] add second.md"})
	austin.sign(t)
	tony.sign(t)
	round.waitPhase(t, project.PhaseExecute)

	// Land the first step so the queue shows one done, one open.
	austinPath := round.svc.View().Worktrees.Austin.Path
	commitInWorktree(t, austinPath, "first.md", "first\n", "add first.md")
	austin.sign(t)
	round.waitPhase(t, project.PhaseReview)
	tony.mustRequest(t, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetVerification, Verification: "passed", Note: "ok"})
	waitUntil(t, func() bool {
		snap := round.svc.View().Project
		return len(snap.Steps) == 2 && snap.Steps[0].Done && !snap.Steps[1].Done
	}, "step 1 to land")

	live := round.svc.observerLiveContext()
	for _, want := range []string{
		"mode=STEP phase=EXECUTE ",
		"step=2/2",
		"Shared plan (version 1):",
		"Add two files, one step each",
		"Step queue (2 steps, 1 landed, current step 2 of 2):",
		"1. [x] [fast] add first.md — landed ",
		"2. [ ] [fast] add second.md",
	} {
		if !strings.Contains(live, want) {
			t.Errorf("live block missing %q:\n%s", want, live)
		}
	}
}

// TestSnapshotLiveBlockCarriesPlanInGoalMode checks the Goal-mode half: with no
// step queue the live block still carries the plan text, so a report can state
// the task decomposition without inventing one.
func TestSnapshotLiveBlockCarriesPlanInGoalMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	ctx := context.Background()
	round := startSessionRound(t, ctx, project.ModeGoal)
	austin := round.dial(t, protocol.Austin)
	tony := round.dial(t, protocol.Tony)
	round.waitConnected(t)

	austin.mustRequest(t, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetPlan, Plan: "Redesign the scheduler and keep tests green"})
	austin.sign(t)
	tony.sign(t)
	round.waitPhase(t, project.PhaseExecute)

	live := round.svc.observerLiveContext()
	if !strings.Contains(live, "Shared plan (version 1):\nRedesign the scheduler and keep tests green\n") {
		t.Fatalf("live block missing the goal-mode plan:\n%s", live)
	}
	if strings.Contains(live, "Step queue") {
		t.Fatalf("goal mode must not render a step queue:\n%s", live)
	}
}
