package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/delivery"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/recovery"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

// TestReconcileDeliveryReconcilesCrashWindow covers the central durability
// requirement: the fast-forward succeeded but Duo crashed before recording it.
// Reconciliation observes that the original HEAD already equals FinalHead and
// completes the delivery plus the DONE transition idempotently.
func TestReconcileDeliveryReconcilesCrashWindow(t *testing.T) {
	ctx := context.Background()
	repo := newDeliveryRepo(t)
	base := deliveryHead(t, repo)
	baseBranch := deliveryBranch(t, repo)
	final := makeFinal(t, repo, "duo/crash/austin", "result.md", "hello Duo\n")

	// Simulate the dangerous window: git already moved, state.json has not.
	deliveryGit(t, repo, "merge", "--ff-only", final)

	store := newDeliveryStore(t)
	snap := deliverySnapshot(repo, base, baseBranch, final)
	snap.Phase = string(project.PhaseIntegrate)
	snap.Delivery = sessionstore.Delivery{
		Status:       sessionstore.DeliveryPending,
		FinalHead:    final,
		FinalBranch:  "duo/crash/austin",
		TargetRepo:   repo,
		TargetBranch: baseBranch,
	}
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}

	ws := workspace.NewGitManager(workspace.GitConfig{Repository: repo})
	ws.Restore(workspace.Set{Repository: repo, BaseBranch: baseBranch, BaseCommit: base})

	outcome, stop, err := reconcileDelivery(ctx, store, ws, store.OpenEvents(), snap)
	if err != nil {
		t.Fatal(err)
	}
	if !stop || !outcome.Applied {
		t.Fatalf("crash window was not reconciled: stop=%t outcome=%+v", stop, outcome)
	}
	if outcome.Snapshot.Phase != string(project.PhaseDone) {
		t.Fatalf("phase = %s, want DONE", outcome.Snapshot.Phase)
	}

	persisted, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Delivery.Status != sessionstore.DeliveryApplied {
		t.Fatalf("persisted delivery = %+v, want applied", persisted.Delivery)
	}
	if !persisted.Ready[protocol.Austin] || !persisted.Ready[protocol.Tony] {
		t.Fatalf("reconciled DONE dropped signatures: %+v", persisted.Ready)
	}
	if got := deliveryHead(t, repo); got != final {
		t.Fatalf("original HEAD = %s, want %s", got, final)
	}
}

func TestDeliverAndPersistDoesNotRegressAppliedCheckpoint(t *testing.T) {
	store := newDeliveryStore(t)
	snap := sessionstore.Snapshot{
		SessionID: "delivery-test",
		Phase:     string(project.PhaseDone),
		Delivery:  sessionstore.Delivery{Status: sessionstore.DeliveryApplied, FinalHead: "final", AppliedHead: "final"},
	}
	ws := workspace.NewGitManager(workspace.GitConfig{})
	outcome, err := deliverAndPersistTo(context.Background(), store, ws, snap, session.DeliveryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Applied || !outcome.Snapshot.Delivery.Applied() {
		t.Fatalf("applied delivery regressed: %+v", outcome.Snapshot.Delivery)
	}
}

// TestReconcileDeliveryAppliesLegacyDoneSession covers v0.4.0 snapshots that
// were marked DONE without any delivery: `duo apply` still hands the artifact
// back using the recorded integration head.
func TestReconcileDeliveryAppliesLegacyDoneSession(t *testing.T) {
	ctx := context.Background()
	repo := newDeliveryRepo(t)
	base := deliveryHead(t, repo)
	baseBranch := deliveryBranch(t, repo)
	final := makeFinal(t, repo, "duo/legacy/austin", "result.md", "hello Duo\n")

	store := newDeliveryStore(t)
	snap := deliverySnapshot(repo, base, baseBranch, final)
	snap.Phase = string(project.PhaseDone)
	snap.Delivery = sessionstore.Delivery{} // v0.4.0 zero value
	snap.Integration = sessionstore.Integration{Started: true, Head: final}
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}

	ws := workspace.NewGitManager(workspace.GitConfig{Repository: repo})
	ws.Restore(workspace.Set{Repository: repo, BaseBranch: baseBranch, BaseCommit: base})

	outcome, stop, err := reconcileDelivery(ctx, store, ws, store.OpenEvents(), snap)
	if err != nil {
		t.Fatal(err)
	}
	if !stop || !outcome.Applied {
		t.Fatalf("legacy DONE session was not delivered: stop=%t outcome=%+v", stop, outcome)
	}
	if outcome.Snapshot.Phase != string(project.PhaseDone) {
		t.Fatalf("legacy apply must not change the DONE phase, got %s", outcome.Snapshot.Phase)
	}
	if got := deliveryHead(t, repo); got != final {
		t.Fatalf("original HEAD = %s, want %s", got, final)
	}
	if _, err := os.Stat(filepath.Join(repo, "result.md")); err != nil {
		t.Fatalf("result.md was not delivered: %v", err)
	}

	persisted, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Delivery.Status != sessionstore.DeliveryApplied || persisted.Delivery.AppliedHead != final {
		t.Fatalf("legacy delivery not recorded: %+v", persisted.Delivery)
	}
}

// TestReconcileDeliveryLeavesDirtyRepositoryPending proves a changed original
// repository keeps the session in INTEGRATE with both signatures and never
// touches the user's files.
func TestReconcileDeliveryLeavesDirtyRepositoryPending(t *testing.T) {
	ctx := context.Background()
	repo := newDeliveryRepo(t)
	base := deliveryHead(t, repo)
	baseBranch := deliveryBranch(t, repo)
	final := makeFinal(t, repo, "duo/dirty/austin", "result.md", "hello Duo\n")

	if err := os.WriteFile(filepath.Join(repo, "user-work.md"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := newDeliveryStore(t)
	snap := deliverySnapshot(repo, base, baseBranch, final)
	snap.Phase = string(project.PhaseIntegrate)
	snap.Integration = sessionstore.Integration{Started: true, Head: final}
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}

	ws := workspace.NewGitManager(workspace.GitConfig{Repository: repo})
	ws.Restore(workspace.Set{Repository: repo, BaseBranch: baseBranch, BaseCommit: base})

	outcome, stop, err := reconcileDelivery(ctx, store, ws, store.OpenEvents(), snap)
	if err != nil {
		t.Fatal(err)
	}
	if !stop {
		t.Fatal("blocked delivery must stop agent startup")
	}
	if outcome.Applied {
		t.Fatal("dirty repository must not be delivered")
	}
	if outcome.Snapshot.Phase != string(project.PhaseIntegrate) {
		t.Fatalf("phase = %s, want INTEGRATE", outcome.Snapshot.Phase)
	}
	if !outcome.Snapshot.Ready[protocol.Austin] || !outcome.Snapshot.Ready[protocol.Tony] {
		t.Fatalf("pending delivery dropped signatures: %+v", outcome.Snapshot.Ready)
	}
	if !strings.Contains(outcome.Reason, "uncommitted") {
		t.Fatalf("reason = %q", outcome.Reason)
	}
	if got := deliveryHead(t, repo); got != base {
		t.Fatalf("original HEAD moved to %s while pending", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "user-work.md")); err != nil {
		t.Fatalf("user file was disturbed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "result.md")); !os.IsNotExist(err) {
		t.Fatalf("result.md must not exist while pending: %v", err)
	}
}

// TestResolveFinalHeadFallsBackToAustinBranch covers a snapshot that lost its
// integration head: the Austin branch ref in the original repository is proven
// against Git rather than guessed.
func TestResolveFinalHeadFallsBackToAustinBranch(t *testing.T) {
	ctx := context.Background()
	repo := newDeliveryRepo(t)
	base := deliveryHead(t, repo)
	baseBranch := deliveryBranch(t, repo)
	final := makeFinal(t, repo, "duo/fallback/austin", "result.md", "hello Duo\n")

	snap := deliverySnapshot(repo, base, baseBranch, final)
	snap.Integration = sessionstore.Integration{}
	snap.Delivery = sessionstore.Delivery{}
	snap.Worktrees = map[protocol.AgentID]sessionstore.Worktree{
		protocol.Austin: {Path: "", Branch: "duo/fallback/austin"},
	}

	ws := workspace.NewGitManager(workspace.GitConfig{Repository: repo})
	ws.Restore(setFromSnapshot(snap))

	head, err := resolveFinalHead(ctx, ws, snap)
	if err != nil {
		t.Fatal(err)
	}
	if head != final {
		t.Fatalf("resolveFinalHead = %s, want %s", head, final)
	}
}

func newDeliveryRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	deliveryGit(t, repo, "init")
	deliveryGit(t, repo, "config", "user.email", "duo@example.invalid")
	deliveryGit(t, repo, "config", "user.name", "Duo Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deliveryGit(t, repo, "add", "README.md")
	deliveryGit(t, repo, "commit", "-m", "base")
	return repo
}

func makeFinal(t *testing.T, repo, branchName, file, content string) string {
	t.Helper()
	baseBranch := deliveryBranch(t, repo)
	deliveryGit(t, repo, "checkout", "-b", branchName)
	if err := os.WriteFile(filepath.Join(repo, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	deliveryGit(t, repo, "add", file)
	deliveryGit(t, repo, "commit", "-m", "add "+file)
	final := deliveryHead(t, repo)
	deliveryGit(t, repo, "checkout", baseBranch)
	return final
}

func newDeliveryStore(t *testing.T) *sessionstore.Store {
	t.Helper()
	store, err := sessionstore.New(t.TempDir(), "repo-1", "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func deliverySnapshot(repo, base, baseBranch, final string) sessionstore.Snapshot {
	return sessionstore.Snapshot{
		SchemaVersion: sessionstore.SchemaVersion,
		SessionID:     "sess-1",
		RepoID:        "repo-1",
		Repository:    repo,
		BaseBranch:    baseBranch,
		BaseCommit:    base,
		Phase:         string(project.PhaseIntegrate),
		Ready: map[protocol.AgentID]bool{
			protocol.Austin: true,
			protocol.Tony:   true,
		},
		Evidence: map[protocol.AgentID]string{
			protocol.Austin: final,
			protocol.Tony:   final,
		},
		Worktrees: map[protocol.AgentID]sessionstore.Worktree{
			protocol.Austin: {Branch: "duo/x/austin"},
			protocol.Tony:   {Branch: "duo/x/tony"},
		},
		Integration: sessionstore.Integration{Started: true, Head: final},
		CreatedAt:   time.Now().UTC(),
	}
}

func deliveryHead(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(deliveryGit(t, dir, "rev-parse", "HEAD"))
}

func deliveryBranch(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(deliveryGit(t, dir, "symbolic-ref", "--short", "HEAD"))
}

func deliveryGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

// TestApplyToNewBranchLeavesUserBranch is the protected-branch exit: the verified
// artifact lands on a branch of its own, while the branch the user is on, its HEAD
// and the working tree stay exactly as they were.
func TestApplyToNewBranchLeavesUserBranch(t *testing.T) {
	ctx := context.Background()
	repo := newDeliveryRepo(t)
	base := deliveryHead(t, repo)
	baseBranch := deliveryBranch(t, repo)
	final := makeFinal(t, repo, "duo/branch/austin", "result.md", "hello Duo\n")

	store := newDeliveryStore(t)
	snap := deliverySnapshot(repo, base, baseBranch, final)
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}
	ws := workspace.NewGitManager(workspace.GitConfig{Repository: repo})
	ws.Restore(workspace.Set{Repository: repo, BaseBranch: baseBranch, BaseCommit: base})

	// Duo never forces a branch: an existing branch at another commit is refused
	// and nothing moves.
	deliveryGit(t, repo, "branch", "duo/taken", base)
	outcome, err := deliverToNewBranch(ctx, store, ws, snap, "duo/taken", false)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Applied || !strings.Contains(outcome.Reason, "already exists") {
		t.Fatalf("an existing branch was not refused: applied=%t reason=%q", outcome.Applied, outcome.Reason)
	}
	if got := deliveryGit(t, repo, "rev-parse", "refs/heads/duo/taken"); strings.TrimSpace(got) != base {
		t.Fatalf("a refused delivery moved the existing branch to %s", strings.TrimSpace(got))
	}

	outcome, err = deliverToNewBranch(ctx, store, ws, snap, "duo/result", false)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Applied {
		t.Fatalf("delivery to a new branch failed: %q", outcome.Reason)
	}
	if got := deliveryBranch(t, repo); got != baseBranch {
		t.Fatalf("the user's branch changed to %q, want %q", got, baseBranch)
	}
	if got := deliveryHead(t, repo); got != base {
		t.Fatalf("the user's HEAD moved to %s, want %s", got, base)
	}
	if got := strings.TrimSpace(deliveryGit(t, repo, "rev-parse", "refs/heads/duo/result")); got != final {
		t.Fatalf("new branch is at %s, want the final artifact %s", got, final)
	}
	if dirty := strings.TrimSpace(deliveryGit(t, repo, "status", "--porcelain")); dirty != "" {
		t.Fatalf("delivery dirtied the working tree: %q", dirty)
	}

	persisted, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Delivery.Status != sessionstore.DeliveryApplied {
		t.Fatalf("persisted delivery = %+v, want applied", persisted.Delivery)
	}
	if persisted.Delivery.TargetBranch != "duo/result" || persisted.Delivery.AppliedHead != final {
		t.Fatalf("delivery record does not name the new branch: %+v", persisted.Delivery)
	}

	// A second apply is the idempotent no-op: the branch is already at the artifact.
	again, err := deliverToNewBranch(ctx, store, ws, persisted, "duo/result", false)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Applied {
		t.Fatalf("a repeated apply regressed: %q", again.Reason)
	}
	if got := strings.TrimSpace(deliveryGit(t, repo, "rev-parse", "refs/heads/duo/result")); got != final {
		t.Fatalf("a repeated apply moved the branch to %s", got)
	}
}

// TestApplyDefaultStillFastForwards keeps the no-flag behavior: the only default
// delivery is a local fast-forward of the branch Duo started on.
func TestApplyDefaultStillFastForwards(t *testing.T) {
	ctx := context.Background()
	repo := newDeliveryRepo(t)
	base := deliveryHead(t, repo)
	baseBranch := deliveryBranch(t, repo)
	final := makeFinal(t, repo, "duo/default/austin", "result.md", "hello Duo\n")

	store := newDeliveryStore(t)
	snap := deliverySnapshot(repo, base, baseBranch, final)
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}
	ws := workspace.NewGitManager(workspace.GitConfig{Repository: repo})
	ws.Restore(workspace.Set{Repository: repo, BaseBranch: baseBranch, BaseCommit: base})

	outcome, err := deliverAndPersistTo(ctx, store, ws, snap, session.DeliveryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Applied {
		t.Fatalf("default delivery failed: %q", outcome.Reason)
	}
	if got := deliveryBranch(t, repo); got != baseBranch {
		t.Fatalf("default delivery left the user on %q, want %q", got, baseBranch)
	}
	if got := deliveryHead(t, repo); got != final {
		t.Fatalf("default delivery did not fast-forward: HEAD=%s want %s", got, final)
	}
}

// newDeliveryRepoWithRemote gives the test repository a real upstream, so a push
// is exercised against Git rather than a stub.
func newDeliveryRepoWithRemote(t *testing.T) (repo, remote, branch string) {
	t.Helper()
	repo = newDeliveryRepo(t)
	remote = t.TempDir()
	deliveryGit(t, remote, "init", "--bare")
	deliveryGit(t, repo, "remote", "add", "origin", remote)
	branch = deliveryBranch(t, repo)
	deliveryGit(t, repo, "push", "-u", "origin", branch)
	return repo, remote, branch
}

// TestApplyPushFastForwardsRemote covers the exit a remote user needs: the local
// fast-forward is followed by a push, and the remote is read back to prove it.
func TestApplyPushFastForwardsRemote(t *testing.T) {
	ctx := context.Background()
	repo, remote, branch := newDeliveryRepoWithRemote(t)
	base := deliveryHead(t, repo)
	final := makeFinal(t, repo, "duo/push/austin", "result.md", "hello Duo\n")

	store := newDeliveryStore(t)
	snap := deliverySnapshot(repo, base, branch, final)
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}
	ws := workspace.NewGitManager(workspace.GitConfig{Repository: repo})
	ws.Restore(workspace.Set{Repository: repo, BaseBranch: branch, BaseCommit: base})

	outcome, err := deliverAndPersistTo(ctx, store, ws, snap, session.DeliveryOptions{Push: true})
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Applied || !outcome.Pushed || outcome.PushReason != "" {
		t.Fatalf("push delivery did not complete: %+v", outcome)
	}
	if got := deliveryHead(t, repo); got != final {
		t.Fatalf("local HEAD = %s, want %s", got, final)
	}
	if got := strings.TrimSpace(deliveryGit(t, remote, "rev-parse", "refs/heads/"+branch)); got != final {
		t.Fatalf("remote branch = %s, want %s", got, final)
	}
}

// TestApplyPushRefusesMovedRemote is the safety rule: when the remote has moved
// ahead, the delivery is refused before the local branch is touched, so neither
// side changes.
func TestApplyPushRefusesMovedRemote(t *testing.T) {
	ctx := context.Background()
	repo, remote, branch := newDeliveryRepoWithRemote(t)
	base := deliveryHead(t, repo)
	final := makeFinal(t, repo, "duo/push/austin", "result.md", "hello Duo\n")

	// Someone else pushes a commit onto the upstream branch, which Duo must not
	// overwrite.
	deliveryGit(t, repo, "checkout", "-b", "remote-move")
	if err := os.WriteFile(filepath.Join(repo, "theirs.md"), []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deliveryGit(t, repo, "add", "theirs.md")
	deliveryGit(t, repo, "commit", "-m", "someone else")
	moved := deliveryHead(t, repo)
	deliveryGit(t, repo, "push", "origin", "remote-move:"+branch)
	deliveryGit(t, repo, "checkout", branch)
	deliveryGit(t, repo, "branch", "-D", "remote-move")

	store := newDeliveryStore(t)
	snap := deliverySnapshot(repo, base, branch, final)
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}
	ws := workspace.NewGitManager(workspace.GitConfig{Repository: repo})
	ws.Restore(workspace.Set{Repository: repo, BaseBranch: branch, BaseCommit: base})

	outcome, err := deliverAndPersistTo(ctx, store, ws, snap, session.DeliveryOptions{Push: true})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Applied {
		t.Fatal("a delivery whose push would overwrite the remote must be refused")
	}
	if !strings.Contains(outcome.Reason, "moved") {
		t.Fatalf("refusal does not explain the moved remote: %q", outcome.Reason)
	}
	if got := deliveryHead(t, repo); got != base {
		t.Fatalf("local HEAD moved to %s, want it left at %s", got, base)
	}
	if got := strings.TrimSpace(deliveryGit(t, remote, "rev-parse", "refs/heads/"+branch)); got != moved {
		t.Fatalf("remote branch changed to %s, want it left at %s", got, moved)
	}
}

// TestApplyPRBodyContainsEvidenceChain is the review context a pull request
// carries: the plan version, the step queue, both signatures with their evidence
// SHAs, the verdict, and the diffstat — the chain Duo already holds.
func TestApplyPRBodyContainsEvidenceChain(t *testing.T) {
	snap := sessionstore.Snapshot{
		SessionID:   "sess-pr",
		Mode:        project.ModeStep,
		Plan:        "# Step Plan\n\nFix the delivery exits.",
		PlanVersion: 3,
		Steps: project.Steps{
			{Title: "T7 migrate the snapshot", Done: true, Policy: project.ModeFast, Head: "aaaa1111", DeliveryHead: "aaaa1111"},
			{Title: "T6 open a pull request", Policy: project.ModeGoal, Head: "bbbb2222"},
		},
		Ready:    map[protocol.AgentID]bool{protocol.Austin: true, protocol.Tony: true},
		Evidence: map[protocol.AgentID]string{protocol.Austin: "deadbeefcafe", protocol.Tony: "deadbeefcafe"},
		Notes:    map[protocol.AgentID]string{protocol.Austin: "T6 is ready"},
		Verification: &sessionstore.Verification{
			Status: string(project.VerificationPassed), Head: "bbbb2222", Note: "tests pass",
		},
	}
	changes := []delivery.Change{
		{Status: "M", Path: "cmd/duo/apply.go"},
		{Status: "A", Path: "cmd/duo/pr.go"},
	}

	body := prBody(snap, changes)
	for _, want := range []string{
		"Plan version 3.",
		"Fix the delivery exits.",
		"T7 migrate the snapshot",
		"T6 open a pull request",
		"[fast]",
		"[goal]",
		"landed aaaa1111",
		"deadbeefca",
		"signed",
		"T6 is ready",
		"verification: passed for `bbbb2222` — tests pass",
		"cmd/duo/apply.go",
		"cmd/duo/pr.go",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("pull request body is missing %q:\n%s", want, body)
		}
	}

	if title := prTitle(snap); title != "Step Plan" {
		t.Fatalf("title = %q, want the plan's first line", title)
	}
}

// TestApplyPRDegradesWithoutGh covers the documented degradation: no gh is not a
// failure, the branch is already pushed, and Duo hands over the same body.
func TestApplyPRDegradesWithoutGh(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var out bytes.Buffer
	err := openPullRequest(context.Background(), t.TempDir(), "main", "duo/pr/result", "Duo delivery", "## Plan\n\nPlan version 1.", &out)
	if err != nil {
		t.Fatalf("a missing gh must degrade instead of failing: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"gh is not installed",
		"duo/pr/result is pushed",
		"into main",
		"Plan version 1.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("degraded output is missing %q:\n%s", want, got)
		}
	}
}

// TestApplyPRRetryReportsTheRecordedDiff is the retry the gh-missing flow
// depends on: an artifact already landed on a branch, applied again so the pull
// request can be opened, must still report the delivery's diffstat and the branch
// it really landed on.
func TestApplyPRRetryReportsTheRecordedDiff(t *testing.T) {
	ctx := context.Background()
	repo := newDeliveryRepo(t)
	base := deliveryHead(t, repo)
	baseBranch := deliveryBranch(t, repo)
	final := makeFinal(t, repo, "duo/pr-retry/austin", "result.md", "hello Duo\n")

	store := newDeliveryStore(t)
	snap := deliverySnapshot(repo, base, baseBranch, final)
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}
	ws := workspace.NewGitManager(workspace.GitConfig{Repository: repo})
	ws.Restore(workspace.Set{Repository: repo, BaseBranch: baseBranch, BaseCommit: base})

	first, err := deliverToNewBranch(ctx, store, ws, snap, "feat/x", false)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Applied {
		t.Fatalf("first delivery failed: %q", first.Reason)
	}
	persisted, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}

	// The retry carries a fresh default branch name, exactly as `duo apply <id>
	// --pr` does without --branch.
	retried, err := deliverToNewBranch(ctx, store, ws, persisted, "duo/sess-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if !retried.Applied {
		t.Fatalf("retry regressed: %q", retried.Reason)
	}
	if len(retried.Result.Changes) == 0 {
		t.Fatal("the retry reported no changes, so the pull request body would claim an empty delivery")
	}
	body := prBody(retried.Snapshot, retried.Result.Changes)
	if !strings.Contains(body, "result.md") {
		t.Fatalf("the retried body has no diffstat:\n%s", body)
	}
	if got := prHead(retried.Snapshot, "duo/sess-1"); got != "feat/x" {
		t.Fatalf("pull request head = %q, want the branch the artifact landed on", got)
	}
}

// TestPRHeadPrefersTheLandedBranch pins the fallback: only a record without a
// target branch may use the freshly computed name.
func TestPRHeadPrefersTheLandedBranch(t *testing.T) {
	snap := sessionstore.Snapshot{Delivery: sessionstore.Delivery{TargetBranch: "feat/x"}}
	if got := prHead(snap, "duo/sess-1"); got != "feat/x" {
		t.Fatalf("prHead = %q, want the recorded branch", got)
	}
	empty := sessionstore.Snapshot{}
	if got := prHead(empty, "duo/sess-1"); got != "duo/sess-1" {
		t.Fatalf("prHead = %q, want the fallback for a record without a branch", got)
	}
}

// TestStatusJSONMatchesDuoStatus is the single-source rule: `duo status --json`
// reports the same phase, mode, plan version, queue, signatures and per-agent Git
// evidence that duo_status answers with, because both are composed from the
// project snapshot, the recorded workspace and each agent's Git status.
func TestStatusJSONMatchesDuoStatus(t *testing.T) {
	ctx := context.Background()
	repo := newDeliveryRepo(t)
	base := deliveryHead(t, repo)
	baseBranch := deliveryBranch(t, repo)

	austinPath := filepath.Join(t.TempDir(), "austin")
	deliveryGit(t, repo, "worktree", "add", "-b", "duo/s/austin", austinPath)
	if err := os.WriteFile(filepath.Join(austinPath, "austin.md"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deliveryGit(t, austinPath, "add", "austin.md")
	deliveryGit(t, austinPath, "commit", "-m", "austin work")
	austinHead := deliveryHead(t, austinPath)

	tonyPath := filepath.Join(t.TempDir(), "tony")
	deliveryGit(t, repo, "worktree", "add", "-b", "duo/s/tony", tonyPath)

	snap := sessionstore.Snapshot{
		SchemaVersion: sessionstore.SchemaVersion,
		SessionID:     "sess-status",
		RepoID:        "repo-1",
		Repository:    repo,
		BaseBranch:    baseBranch,
		BaseCommit:    base,
		Phase:         string(project.PhaseExecute),
		Mode:          project.ModeStep,
		Plan:          "the plan",
		PlanVersion:   4,
		Step:          2,
		Steps: project.Steps{
			{Title: "one", Done: true, Policy: project.ModeFast, Head: austinHead, DeliveryHead: austinHead},
			{Title: "two", Policy: project.ModeGoal},
		},
		Ready:    map[protocol.AgentID]bool{protocol.Austin: true},
		Evidence: map[protocol.AgentID]string{protocol.Austin: austinHead},
		Worktrees: map[protocol.AgentID]sessionstore.Worktree{
			protocol.Austin: {Path: austinPath, Branch: "duo/s/austin"},
			protocol.Tony:   {Path: tonyPath, Branch: "duo/s/tony"},
		},
	}

	report, err := buildStatusReport(ctx, snap)
	if err != nil {
		t.Fatal(err)
	}
	if report.Phase != "EXECUTE" || report.Mode != "step" || report.PlanVersion != 4 || report.Step != 2 {
		t.Fatalf("workflow state does not match the snapshot: %+v", report)
	}
	if !report.Ready[protocol.Austin] || report.Evidence[protocol.Austin] != austinHead {
		t.Fatalf("signatures do not match the snapshot: %+v %+v", report.Ready, report.Evidence)
	}
	if len(report.Steps) != 2 || report.Steps[1].EffectivePolicy() != project.ModeGoal {
		t.Fatalf("queue does not match the snapshot: %+v", report.Steps)
	}
	if got := report.Agents[protocol.Austin]; got.Head != austinHead || got.Ahead != 1 || got.Dirty || got.Branch != "duo/s/austin" {
		t.Fatalf("austin Git evidence = %+v", got)
	}
	if got := report.Agents[protocol.Tony]; got.Head != base || got.Ahead != 0 || got.Dirty {
		t.Fatalf("tony Git evidence = %+v", got)
	}

	// The text is duo_status's own answer: the project snapshot, the recorded
	// workspace and each agent's status, in that order.
	set := session.SetFromSnapshot(snap)
	ws := workspace.NewGitManager(workspace.GitConfig{Repository: repo})
	ws.Restore(set)
	want := []string{recovery.ProjectSnapshot(snap).String(), set.String()}
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		status, err := ws.Status(ctx, agent)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, status.String())
	}
	if report.StatusText != strings.Join(want, "\n") {
		t.Fatalf("status text is not duo_status's answer:\n got: %s\nwant: %s", report.StatusText, strings.Join(want, "\n"))
	}

	// The JSON surface carries the same facts for a script.
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Phase       string                   `json:"phase"`
		Mode        string                   `json:"mode"`
		PlanVersion int                      `json:"planVersion"`
		Steps       []struct{ Title string } `json:"steps"`
		Ready       map[string]bool          `json:"ready"`
		Agents      map[string]struct {
			Head  string `json:"head"`
			Dirty bool   `json:"dirty"`
			Ahead int    `json:"ahead"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Phase != "EXECUTE" || decoded.Mode != "step" || decoded.PlanVersion != 4 {
		t.Fatalf("encoded status lost the workflow: %s", encoded)
	}
	if len(decoded.Steps) != 2 || !decoded.Ready["Austin"] {
		t.Fatalf("encoded status lost the queue or signatures: %s", encoded)
	}
	if decoded.Agents["Austin"].Head != austinHead || decoded.Agents["Austin"].Ahead != 1 {
		t.Fatalf("encoded status lost the Git evidence: %s", encoded)
	}
}
