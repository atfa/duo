package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

// These tests cover the delivery transaction's two crash boundaries: a delivery
// that is retried after it already happened, and one that was interrupted after
// Git moved but before the checkpoint was written. Both must be idempotent — a
// resumed or retried session may never move the user's branch twice.

// deliveryFixture is one repository plus the durable record for its artifact,
// with no agents: the transaction itself is what is under test.
type deliveryFixture struct {
	repo   string
	base   string
	branch string
	final  string
	store  *sessionstore.Store
	snap   sessionstore.Snapshot
	ws     *workspace.GitManager
}

func newDeliveryFixture(t *testing.T) *deliveryFixture {
	t.Helper()
	repo := gitRepo(t)
	base := headOf(t, repo)
	branch := "main"

	runGit(t, repo, "checkout", "-q", "-b", "duo/deliver/austin")
	if err := os.WriteFile(filepath.Join(repo, "result.md"), []byte("hello Duo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "result.md")
	runGit(t, repo, "commit", "-q", "-m", "add result.md")
	final := headOf(t, repo)
	runGit(t, repo, "checkout", "-q", branch)

	store, err := sessionstore.New(t.TempDir(), "repo-1", "sess-delivery")
	if err != nil {
		t.Fatal(err)
	}
	snap := sessionstore.Snapshot{
		SchemaVersion: sessionstore.SchemaVersion,
		SessionID:     "sess-delivery",
		RepoID:        "repo-1",
		Repository:    repo,
		BaseBranch:    branch,
		BaseCommit:    base,
		Phase:         string(project.PhaseIntegrate),
		Mode:          project.ModeGoal,
		Ready:         map[protocol.AgentID]bool{protocol.Austin: true, protocol.Tony: true},
		Evidence:      map[protocol.AgentID]string{protocol.Austin: final, protocol.Tony: final},
		Worktrees: map[protocol.AgentID]sessionstore.Worktree{
			protocol.Austin: {Branch: "duo/deliver/austin"},
			protocol.Tony:   {Branch: "duo/deliver/tony"},
		},
		Integration: sessionstore.Integration{Started: true, Head: final},
		CreatedAt:   time.Now().UTC(),
	}
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}

	ws := workspace.NewGitManager(workspace.GitConfig{Repository: repo})
	ws.Restore(workspace.Set{Repository: repo, BaseBranch: branch, BaseCommit: base})
	return &deliveryFixture{repo: repo, base: base, branch: branch, final: final, store: store, snap: snap, ws: ws}
}

// commitCount is how many commits the user's branch holds, which is how a second
// delivery would show up if it moved the branch again.
func commitCount(t *testing.T, repo string) int {
	t.Helper()
	cmd := exec.Command("git", "rev-list", "--count", "HEAD")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-list --count HEAD in %s: %v", repo, err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("count commits: %v", err)
	}
	return count
}

// TestDeliveryRetryDoesNotMoveTheBranchTwice is the retry contract: applying the
// same finished session again reports success, but the branch, its commit count
// and the recorded checkpoint stay exactly as the first delivery left them.
func TestDeliveryRetryDoesNotMoveTheBranchTwice(t *testing.T) {
	ctx := context.Background()
	f := newDeliveryFixture(t)

	first, err := DeliverAndPersist(ctx, f.store, f.ws, f.snap)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Applied {
		t.Fatalf("first delivery did not apply: %q", first.Reason)
	}
	if got := headOf(t, f.repo); got != f.final {
		t.Fatalf("the user's branch is at %s, want %s", got, f.final)
	}
	commits := commitCount(t, f.repo)
	if commits != 2 {
		t.Fatalf("commit count = %d, want the base commit plus the artifact", commits)
	}
	firstDelivery := first.Snapshot.Delivery
	if !firstDelivery.Applied() || firstDelivery.AppliedHead != f.final || firstDelivery.CompletedAt == nil {
		t.Fatalf("first checkpoint = %+v", firstDelivery)
	}

	// A retry loads the record again, exactly as `duo apply` does.
	reloaded, err := f.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	second, err := DeliverAndPersist(ctx, f.store, f.ws, reloaded)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Applied {
		t.Fatalf("a retry of a delivered session must still report success: %q", second.Reason)
	}
	if got := headOf(t, f.repo); got != f.final {
		t.Fatalf("the retry moved the user's branch to %s", got)
	}
	if got := commitCount(t, f.repo); got != commits {
		t.Fatalf("the retry added commits: %d, want %d", got, commits)
	}
	retried := second.Snapshot.Delivery
	if retried.AppliedHead != firstDelivery.AppliedHead {
		t.Fatalf("the retry changed the applied head: %s, want %s", retried.AppliedHead, firstDelivery.AppliedHead)
	}
	if retried.CompletedAt == nil || !retried.CompletedAt.Equal(*firstDelivery.CompletedAt) {
		t.Fatalf("the retry rewrote the completion time: %v, want %v", retried.CompletedAt, firstDelivery.CompletedAt)
	}
}

// TestDeliveryCrashWindowReconcilesOnResume simulates kill -9 between the
// fast-forward and the checkpoint write: Git already holds the artifact while the
// record still says pending. Resume must finish the transaction without moving
// the branch again, and a second resume must be a no-op.
func TestDeliveryCrashWindowReconcilesOnResume(t *testing.T) {
	ctx := context.Background()
	f := newDeliveryFixture(t)

	// The dangerous window: Git moved, the checkpoint did not.
	runGit(t, f.repo, "merge", "--ff-only", f.final)
	commits := commitCount(t, f.repo)
	if got := headOf(t, f.repo); got != f.final {
		t.Fatalf("setup: the branch is at %s, want %s", got, f.final)
	}

	pending := f.snap
	pending.Delivery = sessionstore.Delivery{
		Status:       sessionstore.DeliveryPending,
		FinalHead:    f.final,
		FinalBranch:  "duo/deliver/austin",
		TargetRepo:   f.repo,
		TargetBranch: f.branch,
	}
	if err := f.store.Save(pending); err != nil {
		t.Fatal(err)
	}
	journal := f.store.OpenEvents()

	outcome, stop, err := ReconcileDelivery(ctx, f.store, f.ws, journal, pending)
	if err != nil {
		t.Fatal(err)
	}
	if !stop || !outcome.Applied {
		t.Fatalf("the crash window was not reconciled: stop=%t applied=%t reason=%q", stop, outcome.Applied, outcome.Reason)
	}
	if got := headOf(t, f.repo); got != f.final {
		t.Fatalf("reconciliation moved the branch to %s", got)
	}
	if got := commitCount(t, f.repo); got != commits {
		t.Fatalf("reconciliation added commits: %d, want %d", got, commits)
	}

	reconciled, err := f.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Phase != string(project.PhaseDone) {
		t.Fatalf("reconciled phase = %s, want DONE", reconciled.Phase)
	}
	if !reconciled.Delivery.Applied() || reconciled.Delivery.AppliedHead != f.final {
		t.Fatalf("reconciled checkpoint = %+v", reconciled.Delivery)
	}

	// A second resume of the now-finished session has nothing left to reconcile:
	// it reports neither a stop nor a delivery, and changes nothing at all.
	again, stop, err := ReconcileDelivery(ctx, f.store, f.ws, journal, reconciled)
	if err != nil {
		t.Fatal(err)
	}
	if stop || again.Applied {
		t.Fatalf("a finished session still reconciled something: stop=%t applied=%t", stop, again.Applied)
	}
	if got := headOf(t, f.repo); got != f.final {
		t.Fatalf("a second reconciliation moved the branch to %s", got)
	}
	if got := commitCount(t, f.repo); got != commits {
		t.Fatalf("a second reconciliation added commits: %d, want %d", got, commits)
	}
	reread, err := f.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reread.Phase != reconciled.Phase || reread.Delivery.AppliedHead != reconciled.Delivery.AppliedHead {
		t.Fatalf("a second reconciliation rewrote the checkpoint: %+v", reread.Delivery)
	}
	if reread.Delivery.CompletedAt == nil || !reread.Delivery.CompletedAt.Equal(*reconciled.Delivery.CompletedAt) {
		t.Fatalf("a second reconciliation rewrote the completion time")
	}
}
