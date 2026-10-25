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
		"Step queue (2 step(s), 1 landed, current step 2 of 2):",
		"1. [x] [fast] add first.md — landed ",
		"2. [ ] [fast] add second.md",
	} {
		if !strings.Contains(live, want) {
			t.Errorf("live block missing %q:\n%s", want, live)
		}
	}
}

// TestSnapshotLiveBlockAfterTheLastStepLands pins the queue tally at the moment
// a progress report is most likely opened: the last step has landed but the
// session is still in INTEGRATE, where Snapshot.Step is len+1 by contract. The
// header's step=3/2 is fine (it is the cursor), but the queue must not claim a
// "current step" that is past the end.
func TestSnapshotLiveBlockAfterTheLastStepLands(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	ctx := context.Background()
	round := startSessionRound(t, ctx, project.ModeStep)
	austin := round.dial(t, protocol.Austin)
	tony := round.dial(t, protocol.Tony)
	round.waitConnected(t)

	austin.mustRequest(t, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetPlan, Plan: "One step"})
	austin.mustRequest(t, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetSteps, Steps: "- [ ] add only.md"})
	austin.sign(t)
	tony.sign(t)
	round.waitPhase(t, project.PhaseExecute)

	commitInWorktree(t, round.svc.View().Worktrees.Austin.Path, "only.md", "only\n", "add only.md")
	austin.sign(t)
	round.waitPhase(t, project.PhaseReview)
	tony.mustRequest(t, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetVerification, Verification: "passed", Note: "ok"})
	// Land the last step: its dual INTEGRATE sign-off delivers and ticks the
	// queue, leaving the cursor at len+1 in INTEGRATE — the state the tally
	// below must survive.
	austin.sign(t)
	tony.sign(t)
	waitUntil(t, func() bool {
		snap := round.svc.View().Project
		return len(snap.Steps) == 1 && snap.Steps[0].Done && snap.Step == len(snap.Steps)+1
	}, "the only step to land with the cursor past the end")

	live := round.svc.observerLiveContext()
	if !strings.Contains(live, "Step queue (1 step(s), 1 landed, all steps landed):") {
		t.Fatalf("live block missing the all-landed tally:\n%s", live)
	}
	if strings.Contains(live, "current step") {
		t.Fatalf("all-landed queue must not name a current step:\n%s", live)
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

// TestClipPlanNeverExceedsItsBound pins the fallback rule: a plan without
// newlines — the common goal-mode shape where the numbered items are inlined —
// is still cut at maxPlanRunes, and every cut keeps the truncation marker so a
// report cannot read a partial plan as the whole decomposition.
func TestClipPlanNeverExceedsItsBound(t *testing.T) {
	long := strings.Repeat("1) step one 2) step two 3) step three ", 300) // > 2× maxPlanRunes, one line
	got := clipPlan(long)
	if cut, ok := clipRunesMarked(long, maxPlanRunes); !ok || got != cut {
		t.Fatalf("a one-line plan must be cut at the bound with a marker, got %d runes", len([]rune(got)))
	}
	if strings.HasSuffix(got, "1)") || strings.HasSuffix(got, "1") {
		t.Fatalf("cut must not land mid-token without a marker: %q", clipRunes(got, 40))
	}
	if !strings.Contains(got, "…") {
		t.Fatalf("the truncation marker is lost:\n%q", clipRunes(got, 80))
	}

	multi := "1) first item\n2) second item\n" + strings.Repeat("filler line\n", 500) + "3) third item"
	got = clipPlan(multi)
	if len([]rune(got)) > maxPlanRunes+10 { // marker only
		t.Fatalf("a multi-line plan exceeded the bound: %d runes", len([]rune(got)))
	}
	if strings.Contains(got, "3) third item") {
		t.Fatalf("the newline fallback must drop content past the cut:\n%q", clipRunes(got, 200))
	}
	// The fallback dropped everything from the cut's line onward, so the kept
	// body ends at a line boundary — no half item, no partial filler line.
	body := strings.TrimSuffix(got, "\n…")
	if !strings.HasSuffix(body, "\n") {
		t.Fatalf("the newline fallback left a partial line:\n%q", clipRunes(got, 200))
	}
	if !strings.Contains(got, "…") {
		t.Fatalf("the truncation marker is lost after the newline fallback")
	}

	if got := clipPlan("short plan"); got != "short plan" {
		t.Fatalf("a plan under the bound must pass through unchanged, got %q", got)
	}
}
