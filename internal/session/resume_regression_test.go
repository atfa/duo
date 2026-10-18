package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/recovery"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

// The reported failure, end to end: a STEP session stopped while step 4 of 6 was
// under review came back in INTEGRATE — the phase that lands a step — and the
// delivery transaction read the session-level record (step 3's landing) as the
// current step's landing, so `duo resume` died with
//
//	deliver session ...: operation is not allowed in the current phase:
//	6b65ff3cdcb5 already landed as step 4
//
// and left the session persisted in INTEGRATE with an unverified step. The two
// states below are the two checkpoints that session produced; both must resume in
// REVIEW, land nothing, and refuse `duo apply`.

// resumeRepo is the user's repository as Duo leaves it mid-step: master carries the
// landed steps, Austin's worktree has the submitted step with Tony's work for it
// merged in, and Tony's worktree is on the landed step plus his own work.
type resumeRepo struct {
	ws     *workspace.GitManager
	set    workspace.Set
	repo   string
	landed []string // master HEAD of each landed step, oldest first
	step   string   // Austin's submitted HEAD for the step under the cursor
	tony   string   // Tony's HEAD merged into that submission
}

func resumeRepoFor(t *testing.T, id string) resumeRepo {
	t.Helper()
	ctx := context.Background()
	repo := t.TempDir()
	resumeGit(t, repo, "init", "-b", "master")
	resumeGit(t, repo, "config", "user.email", "duo@example.invalid")
	resumeGit(t, repo, "config", "user.name", "Duo Test")
	resumeCommit(t, repo, "base.txt", "base")

	ws := workspace.NewGitManager(workspace.GitConfig{
		Repository: repo,
		Root:       filepath.Join(t.TempDir(), "worktrees"),
		Session:    id,
	})
	set, err := ws.Prepare(ctx)
	if err != nil {
		t.Fatalf("prepare worktrees: %v", err)
	}

	f := resumeRepo{ws: ws, set: set, repo: repo}
	for i := 1; i <= 3; i++ {
		resumeCommit(t, set.Austin.Path, fmt.Sprintf("step%d.txt", i), fmt.Sprint(i))
		resumeGit(t, repo, "merge", "--ff-only", set.Austin.Branch)
		f.landed = append(f.landed, resumeRev(t, repo, "HEAD"))
	}
	resumeCommit(t, set.Austin.Path, "step4.txt", "four")
	// Tony works on the landed step, as the per-step sync leaves him.
	resumeGit(t, set.Tony.Path, "merge", "--ff-only", f.landed[2])
	f.tony = resumeCommit(t, set.Tony.Path, "step4-tony.txt", "tony")
	resumeGit(t, set.Austin.Path, "merge", "--no-edit", set.Tony.Branch)
	f.step = resumeRev(t, set.Austin.Path, "HEAD")
	return f
}

// snapshot is the checkpoint of that session at `phase`, with step 4 submitted and
// the delivery record left by step 3's landing. integration is the record the old
// recovery rule wrote from the per-step merge; the user's session has it.
func (f resumeRepo) snapshot(t *testing.T, id string, phase project.Phase, integration bool) sessionstore.Snapshot {
	t.Helper()
	steps := project.Steps{
		{Title: "one"}, {Title: "two"}, {Title: "three"},
		{Title: "four"}, {Title: "five"}, {Title: "six"},
	}
	for i := 0; i < 3; i++ {
		steps[i].Done = true
		steps[i].Head = f.landed[i]
		steps[i].DeliveryHead = f.landed[i]
		steps[i].Verification = project.Verification{Status: project.VerificationPassed, Head: f.landed[i]}
	}
	steps[3].Head = f.step

	snap := sessionstore.Snapshot{
		SessionID:   id,
		Mode:        project.ModeStep,
		Phase:       string(phase),
		Plan:        "the plan",
		PlanVersion: 1,
		Repository:  f.repo,
		BaseBranch:  "master",
		BaseCommit:  f.set.BaseCommit,
		Step:        4,
		Steps:       steps,
		Ready:       map[protocol.AgentID]bool{protocol.Austin: true, protocol.Tony: true},
		Evidence:    map[protocol.AgentID]string{protocol.Austin: "stale", protocol.Tony: "stale"},
		Notes:       map[protocol.AgentID]string{},
		Worktrees: map[protocol.AgentID]sessionstore.Worktree{
			protocol.Austin: {Path: f.set.Austin.Path, Branch: f.set.Austin.Branch},
			protocol.Tony:   {Path: f.set.Tony.Path, Branch: f.set.Tony.Branch},
		},
		Delivery: sessionstore.Delivery{
			Status:       sessionstore.DeliveryApplied,
			FinalHead:    f.landed[2],
			AppliedHead:  f.landed[2],
			FinalBranch:  f.set.Austin.Branch,
			TargetRepo:   f.repo,
			TargetBranch: "master",
		},
	}
	if integration {
		snap.Integration = sessionstore.Integration{Started: true, Head: f.step, MergedTony: f.tony}
	}
	return snap
}

func resumeGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func resumeCommit(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	resumeGit(t, dir, "add", name)
	resumeGit(t, dir, "commit", "-m", "add "+name)
	return resumeRev(t, dir, "HEAD")
}

func resumeRev(t *testing.T, dir, ref string) string {
	t.Helper()
	return strings.TrimSpace(resumeGit(t, dir, "rev-parse", ref))
}

// resumeStep4 is the step under the cursor: open, submitted, and not landed.
func resumeStep4(t *testing.T, snap sessionstore.Snapshot) project.Step {
	t.Helper()
	if snap.Step != 4 || len(snap.Steps) < 4 {
		t.Fatalf("cursor = %d of %d, want step 4", snap.Step, len(snap.Steps))
	}
	if snap.Steps[3].Done || snap.Steps[3].DeliveryHead != "" {
		t.Fatalf("step 4 must be open, got %+v", snap.Steps[3])
	}
	return snap.Steps[3]
}

// TestResumeStepInReviewLandsNothing is the session as the user stopped it. The
// per-step merge of Tony's work is not an integration merge, so recovery keeps the
// session in REVIEW, and the record of step 3's landing is not step 4's landing, so
// the delivery transaction has nothing to land.
func TestResumeStepInReviewLandsNothing(t *testing.T) {
	ctx := context.Background()
	const id = "20261018-105749-e5d4b68e"
	f := resumeRepoFor(t, id)
	store, err := sessionstore.New(t.TempDir(), "duo-test-f59fd836", id)
	if err != nil {
		t.Fatal(err)
	}
	snap := f.snapshot(t, id, project.PhaseReview, true)
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}

	result, err := recovery.Reconcile(ctx, f.ws, snap)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.Snapshot.Phase != string(project.PhaseReview) {
		t.Fatalf("phase = %s, want REVIEW: a step is gated by Tony's verdict", result.Snapshot.Phase)
	}
	if result.Report.IntegrationRecovered {
		t.Fatalf("recovery read the per-step merge as an integration: %s", result.Report.String())
	}
	if got := resumeStep4(t, result.Snapshot); got.Head != f.step {
		t.Fatalf("the submitted step HEAD was lost: %+v", got)
	}

	outcome, stop, err := ReconcileDelivery(ctx, store, f.ws, store.OpenEvents(), result.Snapshot)
	if err != nil {
		t.Fatalf("resume died on delivery: %v", err)
	}
	if stop {
		t.Fatal("resume ended the session: nothing was delivered")
	}
	assertNothingLanded(t, f, outcome.Snapshot)
}

// TestResumeStepInIntegrateReturnsToReview is the checkpoint the failed resume
// persisted: INTEGRATE, the phase that lands a step, with step 4 carrying no verdict
// at all. Recovery re-derives the gate, and the step is neither landed by resume nor
// deliverable by `duo apply`.
func TestResumeStepInIntegrateReturnsToReview(t *testing.T) {
	ctx := context.Background()
	const id = "20261018-105749-e5d4b68e"
	f := resumeRepoFor(t, id)
	store, err := sessionstore.New(t.TempDir(), "duo-test-f59fd836", id)
	if err != nil {
		t.Fatal(err)
	}
	snap := f.snapshot(t, id, project.PhaseIntegrate, true)
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}

	result, err := recovery.Reconcile(ctx, f.ws, snap)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.Snapshot.Phase != string(project.PhaseReview) {
		t.Fatalf("phase = %s, want REVIEW: an unverified step must not land", result.Snapshot.Phase)
	}
	step := resumeStep4(t, result.Snapshot)
	if step.Verification.Status != "" {
		t.Fatalf("a verdict that binds nothing survived resume: %+v", step.Verification)
	}

	outcome, stop, err := ReconcileDelivery(ctx, store, f.ws, store.OpenEvents(), result.Snapshot)
	if err != nil {
		t.Fatalf("resume died on delivery: %v", err)
	}
	if stop {
		t.Fatal("resume ended the session: nothing was delivered")
	}
	assertNothingLanded(t, f, outcome.Snapshot)
}

// TestApplyRefusesTheReportedIntegrateState is `duo apply` on the same checkpoint
// with no recovery in front of it: the unverified step is refused before Git is
// touched, so the user's master keeps only the steps that were reviewed.
func TestApplyRefusesTheReportedIntegrateState(t *testing.T) {
	ctx := context.Background()
	const id = "20261018-105749-e5d4b68e"
	f := resumeRepoFor(t, id)
	store, err := sessionstore.New(t.TempDir(), "duo-test-f59fd836", id)
	if err != nil {
		t.Fatal(err)
	}
	snap := f.snapshot(t, id, project.PhaseIntegrate, true)
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}

	_, _, err = ReconcileDelivery(ctx, store, f.ws, store.OpenEvents(), snap)
	if err == nil {
		t.Fatal("apply landed an unverified step")
	}
	if !strings.Contains(err.Error(), "no passed verdict") {
		t.Fatalf("apply error = %v; want the missing step verdict", err)
	}
	assertNothingLanded(t, f, persist(t, store, id))
}

// assertNothingLanded proves the user's repository is untouched and the session
// still describes step 4 as submitted, open, and undelivered.
func assertNothingLanded(t *testing.T, f resumeRepo, snap sessionstore.Snapshot) {
	t.Helper()
	if got := resumeRev(t, f.repo, "HEAD"); got != f.landed[2] {
		t.Fatalf("master = %s, want the step 3 landing %s: nothing was reviewed for step 4", got, f.landed[2])
	}
	step := resumeStep4(t, snap)
	if step.Head != f.step {
		t.Fatalf("step 4 = %+v, want it submitted at %s", step, f.step)
	}
	if !snap.Delivery.Applied() || snap.Delivery.FinalHead != f.landed[2] {
		t.Fatalf("the step 3 landing record was rewritten: %+v", snap.Delivery)
	}
}

func persist(t *testing.T, store *sessionstore.Store, id string) sessionstore.Snapshot {
	t.Helper()
	snap, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return snap
}
