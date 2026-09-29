package coordinator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
)

func fastRequestVerification(t *testing.T, austin *testAgent) {
	t.Helper()
	ready := true
	request(t, austin, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetStatus, Ready: &ready, Note: "work complete"})
}

func fastVerdict(t *testing.T, tony *testAgent, result, note string) (protocol.Message, error) {
	t.Helper()
	return tony.request(protocol.Message{Version: protocol.Version, Type: protocol.MsgSetVerification, Verification: result, Note: note})
}

// TestFastModeEndToEndVerifiesThenDelivers drives the whole Fast lifecycle over
// the real transport: RUNNING → VERIFY, an issue_found round trip back to
// RUNNING, a fresh request, then a passed verdict and delivery of Austin's HEAD
// before DONE. It also pins the gates that keep Fast from becoming a weaker
// Goal: no shared plan, no Tony sign-off, no verdict outside VERIFY.
func TestFastModeEndToEndVerifiesThenDelivers(t *testing.T) {
	ctx := context.Background()
	runtime := startE2EWithMode(t, ctx, project.ModeFast)
	repo, set, store, state, server := runtime.repo, runtime.set, runtime.store, runtime.state, runtime.server

	austin := runtime.dialAgent(t, protocol.Austin)
	tony := runtime.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool { return server.IsConnected(protocol.Austin) && server.IsConnected(protocol.Tony) }, "both agents to connect")

	snap := state.Snapshot()
	if snap.Mode != project.ModeFast || snap.Phase != project.PhaseRunning {
		t.Fatalf("fast session must start in RUNNING, got mode=%q phase=%s", snap.Mode, snap.Phase)
	}

	// RUNNING: Austin drives alone and produces the deliverable.
	writeAndCommit(t, set.Austin.Path, "result.md", "hello Duo\n", "add result.md")

	if resp, err := austin.request(protocol.Message{Version: protocol.Version, Type: protocol.MsgSetPlan, Plan: "nope"}); err == nil {
		t.Fatalf("Fast mode accepted a shared plan: %+v", resp)
	}
	{
		ready := true
		if resp, err := tony.request(protocol.Message{Version: protocol.Version, Type: protocol.MsgSetStatus, Ready: &ready}); err == nil {
			t.Fatalf("Fast mode accepted Tony's phase sign-off: %+v", resp)
		}
	}
	if resp, err := fastVerdict(t, tony, "passed", ""); err == nil {
		t.Fatalf("Fast mode accepted a verdict before any request: %+v", resp)
	}

	fastRequestVerification(t, austin)
	waitPhase(t, state, project.PhaseVerify)
	requested := state.Snapshot().Verification.Head
	if requested != gitHead(t, set.Austin.Path) {
		t.Fatalf("verification target = %s, want Austin HEAD %s", requested, gitHead(t, set.Austin.Path))
	}

	// A structured verdict is mandatory: issue_found without a note is refused.
	if resp, err := fastVerdict(t, tony, "issue_found", ""); err == nil {
		t.Fatalf("issue_found without a note was accepted: %+v", resp)
	}

	if resp, err := fastVerdict(t, tony, "issue_found", "missing trailing newline handling"); err != nil {
		t.Fatalf("issue_found rejected: %s", resp.Text)
	}
	waitPhase(t, state, project.PhaseRunning)
	// The issue is retained as a result Austin can act on (it is what duo_status
	// and the TUI show in RUNNING); only a *pending* request is revoked.
	issue := state.Snapshot().Verification
	if issue.Status != project.VerificationIssueFound || issue.Note != "missing trailing newline handling" {
		t.Fatalf("issue_found must be recorded for Austin, got %+v", issue)
	}
	if delivered := coordDelivery(t, store); delivered.Applied() {
		t.Fatalf("issue_found must never deliver, got %+v", delivered)
	}

	// Austin fixes the issue and requests verification of the new HEAD.
	writeAndCommit(t, set.Austin.Path, "result.md", "hello Duo!\n", "fix result.md")
	fastRequestVerification(t, austin)
	waitPhase(t, state, project.PhaseVerify)
	finalHead := state.Snapshot().Verification.Head
	if finalHead == requested {
		t.Fatal("test did not produce a new verified HEAD")
	}

	if resp, err := fastVerdict(t, tony, "passed", "checked on a clean worktree"); err != nil {
		t.Fatalf("passed verdict rejected: %s", resp.Text)
	}
	waitPhase(t, state, project.PhaseDone)

	if got := gitHead(t, repo); got != finalHead {
		t.Fatalf("original repository HEAD = %s, want the delivered %s", got, finalHead)
	}
	data, err := os.ReadFile(filepath.Join(repo, "result.md"))
	if err != nil {
		t.Fatalf("result.md was not delivered: %v", err)
	}
	if string(data) != "hello Duo!\n" {
		t.Fatalf("result.md = %q", data)
	}

	waitFor(t, func() bool {
		snap, err := store.Load()
		return err == nil && snap.Phase == string(project.PhaseDone)
	}, "durable DONE checkpoint")
	persisted, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if persisted.EffectiveMode() != project.ModeFast {
		t.Fatalf("persisted mode = %q, want fast", persisted.Mode)
	}
	if persisted.Delivery.Status != sessionstore.DeliveryApplied || persisted.Delivery.AppliedHead != finalHead {
		t.Fatalf("persisted delivery = %+v, want applied at %s", persisted.Delivery, finalHead)
	}
	if persisted.VerificationResult().Status != project.VerificationPassed {
		t.Fatalf("persisted verification = %+v, want passed", persisted.Verification)
	}
}

// TestComposerReopensDeliveredSession is the follow-up regression: a new human
// task submitted through the Duo composer after DONE must start a fresh round so
// Austin can request another independent verification, instead of being answered
// with no path back to Tony. It also proves the finished round's applied
// delivery checkpoint cannot short-circuit the new round back to DONE.
func TestComposerReopensDeliveredSession(t *testing.T) {
	ctx := context.Background()
	runtime := startE2EWithMode(t, ctx, project.ModeFast)
	set, store, state, server := runtime.set, runtime.store, runtime.state, runtime.server

	austin := runtime.dialAgent(t, protocol.Austin)
	tony := runtime.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool { return server.IsConnected(protocol.Austin) && server.IsConnected(protocol.Tony) }, "both agents to connect")

	// Round 1: the normal Fast cycle delivers a verified artifact.
	writeAndCommit(t, set.Austin.Path, "result.md", "round one\n", "round one")
	fastRequestVerification(t, austin)
	waitPhase(t, state, project.PhaseVerify)
	if _, err := fastVerdict(t, tony, "passed", ""); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, state, project.PhaseDone)
	firstHead := gitHead(t, runtime.repo)
	if delivered := coordDelivery(t, store); !delivered.Applied() || delivered.AppliedHead != firstHead {
		t.Fatalf("round 1 delivery = %+v, want applied at %s", delivered, firstHead)
	}

	// The human submits a follow-up through the composer. It must reopen the
	// finished session rather than leaving Austin unable to reach Tony.
	if err := runtime.coord.SubmitUserTask(ctx, "one more change please"); err != nil {
		t.Fatalf("SubmitUserTask after DONE: %v", err)
	}
	waitPhase(t, state, project.PhaseRunning)
	if reopened := state.Snapshot(); reopened.Verification.Status != project.VerificationNone || reopened.Verification.Head != "" {
		t.Fatalf("a reopened round must not inherit a verification: %+v", reopened.Verification)
	}
	if cleared := coordDelivery(t, store); cleared.Status != "" {
		t.Fatalf("a reopened round must clear the applied delivery checkpoint, got %+v", cleared)
	}

	// Round 2 is an ordinary Fast cycle and must deliver independently.
	writeAndCommit(t, set.Austin.Path, "result.md", "round two\n", "round two")
	fastRequestVerification(t, austin)
	waitPhase(t, state, project.PhaseVerify)
	if _, err := fastVerdict(t, tony, "passed", ""); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, state, project.PhaseDone)
	secondHead := gitHead(t, runtime.repo)
	if secondHead == firstHead {
		t.Fatal("round 2 did not deliver a new HEAD")
	}
	if delivered := coordDelivery(t, store); !delivered.Applied() || delivered.AppliedHead != secondHead {
		t.Fatalf("round 2 delivery = %+v, want applied at %s", delivered, secondHead)
	}
}

// TestFastModeNativeFollowUpReopens pins the follow-up path that does not go
// through the Duo composer: the human talks to Austin in native Pi (Ctrl+A) and
// lets it commit, so SubmitUserTask never runs. Austin's next completion request
// is the only signal Duo sees, and it names a new Austin HEAD. It must reopen the
// DONE round instead of being rejected, otherwise the new commit is stranded in
// the worktree with no path to verification or delivery.
func TestFastModeNativeFollowUpReopens(t *testing.T) {
	ctx := context.Background()
	runtime := startE2EWithMode(t, ctx, project.ModeFast)
	set, store, state, server := runtime.set, runtime.store, runtime.state, runtime.server

	austin := runtime.dialAgent(t, protocol.Austin)
	tony := runtime.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool { return server.IsConnected(protocol.Austin) && server.IsConnected(protocol.Tony) }, "both agents to connect")

	// Round 1: the normal Fast cycle delivers a verified artifact.
	writeAndCommit(t, set.Austin.Path, "result.md", "round one\n", "round one")
	fastRequestVerification(t, austin)
	waitPhase(t, state, project.PhaseVerify)
	if _, err := fastVerdict(t, tony, "passed", ""); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, state, project.PhaseDone)
	firstHead := gitHead(t, runtime.repo)
	if delivered := coordDelivery(t, store); !delivered.Applied() || delivered.AppliedHead != firstHead {
		t.Fatalf("round 1 delivery = %+v, want applied at %s", delivered, firstHead)
	}

	// The human asks for a follow-up in native Pi, and Austin commits and
	// requests verification without Duo ever seeing a human task.
	writeAndCommit(t, set.Austin.Path, "result.md", "round two\n", "round two")
	fastRequestVerification(t, austin)
	waitPhase(t, state, project.PhaseVerify)
	if got := state.Snapshot().Verification.Head; got != gitHead(t, set.Austin.Path) {
		t.Fatalf("native follow-up verification targets %s, want Austin HEAD %s", got, gitHead(t, set.Austin.Path))
	}
	if cleared := coordDelivery(t, store); cleared.Status != "" {
		t.Fatalf("a reopened round must clear the applied delivery checkpoint, got %+v", cleared)
	}

	// Round 2 is an ordinary Fast cycle and must deliver independently.
	if _, err := fastVerdict(t, tony, "passed", ""); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, state, project.PhaseDone)
	secondHead := gitHead(t, runtime.repo)
	if secondHead == firstHead {
		t.Fatal("round 2 did not deliver a new HEAD")
	}
	if delivered := coordDelivery(t, store); !delivered.Applied() || delivered.AppliedHead != secondHead {
		t.Fatalf("round 2 delivery = %+v, want applied at %s", delivered, secondHead)
	}
}

// TestFastModeRevokesVerificationWhenHeadMoves pins the single safety rule that
// makes Fast delivery sound: a verdict describes one exact Austin HEAD, and if
// that HEAD moves the request is revoked before the verdict can be accepted.
func TestFastModeRevokesVerificationWhenHeadMoves(t *testing.T) {
	ctx := context.Background()
	runtime := startE2EWithMode(t, ctx, project.ModeFast)
	set, state, server := runtime.set, runtime.state, runtime.server

	austin := runtime.dialAgent(t, protocol.Austin)
	tony := runtime.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool { return server.IsConnected(protocol.Austin) && server.IsConnected(protocol.Tony) }, "both agents to connect")

	writeAndCommit(t, set.Austin.Path, "app.txt", "v1\n", "v1")
	fastRequestVerification(t, austin)
	waitPhase(t, state, project.PhaseVerify)
	stale := state.Snapshot().Verification.Head

	// Austin changes the artifact after requesting verification.
	writeAndCommit(t, set.Austin.Path, "app.txt", "v2\n", "v2")
	if gitHead(t, set.Austin.Path) == stale {
		t.Fatal("test did not move Austin's HEAD")
	}

	if resp, err := fastVerdict(t, tony, "passed", ""); err == nil {
		t.Fatalf("a verdict on the stale artifact was accepted: %+v", resp)
	}
	waitPhase(t, state, project.PhaseRunning)
	if got := state.Snapshot().Verification; got.Status != project.VerificationNone || got.Head != "" {
		t.Fatalf("a revoked request must leave no verification behind, got %+v", got)
	}
	if delivered := coordDelivery(t, runtime.store); delivered.Applied() {
		t.Fatalf("a stale verification must never deliver, got %+v", delivered)
	}
}

// TestFastModeAustinWithdrawsInsteadOfWaiting proves Fast is non-blocking: while
// Tony is idle or thinking, Austin can withdraw its completion request and keep
// working rather than waiting for a verdict.
func TestFastModeAustinWithdrawsInsteadOfWaiting(t *testing.T) {
	ctx := context.Background()
	runtime := startE2EWithMode(t, ctx, project.ModeFast)
	set, state, server := runtime.set, runtime.state, runtime.server

	austin := runtime.dialAgent(t, protocol.Austin)
	runtime.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool { return server.IsConnected(protocol.Austin) && server.IsConnected(protocol.Tony) }, "both agents to connect")

	writeAndCommit(t, set.Austin.Path, "app.txt", "v1\n", "v1")
	fastRequestVerification(t, austin)
	waitPhase(t, state, project.PhaseVerify)

	notReady := false
	request(t, austin, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetStatus, Ready: &notReady})
	waitPhase(t, state, project.PhaseRunning)
	if delivered := coordDelivery(t, runtime.store); delivered.Applied() {
		t.Fatalf("a withdrawn request must not deliver, got %+v", delivered)
	}

	// Austin can request verification again for the same HEAD.
	fastRequestVerification(t, austin)
	waitPhase(t, state, project.PhaseVerify)
}

// TestFastModeAutomatedTestGate verifies that when a test command is configured,
// a failing test command blocks advancing from RUNNING to VERIFY, while a passing
// test command allows verification and delivery.
func TestFastModeAutomatedTestGate(t *testing.T) {
	ctx := context.Background()
	runtime := startE2EWithMode(t, ctx, project.ModeFast)
	set, state, server := runtime.set, runtime.state, runtime.server

	austin := runtime.dialAgent(t, protocol.Austin)
	tony := runtime.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool { return server.IsConnected(protocol.Austin) && server.IsConnected(protocol.Tony) }, "both agents to connect")

	// Configure a test command that checks for required.txt
	runtime.coord.SetTestCommand("test -f required.txt")

	// 1. Austin creates a commit without required.txt and requests verification
	writeAndCommit(t, set.Austin.Path, "other.txt", "v1\n", "v1")
	ready := true
	resp, err := austin.request(protocol.Message{Version: protocol.Version, Type: protocol.MsgSetStatus, Ready: &ready, Note: "work complete"})
	if err == nil {
		t.Fatalf("verification request should fail due to failing test command: %+v", resp)
	}

	// Session must remain in RUNNING
	if state.Snapshot().Phase != project.PhaseRunning {
		t.Fatalf("phase = %s, want %s after failed test gate", state.Snapshot().Phase, project.PhaseRunning)
	}

	// 2. Austin adds required.txt and requests verification again
	writeAndCommit(t, set.Austin.Path, "required.txt", "passed\n", "add required.txt")
	fastRequestVerification(t, austin)
	waitPhase(t, state, project.PhaseVerify)

	// 3. Tony verifies passed and delivery completes
	if _, err := fastVerdict(t, tony, "passed", ""); err != nil {
		t.Fatalf("tony verdict passed rejected: %v", err)
	}
	waitPhase(t, state, project.PhaseDone)
}

// TestFastModeEscalateToGoal verifies that an active Fast mode session can be
// dynamically escalated to Goal mode. Austin's existing work is preserved,
// the session enters PLAN phase, both agents co-design and approve the shared
// plan, and then execute through the full Goal workflow to DONE.
func TestFastModeEscalateToGoal(t *testing.T) {
	ctx := context.Background()
	runtime := startE2EWithMode(t, ctx, project.ModeFast)
	repo, set, store, state, server, coord := runtime.repo, runtime.set, runtime.store, runtime.state, runtime.server, runtime.coord

	austin := runtime.dialAgent(t, protocol.Austin)
	tony := runtime.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool { return server.IsConnected(protocol.Austin) && server.IsConnected(protocol.Tony) }, "both agents to connect")

	// 1. Austin commits initial work in Fast mode.
	writeAndCommit(t, set.Austin.Path, "feature.txt", "v1\n", "initial feature commit")

	// 2. Escalate to Goal mode via MsgEscalate from Austin.
	resp, err := austin.request(protocol.Message{
		Version: protocol.Version,
		Type:    protocol.MsgEscalate,
		Note:    "needs dual-agent architecture co-design",
	})
	if err != nil {
		t.Fatalf("escalate request failed: %v", err)
	}
	if !resp.OK {
		t.Fatalf("escalate response not ok: %+v", resp)
	}

	// Session must now be Goal mode and PLAN phase.
	snap := state.Snapshot()
	if snap.Mode != project.ModeGoal || snap.EffectiveMode() != project.ModeGoal {
		t.Fatalf("mode = %s, want %s", snap.Mode, project.ModeGoal)
	}
	if snap.Phase != project.PhasePlan {
		t.Fatalf("phase = %s, want %s", snap.Phase, project.PhasePlan)
	}
	if snap.PlanVersion != 1 {
		t.Fatalf("planVersion = %d, want 1", snap.PlanVersion)
	}

	// Verify persistence wrote Goal mode to disk.
	persisted, err := store.Load()
	if err != nil {
		t.Fatalf("load persisted snapshot: %v", err)
	}
	if persisted.EffectiveMode() != project.ModeGoal || persisted.Phase != string(project.PhasePlan) {
		t.Fatalf("persisted snapshot mode=%q phase=%q, want goal/PLAN", persisted.Mode, persisted.Phase)
	}

	// 3. In PLAN, Austin updates the shared plan.
	planResp, err := austin.request(protocol.Message{
		Version: protocol.Version,
		Type:    protocol.MsgSetPlan,
		Plan:    "# Co-designed Plan\n- Austin: core logic\n- Tony: tests",
	})
	if err != nil || !planResp.OK {
		t.Fatalf("MsgSetPlan failed in escalated session: %v (resp: %+v)", err, planResp)
	}

	// Both agents sign the plan.
	readyTrue := true
	if _, err := austin.request(protocol.Message{Version: protocol.Version, Type: protocol.MsgSetStatus, Ready: &readyTrue, Note: "approved"}); err != nil {
		t.Fatalf("austin plan approval failed: %v", err)
	}
	if _, err := tony.request(protocol.Message{Version: protocol.Version, Type: protocol.MsgSetStatus, Ready: &readyTrue, Note: "approved"}); err != nil {
		t.Fatalf("tony plan approval failed: %v", err)
	}

	// Phase must advance to EXECUTE.
	waitPhase(t, state, project.PhaseExecute)

	// 4. In EXECUTE, Tony adds tests and Austin finishes core logic.
	writeAndCommit(t, set.Tony.Path, "feature_test.txt", "test ok\n", "add tests")
	writeAndCommit(t, set.Austin.Path, "feature.txt", "v1 + v2\n", "finish core")

	// Both sign EXECUTE.
	if _, err := austin.request(protocol.Message{Version: protocol.Version, Type: protocol.MsgSetStatus, Ready: &readyTrue, Note: "execute done"}); err != nil {
		t.Fatalf("austin execute sign failed: %v", err)
	}
	if _, err := tony.request(protocol.Message{Version: protocol.Version, Type: protocol.MsgSetStatus, Ready: &readyTrue, Note: "execute done"}); err != nil {
		t.Fatalf("tony execute sign failed: %v", err)
	}

	waitPhase(t, state, project.PhaseReview)

	// Both sign REVIEW.
	if _, err := austin.request(protocol.Message{Version: protocol.Version, Type: protocol.MsgSetStatus, Ready: &readyTrue, Note: "reviewed"}); err != nil {
		t.Fatalf("austin review sign failed: %v", err)
	}
	if _, err := tony.request(protocol.Message{Version: protocol.Version, Type: protocol.MsgSetStatus, Ready: &readyTrue, Note: "reviewed"}); err != nil {
		t.Fatalf("tony review sign failed: %v", err)
	}

	waitPhase(t, state, project.PhaseIntegrate)

	// In INTEGRATE, both sign off to trigger delivery.
	if _, err := austin.request(protocol.Message{Version: protocol.Version, Type: protocol.MsgSetStatus, Ready: &readyTrue, Note: "integrated"}); err != nil {
		t.Fatalf("austin integrate sign failed: %v", err)
	}
	if _, err := tony.request(protocol.Message{Version: protocol.Version, Type: protocol.MsgSetStatus, Ready: &readyTrue, Note: "integrated"}); err != nil {
		t.Fatalf("tony integrate sign failed: %v", err)
	}

	waitPhase(t, state, project.PhaseDone)

	// Verify deliverable made it back to the original repository.
	content, err := os.ReadFile(filepath.Join(repo, "feature.txt"))
	if err != nil || string(content) != "v1 + v2\n" {
		t.Fatalf("delivered feature.txt content %q, err %v", string(content), err)
	}
	testContent, err := os.ReadFile(filepath.Join(repo, "feature_test.txt"))
	if err != nil || string(testContent) != "test ok\n" {
		t.Fatalf("delivered feature_test.txt content %q, err %v", string(testContent), err)
	}

	_ = coord
}

func TestFastModeEscalateViaComposer(t *testing.T) {
	ctx := context.Background()
	runtime := startE2EWithMode(t, ctx, project.ModeFast)
	state, server, coord := runtime.state, runtime.server, runtime.coord

	runtime.dialAgent(t, protocol.Austin)
	runtime.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool { return server.IsConnected(protocol.Austin) && server.IsConnected(protocol.Tony) }, "both agents to connect")

	// Escalate via /escalate slash command in composer
	if err := coord.SubmitUserTask(ctx, "/escalate refactoring is too broad"); err != nil {
		t.Fatalf("SubmitUserTask with /escalate failed: %v", err)
	}

	snap := state.Snapshot()
	if snap.Mode != project.ModeGoal || snap.EffectiveMode() != project.ModeGoal {
		t.Fatalf("expected ModeGoal, got %s", snap.Mode)
	}
	if snap.Phase != project.PhasePlan {
		t.Fatalf("expected PhasePlan, got %s", snap.Phase)
	}

	// Attempting /mode fast must be rejected
	if err := coord.SubmitUserTask(ctx, "/mode fast"); err == nil {
		t.Fatal("expected error when attempting to switch from Goal to Fast")
	}
}



