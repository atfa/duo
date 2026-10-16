package coordinator

import (
	"context"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// TestStepModeLandsEachStepEndToEnd is the regression for the defect that made
// STEP mode non-functional: the delivery gate required a Goal-style dual INTEGRATE
// sign-off, which a mid-queue step never has, so every non-final landing was
// silently refused. Nothing exercised the coordinator gate and the landing
// together, which is exactly where the defect lived.
//
// It drives the real coordinator over real Git worktrees and asserts the four
// facts the mode promises for one step: the step is recorded as landed with its
// delivery head, the cursor advances, the session returns to EXECUTE for the next
// step, and the user's repository actually moved.
func TestStepModeLandsEachStepEndToEnd(t *testing.T) {
	ctx := context.Background()

	runtime := startE2EWithMode(t, ctx, project.ModeStep)
	repo := runtime.repo
	baseCommit := gitHead(t, repo)

	set, store, coord, state, server := runtime.set, runtime.store, runtime.coord, runtime.state, runtime.server
	_ = coord

	austin := runtime.dialAgent(t, protocol.Austin)
	tony := runtime.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool { return server.IsConnected(protocol.Austin) && server.IsConnected(protocol.Tony) }, "both agents to connect")

	// PLAN: the queue is the dual-approved artifact, so the plan is only prose.
	request(t, austin, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetPlan, Plan: "Add two files, one step each"})
	request(t, austin, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetSteps, Steps: "- [ ] add first.md\n- [ ] add second.md"})
	sign(t, austin)
	sign(t, tony)
	waitPhase(t, state, project.PhaseExecute)
	if got := state.Snapshot().Step; got != 1 {
		t.Fatalf("cursor after PLAN = %d, want 1", got)
	}

	// Step 1: Austin commits, submits the step with his HEAD, Tony verifies it.
	writeAndCommit(t, set.Austin.Path, "first.md", "first\n", "add first.md")
	stepHead := gitHead(t, set.Austin.Path)
	sign(t, austin)
	waitPhase(t, state, project.PhaseReview)

	verdict := protocol.Message{Version: protocol.Version, Type: protocol.MsgSetVerification, Verification: "passed", Note: "ok"}
	if response := request(t, tony, verdict); response.Type != protocol.MsgResponse {
		t.Fatalf("verdict response = %+v", response)
	}

	// The landing is the assertion the old code failed: the queue advances, the
	// step records where it landed, and the session is back in EXECUTE.
	waitFor(t, func() bool {
		snap := state.Snapshot()
		return len(snap.Steps) > 0 && snap.Steps[0].Done
	}, "step 1 to be recorded as landed")
	waitPhase(t, state, project.PhaseExecute)

	snap := state.Snapshot()
	if snap.Step != 2 {
		t.Fatalf("cursor after landing step 1 = %d, want 2", snap.Step)
	}
	if got := snap.Steps[0].DeliveryHead; got != stepHead {
		t.Fatalf("step 1 delivery head = %q, want the reviewed HEAD %q", got, stepHead)
	}
	if snap.Steps[0].DeliveredAt == "" {
		t.Fatal("a landed step must record when it landed")
	}
	waitFor(t, func() bool { return gitHead(t, repo) == stepHead }, "the base branch to move to step 1's HEAD")
	if gitHead(t, repo) == baseCommit {
		t.Fatal("delivery did not move the user's repository at all")
	}

	// The session must not be finished and must not be waiting on a signature: a
	// mid-queue step has no dual sign-off by design.
	if ready := snap.Ready[protocol.Austin] || snap.Ready[protocol.Tony]; ready {
		t.Fatal("a landed mid-queue step must not leave a signature pending")
	}
	if delivery := coordDelivery(t, store); !delivery.Applied() {
		t.Fatalf("step 1 delivery = %+v, want applied", delivery)
	}

	// Step 2 is the final step: its verdict must NOT land it on its own. The Goal
	// gate is kept exactly here, so both agents sign before Duo delivers.
	writeAndCommit(t, set.Austin.Path, "second.md", "second\n", "add second.md")
	finalStepHead := gitHead(t, set.Austin.Path)
	sign(t, austin)
	waitPhase(t, state, project.PhaseReview)
	request(t, tony, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetVerification, Verification: "passed", Note: "ok"})
	waitPhase(t, state, project.PhaseIntegrate)
	waitFor(t, func() bool { return state.Snapshot().Step == 2 }, "the cursor to stay on the final step")
	if got := gitHead(t, repo); got != stepHead {
		t.Fatalf("the final step landed before sign-off: repo HEAD = %s, want still %s", got, stepHead)
	}

	// The final step needs both agents to sign INTEGRATE; that is the Goal gate the
	// mode keeps, and it is why Tony's verdict alone did not land it.
	sign(t, austin)
	sign(t, tony)
	waitFor(t, func() bool { return state.Snapshot().Phase == project.PhaseDone }, "the session to complete after both signatures")
	if got := gitHead(t, repo); got != finalStepHead {
		t.Fatalf("original repository HEAD = %s, want the delivered %s", got, finalStepHead)
	}
	final := state.Snapshot()
	if len(final.Steps) != 2 || !final.Steps[1].Done {
		t.Fatalf("finished queue = %+v, want both steps landed", final.Steps)
	}
	if final.Steps[1].DeliveryHead != finalStepHead {
		t.Fatalf("final step delivery head = %q, want %q", final.Steps[1].DeliveryHead, finalStepHead)
	}
}

// TestStepModeRefusesToLandWhenTheReviewedHeadMoved is the other half of the gate:
// a step whose HEAD moved after Tony's verdict must not be landed, even though the
// verdict was for the same step. Landing it would put commits nobody reviewed into
// the user's repository.
func TestStepModeRefusesToLandWhenTheReviewedHeadMoved(t *testing.T) {
	ctx := context.Background()

	runtime := startE2EWithMode(t, ctx, project.ModeStep)
	repo := runtime.repo

	set, state, server := runtime.set, runtime.state, runtime.server

	austin := runtime.dialAgent(t, protocol.Austin)
	tony := runtime.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool { return server.IsConnected(protocol.Austin) && server.IsConnected(protocol.Tony) }, "both agents to connect")

	request(t, austin, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetPlan, Plan: "one step, then a second"})
	request(t, austin, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetSteps, Steps: "- [ ] one\n- [ ] two"})
	sign(t, austin)
	sign(t, tony)
	waitPhase(t, state, project.PhaseExecute)

	writeAndCommit(t, set.Austin.Path, "one.md", "one\n", "add one.md")
	reviewed := gitHead(t, set.Austin.Path)
	sign(t, austin)
	waitPhase(t, state, project.PhaseReview)

	// Austin commits again while the step is under review. The verdict names the
	// reviewed HEAD, so it must be refused and the step withdrawn.
	writeAndCommit(t, set.Austin.Path, "extra.md", "unreviewed\n", "commit during review")
	moved := gitHead(t, set.Austin.Path)

	response, err := tony.request(protocol.Message{Version: protocol.Version, Type: protocol.MsgSetVerification, Verification: "passed", Note: "ok"})
	if err == nil {
		t.Fatal("a verdict for a HEAD that moved must be refused, not accepted")
	}
	if !strings.Contains(err.Error(), "withdrawn") {
		t.Fatalf("refusal must say the review was withdrawn, got: %v", err)
	}
	if response.OK {
		t.Fatal("the response must report the refusal")
	}
	waitPhase(t, state, project.PhaseExecute)

	snap := state.Snapshot()
	if snap.Steps[0].Done {
		t.Fatal("a step whose HEAD moved must not be recorded as landed")
	}
	if snap.Steps[0].Head != "" {
		t.Fatalf("a withdrawn step must not keep a submitted HEAD, got %q", snap.Steps[0].Head)
	}
	if got := gitHead(t, repo); got == moved || got == reviewed {
		t.Fatalf("nothing may land from a withdrawn step, but repo HEAD = %s", got)
	}
}
