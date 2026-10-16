package coordinator

import (
	"context"
	"os"
	"path/filepath"
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

// TestStepModeRefusesAnEmptyQueue pins the PLAN gate: the queue is the PLAN
// artifact, so a plan with no steps must not be approvable. Without this, EXECUTE
// would begin with no step to work on and the session would be stuck.
func TestStepModeRefusesAnEmptyQueue(t *testing.T) {
	ctx := context.Background()

	runtime := startE2EWithMode(t, ctx, project.ModeStep)
	state, server := runtime.state, runtime.server

	austin := runtime.dialAgent(t, protocol.Austin)
	tony := runtime.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool { return server.IsConnected(protocol.Austin) && server.IsConnected(protocol.Tony) }, "both agents to connect")

	request(t, austin, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetPlan, Plan: "prose only"})

	ready := true
	approve := func(c *testAgent) (protocol.Message, error) {
		return c.request(protocol.Message{Version: protocol.Version, Type: protocol.MsgSetStatus, Ready: &ready})
	}
	if response, err := approve(austin); err == nil {
		t.Fatalf("Austin approved PLAN with no queue: %+v", response)
	}
	if response, err := approve(tony); err == nil {
		t.Fatalf("Tony approved PLAN with no queue: %+v", response)
	}
	if phase := state.Snapshot().Phase; phase != project.PhasePlan {
		t.Fatalf("an empty queue must not leave PLAN, got %s", phase)
	}
}

// TestStepModeOnlyAustinSubmitsAndTonyOnlyReports pins who may use which verb in a
// step. A step is submitted by Austin and checked by Tony's verdict, so Tony
// signing a step would be a weaker gate than the one this mode promises.
func TestStepModeOnlyAustinSubmitsAndTonyOnlyReports(t *testing.T) {
	ctx := context.Background()

	runtime := startE2EWithMode(t, ctx, project.ModeStep)
	set, state, server := runtime.set, runtime.state, runtime.server

	austin := runtime.dialAgent(t, protocol.Austin)
	tony := runtime.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool { return server.IsConnected(protocol.Austin) && server.IsConnected(protocol.Tony) }, "both agents to connect")

	request(t, austin, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetPlan, Plan: "two steps"})
	request(t, austin, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetSteps, Steps: "- [ ] one\n- [ ] two"})
	sign(t, austin)
	sign(t, tony)
	waitPhase(t, state, project.PhaseExecute)

	writeAndCommit(t, set.Austin.Path, "one.md", "one\n", "add one.md")

	// Tony cannot submit the step by signing it.
	if response, err := tony.request(protocol.Message{Version: protocol.Version, Type: protocol.MsgSetStatus, Ready: &[]bool{true}[0], Note: "looks fine"}); err == nil {
		t.Fatalf("Tony submitted a step with a sign-off: %+v", response)
	}
	if phase := state.Snapshot().Phase; phase != project.PhaseExecute {
		t.Fatalf("Tony's sign-off moved the session to %s", phase)
	}

	// And Austin's verdict is not a verdict either: the check belongs to Tony.
	if response, err := austin.request(protocol.Message{Version: protocol.Version, Type: protocol.MsgSetVerification, Verification: "passed", Note: "self-approved"}); err == nil {
		t.Fatalf("Austin reported a verification verdict: %+v", response)
	}

	// Austin's own submission still works, so the two refusals above are about the
	// actor and not about a session that cannot move at all.
	sign(t, austin)
	waitPhase(t, state, project.PhaseReview)
}

// TestStepModeRefusesAMidQueueSignOff pins the Goal gate that STEP keeps only at
// the end: with the queue unfinished, a sign-off may not finish the session.
//
// The delivery is blocked on purpose, by moving the base branch underneath the
// session, so the mid-queue step actually stays in INTEGRATE. Leaving it to a
// fresh repository would instead catch the transient INTEGRATE that exists
// between the verdict and the landing, which is a race, not a state.
func TestStepModeRefusesAMidQueueSignOff(t *testing.T) {
	ctx := context.Background()

	runtime := startE2EWithMode(t, ctx, project.ModeStep)
	repo, set, state, server := runtime.repo, runtime.set, runtime.state, runtime.server

	austin := runtime.dialAgent(t, protocol.Austin)
	tony := runtime.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool { return server.IsConnected(protocol.Austin) && server.IsConnected(protocol.Tony) }, "both agents to connect")

	request(t, austin, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetPlan, Plan: "three steps"})
	request(t, austin, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetSteps, Steps: "- [ ] one\n- [ ] two\n- [ ] three"})
	sign(t, austin)
	sign(t, tony)
	waitPhase(t, state, project.PhaseExecute)

	writeAndCommit(t, set.Austin.Path, "one.md", "one\n", "add one.md")
	sign(t, austin)
	waitPhase(t, state, project.PhaseReview)

	// Move the base branch so the landing cannot fast-forward. Delivery refuses
	// rather than rewriting the user's history, which is what keeps this mid-queue
	// step sitting in INTEGRATE for the sign-off attempt below.
	writeAndCommit(t, repo, "diverge.md", "base moved\n", "move the base branch")

	request(t, tony, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetVerification, Verification: "passed", Note: "ok"})
	waitPhase(t, state, project.PhaseIntegrate)
	waitFor(t, func() bool { return gitHead(t, repo) != gitHead(t, set.Austin.Path) }, "the blocked landing to be observable")

	// A mid-queue step is not the end of the session, so no sign-off may finish it.
	if response, err := austin.request(protocol.Message{Version: protocol.Version, Type: protocol.MsgSetStatus, Ready: &[]bool{true}[0], Note: "finish"}); err == nil {
		t.Fatalf("a mid-queue step accepted a sign-off: %+v", response)
	}
	if phase := state.Snapshot().Phase; phase == project.PhaseDone {
		t.Fatal("a mid-queue step reached DONE")
	}
	if snap := state.Snapshot(); snap.Step != 1 {
		t.Fatalf("a blocked mid-queue landing advanced the cursor to %d", snap.Step)
	}
}

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

// TestStepModeSyncsTheVerifierOntoTheLandedStep pins the verifier-visibility rule
// at the live coordinator, not only in the recovery replay.
//
// Every landed step goes to the user's branch, so a verifier left on the previous
// step would review and build the next one on a tree that never reached the user,
// and each step's merge would re-conflict against the original session base. The
// fast-forward happens at the durable landing, so it must be observable here and
// not only after a resume.
func TestStepModeSyncsTheVerifierOntoTheLandedStep(t *testing.T) {
	ctx := context.Background()

	runtime := startE2EWithMode(t, ctx, project.ModeStep)
	set, state, server := runtime.set, runtime.state, runtime.server

	austin := runtime.dialAgent(t, protocol.Austin)
	tony := runtime.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool { return server.IsConnected(protocol.Austin) && server.IsConnected(protocol.Tony) }, "both agents to connect")

	tonyBefore := gitHead(t, set.Tony.Path)

	request(t, austin, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetPlan, Plan: "two steps"})
	request(t, austin, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetSteps, Steps: "- [ ] one\n- [ ] two"})
	sign(t, austin)
	sign(t, tony)
	waitPhase(t, state, project.PhaseExecute)

	writeAndCommit(t, set.Austin.Path, "one.md", "one\n", "add one.md")
	stepHead := gitHead(t, set.Austin.Path)
	if stepHead == tonyBefore {
		t.Fatal("test setup: Tony was already on the step head")
	}
	sign(t, austin)
	waitPhase(t, state, project.PhaseReview)
	request(t, tony, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetVerification, Verification: "passed", Note: "ok"})
	waitFor(t, func() bool {
		snap := state.Snapshot()
		return len(snap.Steps) > 0 && snap.Steps[0].Done
	}, "step 1 to land")

	waitFor(t, func() bool { return gitHead(t, set.Tony.Path) == stepHead }, "the verifier worktree to be fast-forwarded onto the landed step")

	// The landed content, not just the ref, must be in Tony's worktree: that is what
	// he reviews and builds against.
	if _, err := os.Stat(filepath.Join(set.Tony.Path, "one.md")); err != nil {
		t.Fatalf("the landed step is missing from the verifier worktree: %v", err)
	}
}
