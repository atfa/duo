package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
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
	outcome, err := deliverAndPersist(context.Background(), store, ws, snap)
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
