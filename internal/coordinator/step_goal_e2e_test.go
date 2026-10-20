package coordinator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// TestStepModeRunsGoalAndFastStepsInOneQueue is the end-to-end regression for
// the Step meta-workflow: a Goal-policy step has both agents execute and
// cross-sign, Duo merges Tony's branch, lands it, and loops back to a normal
// Fast-policy final step. A queue that ran only one policy would miss the
// transition where the previous mode's assumptions leak into the next step.
func TestStepModeRunsGoalAndFastStepsInOneQueue(t *testing.T) {
	ctx := context.Background()

	runtime := startE2EWithMode(t, ctx, project.ModeStep)
	repo, set, state, server := runtime.repo, runtime.set, runtime.state, runtime.server

	austin := runtime.dialAgent(t, protocol.Austin)
	tony := runtime.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool { return server.IsConnected(protocol.Austin) && server.IsConnected(protocol.Tony) }, "both agents to connect")

	request(t, austin, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetPlan, Plan: "one shared step, then one fast step"})
	request(t, austin, protocol.Message{
		Version: protocol.Version,
		Type:    protocol.MsgSetSteps,
		Steps:   "- [ ] [goal] build the shared feature\n- [ ] [fast] add the final file",
	})
	sign(t, austin)
	sign(t, tony)
	waitPhase(t, state, project.PhaseExecute)

	if snap := state.Snapshot(); snap.Step != 1 || snap.Steps[0].EffectivePolicy() != project.ModeGoal {
		t.Fatalf("first step = %+v at cursor %d, want a GOAL step", snap.Steps, snap.Step)
	}

	// Both agents are authors of the Goal step, each committing in their own
	// worktree. Both EXECUTE signatures are required before cross-review.
	writeAndCommit(t, set.Austin.Path, "austin-goal.txt", "austin\n", "austin goal work")
	writeAndCommit(t, set.Tony.Path, "tony-goal.txt", "tony\n", "tony goal work")
	sign(t, austin)
	if phase := state.Snapshot().Phase; phase != project.PhaseExecute {
		t.Fatalf("one EXECUTE signature advanced the Goal step to %s", phase)
	}
	sign(t, tony)
	waitPhase(t, state, project.PhaseReview)

	// Cross-review, then Duo merges Tony's branch into Austin's for INTEGRATE.
	sign(t, austin)
	sign(t, tony)
	waitPhase(t, state, project.PhaseIntegrate)
	waitFor(t, func() bool {
		data, err := os.ReadFile(filepath.Join(set.Austin.Path, "tony-goal.txt"))
		return err == nil && string(data) == "tony\n"
	}, "Tony's branch to be merged into Austin's worktree")

	// A non-final Goal step lands on the pair of INTEGRATE signatures and the
	// cursor advances to the Fast final step.
	sign(t, austin)
	sign(t, tony)
	waitFor(t, func() bool {
		snap := state.Snapshot()
		return snap.Step == 2 && snap.Steps[0].Done
	}, "the GOAL step to land and the queue to advance")
	waitPhase(t, state, project.PhaseExecute)
	if snap := state.Snapshot(); snap.Steps[1].EffectivePolicy() != project.ModeFast {
		t.Fatalf("second step policy = %s, want FAST", snap.Steps[1].EffectivePolicy())
	}
	for _, name := range []string{"austin-goal.txt", "tony-goal.txt"} {
		if _, err := os.Stat(filepath.Join(repo, name)); err != nil {
			t.Fatalf("landed GOAL step is missing %s in the user's repository: %v", name, err)
		}
	}

	// The final step is an ordinary FAST step: Austin submits, Tony verifies,
	// then both sign the finished queue.
	writeAndCommit(t, set.Austin.Path, "final.txt", "final\n", "fast final step")
	sign(t, austin)
	waitPhase(t, state, project.PhaseReview)
	request(t, tony, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetVerification, Verification: "passed", Note: "ok"})
	waitPhase(t, state, project.PhaseIntegrate)
	sign(t, austin)
	sign(t, tony)
	waitPhase(t, state, project.PhaseDone)

	if _, err := os.Stat(filepath.Join(repo, "final.txt")); err != nil {
		t.Fatalf("the final FAST step did not reach the user's repository: %v", err)
	}
	snap := state.Snapshot()
	if len(snap.Steps) != 2 || !snap.Steps[0].Done || !snap.Steps[1].Done {
		t.Fatalf("finished queue = %+v, want both steps landed", snap.Steps)
	}
}
