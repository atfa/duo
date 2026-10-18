package recovery

import (
	"context"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

// stepUnderReview builds the state a STEP session leaves behind when it is stopped
// while a step is under review: step 1 has landed on the user's branch, and step 2
// is submitted at a HEAD that already contains Tony's work for that step. Austin
// merges Tony per step inside EXECUTE, so Tony's HEAD being an ancestor of the
// reviewed HEAD is routine here — it is not the integration merge the Fast and
// Goal recovery rules read it as.
func stepUnderReview(t *testing.T, set workspace.Set, repo, landed string) sessionstore.Snapshot {
	t.Helper()

	committed(t, set.Tony.Path, "step2-tony.txt", "tony\n")
	run(t, set.Austin.Path, "git", "merge", "--no-edit", set.Tony.Branch)
	reviewed := strings.TrimSpace(run(t, set.Austin.Path, "git", "rev-parse", "HEAD"))

	// The verifier's tree is mid-scratch, so the landed-step fast-forward is
	// skipped: the session is reconciled exactly as the reported one was.
	writeFile(t, set.Tony.Path, "scratch.txt", "wip\n")

	snap := stepSnapshotWithLanded(set, repo, landed)
	snap.Steps[1].Head = reviewed
	return snap
}

// TestStepReconcileKeepsReviewForTheStepVerdict is the reported resume failure.
// A STEP session stopped in REVIEW came back in INTEGRATE, because the Fast-mode
// rule "Tony's HEAD is an ancestor of Austin's HEAD, so the integration merge
// landed" fired on the per-step merge. INTEGRATE is the phase that lands a step,
// so the session landed step 2 with no verdict from Tony at all.
func TestStepReconcileKeepsReviewForTheStepVerdict(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)

	landed := committed(t, set.Austin.Path, "step1.txt", "one\n")
	snap := stepUnderReview(t, set, repo, landed)
	snap.Phase = string(project.PhaseReview)

	result, err := Reconcile(ctx, ws, snap)
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Phase != string(project.PhaseReview) {
		t.Fatalf("phase = %s, want REVIEW: a step is gated by Tony's verdict, not by a merge", result.Snapshot.Phase)
	}
	if result.Report.Phase != project.PhaseReview {
		t.Fatalf("report phase = %s, want REVIEW", result.Report.Phase)
	}
	if result.Report.IntegrationRecovered || result.Snapshot.Integration.Started {
		t.Fatalf("STEP recovery invented an integration from a per-step merge: %+v", result.Snapshot.Integration)
	}
	if result.Snapshot.Steps[1].Head != snap.Steps[1].Head {
		t.Fatalf("the reviewed HEAD was lost: %+v", result.Snapshot.Steps[1])
	}
}

// TestStepReconcileKeepsIntegrateForTheStepDelivery covers the STEP session that
// is genuinely in INTEGRATE: Tony passed the step and the crash happened before it
// landed. The phase is the delivery's business, so recovery leaves it alone and
// records no integration merge.
func TestStepReconcileKeepsIntegrateForTheStepDelivery(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)

	landed := committed(t, set.Austin.Path, "step1.txt", "one\n")
	snap := stepUnderReview(t, set, repo, landed)
	snap.Phase = string(project.PhaseIntegrate)
	snap.Steps[1].Verification = project.Verification{Status: project.VerificationPassed, Head: snap.Steps[1].Head}

	result, err := Reconcile(ctx, ws, snap)
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Phase != string(project.PhaseIntegrate) {
		t.Fatalf("phase = %s, want INTEGRATE so the verified step is delivered", result.Snapshot.Phase)
	}
	if result.Report.IntegrationRecovered || result.Snapshot.Integration.Started {
		t.Fatalf("STEP recovery invented an integration: %+v", result.Snapshot.Integration)
	}
	if result.Snapshot.Steps[1].Verification.Status != project.VerificationPassed {
		t.Fatalf("Tony's verdict was lost on resume: %+v", result.Snapshot.Steps[1].Verification)
	}
}

// TestStepReconcileDemotesAnUnverifiedIntegrateToReview is the state the reported
// session was left in: a resume promoted it to INTEGRATE, the phase that lands a
// step, while the step carried no verdict at all. INTEGRATE is reachable only from
// Tony's passed verdict, so recovery re-derives the gate and sends the step back
// to review instead of landing it unreviewed.
func TestStepReconcileDemotesAnUnverifiedIntegrateToReview(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)

	landed := committed(t, set.Austin.Path, "step1.txt", "one\n")
	snap := stepUnderReview(t, set, repo, landed)
	snap.Phase = string(project.PhaseIntegrate)

	result, err := Reconcile(ctx, ws, snap)
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Phase != string(project.PhaseReview) {
		t.Fatalf("phase = %s, want REVIEW: an unverified step must not be delivered", result.Snapshot.Phase)
	}
	if result.Report.Phase != project.PhaseReview {
		t.Fatalf("report phase = %s, want REVIEW", result.Report.Phase)
	}
	if result.Snapshot.Steps[1].Verification.Status != project.VerificationNone {
		t.Fatalf("a verdict that never existed must not survive: %+v", result.Snapshot.Steps[1].Verification)
	}
	if result.Snapshot.Steps[1].Head != snap.Steps[1].Head {
		t.Fatalf("the submitted HEAD was lost: %+v", result.Snapshot.Steps[1])
	}
	if !hasNote(result.Report.Notes, "under review again") {
		t.Fatalf("the demotion was not reported: %v", result.Report.Notes)
	}
	// The landed step is untouched: recovery must not re-open finished work.
	if !result.Snapshot.Steps[0].Done || result.Snapshot.Steps[0].DeliveryHead != landed {
		t.Fatalf("the landed step changed: %+v", result.Snapshot.Steps[0])
	}
}

// TestStepReconcileDemotesAnIntegrateWhoseHeadMoved covers the same gate from the
// other side: Tony passed a HEAD, Austin committed afterwards, so the verdict no
// longer describes the artifact INTEGRATE would land.
func TestStepReconcileDemotesAnIntegrateWhoseHeadMoved(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)

	landed := committed(t, set.Austin.Path, "step1.txt", "one\n")
	snap := stepUnderReview(t, set, repo, landed)
	snap.Phase = string(project.PhaseIntegrate)
	snap.Steps[1].Verification = project.Verification{Status: project.VerificationPassed, Head: snap.Steps[1].Head}

	// Austin commits after the verdict: the reviewed HEAD is no longer the artifact.
	committed(t, set.Austin.Path, "step2-extra.txt", "extra\n")

	result, err := Reconcile(ctx, ws, snap)
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Phase != string(project.PhaseReview) {
		t.Fatalf("phase = %s, want REVIEW: the verdict binds a HEAD that is no longer current", result.Snapshot.Phase)
	}
	if result.Snapshot.Steps[1].Verification.Status != project.VerificationNone {
		t.Fatalf("a verdict about an old HEAD survived: %+v", result.Snapshot.Steps[1].Verification)
	}
}

// TestStepReconcileKeepsAFinishedQueueInIntegrate guards the last step: with the
// queue landed there is no current step, and the gate is the two signatures, so
// INTEGRATE is the phase the session must stay in to be signed off and completed.
func TestStepReconcileKeepsAFinishedQueueInIntegrate(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)

	landed := committed(t, set.Austin.Path, "step1.txt", "one\n")
	snap := stepUnderReview(t, set, repo, landed)
	snap.Phase = string(project.PhaseIntegrate)
	snap.Steps[1].Done = true
	snap.Steps[1].DeliveryHead = snap.Steps[1].Head
	snap.Step = 3 // the cursor is past the queue: nothing left to land

	result, err := Reconcile(ctx, ws, snap)
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Phase != string(project.PhaseIntegrate) {
		t.Fatalf("phase = %s, want INTEGRATE so the finished queue can be signed off", result.Snapshot.Phase)
	}
}

// TestStepReconcileReportsAnInterruptedMergeWithoutPromotingTheStep keeps Austin's
// mid-step merge of Tony's work from being read as a finished integration: the
// step stays where it was and the agent is told to finish the merge.
func TestStepReconcileReportsAnInterruptedMergeWithoutPromotingTheStep(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)

	landed := committed(t, set.Austin.Path, "step1.txt", "one\n")
	committed(t, set.Austin.Path, "conflict.txt", "austin\n")
	committed(t, set.Tony.Path, "conflict.txt", "tony\n")
	runFails(t, set.Austin.Path, "git", "merge", set.Tony.Branch)
	state, err := ws.MergeState(ctx, protocol.Austin)
	if err != nil {
		t.Fatal(err)
	}
	if !state.InProgress {
		t.Fatalf("test setup did not leave a merge in progress: %+v", state)
	}
	writeFile(t, set.Tony.Path, "scratch.txt", "wip\n")

	snap := stepSnapshotWithLanded(set, repo, landed)
	result, err := Reconcile(ctx, ws, snap)
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Phase != string(project.PhaseExecute) {
		t.Fatalf("phase = %s, want EXECUTE: an in-flight merge is not a finished step", result.Snapshot.Phase)
	}
	if result.Report.IntegrationRecovered || result.Snapshot.Integration.Started {
		t.Fatalf("a per-step merge was recorded as an integration: %+v", result.Snapshot.Integration)
	}
	if !hasNote(result.Report.Notes, "interrupted merge") {
		t.Fatalf("the interrupted merge was not reported: %v", result.Report.Notes)
	}
}
