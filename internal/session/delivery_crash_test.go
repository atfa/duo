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
	"github.com/atfa/duo/internal/recovery"
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

func TestLoopDeliveryRefusalAndRestart(t *testing.T) {
	ctx := context.Background()
	f := newDeliveryFixture(t)
	wt := filepath.Join(t.TempDir(), "austin")
	runGit(t, f.repo, "worktree", "add", "-q", wt, "duo/deliver/austin")
	f.ws.Restore(workspace.Set{Repository: f.repo, BaseBranch: f.branch, BaseCommit: f.base, Austin: workspace.Worktree{Path: wt, Branch: "duo/deliver/austin"}})
	f.snap.Mode = project.ModeLoop
	f.snap.Loop = project.LoopState{Finishing: true, Summary: "finished"}
	f.snap.Integration = sessionstore.Integration{Started: true, Head: f.base}
	f.snap.Worktrees[protocol.Austin] = sessionstore.Worktree{Path: wt, Branch: "duo/deliver/austin"}
	if err := os.WriteFile(filepath.Join(wt, "unfinished"), []byte("work"), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := DeliverAndPersist(ctx, f.store, f.ws, f.snap); err != nil || out.Applied || out.Reason == "" {
		t.Fatalf("dirty Austin worktree accepted: %+v %v", out, err)
	}
	if headOf(t, f.repo) != f.base {
		t.Fatal("dirty worktree moved target")
	}
	if err := os.Remove(filepath.Join(wt, "unfinished")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repo, "user-file"), []byte("user"), 0644); err != nil {
		t.Fatal(err)
	}
	outcome, err := DeliverAndPersist(ctx, f.store, f.ws, f.snap)
	if err != nil || outcome.Applied || outcome.Snapshot.Delivery.Status != sessionstore.DeliveryPending {
		t.Fatalf("expected pending refusal: %+v %v", outcome, err)
	}
	if headOf(t, f.repo) != f.base {
		t.Fatal("dirty target moved")
	}
	if err := os.Remove(filepath.Join(f.repo, "user-file")); err != nil {
		t.Fatal(err)
	}
	snap, err := f.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	outcome, stop, err := ReconcileDelivery(ctx, f.store, f.ws, f.store.OpenEvents(), snap)
	if err != nil || !stop || !outcome.Applied || outcome.Snapshot.Phase != string(project.PhaseDone) || headOf(t, f.repo) != f.final {
		t.Fatalf("resume failed: %+v stop=%v err=%v", outcome, stop, err)
	}
}

func TestGoalDeliveryUsesSignedCurrentBranchAndPinsPending(t *testing.T) {
	ctx := context.Background()
	for _, pending := range []bool{false, true} {
		t.Run(strconv.FormatBool(pending), func(t *testing.T) {
			f := newDeliveryFixture(t)
			runGit(t, f.repo, "checkout", "-q", "duo/deliver/austin")
			if err := os.WriteFile(filepath.Join(f.repo, "second"), []byte("new"), 0644); err != nil {
				t.Fatal(err)
			}
			runGit(t, f.repo, "add", "second")
			runGit(t, f.repo, "commit", "-q", "-m", "second")
			head := headOf(t, f.repo)
			runGit(t, f.repo, "checkout", "-q", "main")
			if _, err := DeliverAndPersist(ctx, f.store, f.ws, f.snap); err == nil || headOf(t, f.repo) != f.base {
				t.Fatal("old signatures must not deliver the new head")
			}
			f.snap.Evidence[protocol.Austin], f.snap.Evidence[protocol.Tony] = head, head
			if pending {
				f.snap.Delivery = sessionstore.Delivery{Status: sessionstore.DeliveryPending, FinalHead: f.final}
			}
			out, err := DeliverAndPersist(ctx, f.store, f.ws, f.snap)
			if pending {
				if err == nil || headOf(t, f.repo) != f.base {
					t.Fatal("stale checkpoint must be refused without moving the branch")
				}
				f.snap.Delivery.FinalHead = head
				out, err = DeliverAndPersist(ctx, f.store, f.ws, f.snap)
			}
			if err != nil || !out.Applied || headOf(t, f.repo) != head {
				t.Fatalf("delivery: %+v %v", out, err)
			}
		})
	}
}

func TestGoalStepFullRecoveryDeliversSignedHead(t *testing.T) {
	for _, mode := range []project.Mode{project.ModeGoal, project.ModeStep} {
		t.Run(string(mode), func(t *testing.T) {
			ctx := context.Background()
			repo := gitRepo(t)
			ws := workspace.NewGitManager(workspace.GitConfig{Repository: repo, Root: t.TempDir(), Session: "goal-step"})
			set, err := ws.Prepare(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(set.Austin.Path, "change"), []byte("work"), 0644); err != nil {
				t.Fatal(err)
			}
			runGit(t, set.Austin.Path, "add", "change")
			runGit(t, set.Austin.Path, "commit", "-q", "-m", "change")
			head := headOf(t, set.Austin.Path)
			snap := recovery.Compose(recovery.ComposeInput{SessionID: "goal-step", RepoID: "repo", Repository: repo, BaseBranch: set.BaseBranch, BaseCommit: set.BaseCommit, Worktrees: set, Project: project.Snapshot{Mode: project.ModeStep, Phase: project.PhaseIntegrate, Started: true, Step: 1, Steps: project.Steps{{Title: "goal", Policy: project.ModeGoal}, {Title: "next"}}, Ready: map[protocol.AgentID]bool{protocol.Austin: true, protocol.Tony: true}, Evidence: map[protocol.AgentID]string{protocol.Austin: head, protocol.Tony: head}}})
			snap.Integration = sessionstore.Integration{Started: true, Head: set.BaseCommit}
			if mode == project.ModeStep {
				snap.Delivery = sessionstore.Delivery{Status: sessionstore.DeliveryPending, FinalHead: head}
			}
			if mode == project.ModeGoal {
				snap.Mode = mode
				snap.Steps = nil
				snap.Step = 0
			}
			stale := snap
			stale.Evidence = map[protocol.AgentID]string{protocol.Austin: set.BaseCommit, protocol.Tony: head}
			stale.Ready = map[protocol.AgentID]bool{protocol.Austin: true, protocol.Tony: true}
			revoked, err := recovery.Reconcile(ctx, ws, stale)
			if err != nil || revoked.Snapshot.Ready[protocol.Austin] {
				t.Fatalf("stale signature not revoked: %+v %v", revoked, err)
			}
			result, err := recovery.Reconcile(ctx, ws, snap)
			if err != nil {
				t.Fatal(err)
			}
			if result.Snapshot.Phase != string(project.PhaseIntegrate) || !result.Snapshot.Ready[protocol.Austin] || !result.Snapshot.Ready[protocol.Tony] {
				t.Fatalf("recovery revoked valid goal: %+v", result.Snapshot)
			}
			store, err := sessionstore.New(t.TempDir(), "repo", "goal-step")
			if err != nil {
				t.Fatal(err)
			}
			out, stop, err := ReconcileDelivery(ctx, store, ws, store.OpenEvents(), result.Snapshot)
			if err != nil || stop != (mode == project.ModeGoal) || !out.Applied || headOf(t, repo) != head {
				t.Fatalf("reconcile delivery: %+v stop=%v err=%v", out, stop, err)
			}
		})
	}
}

func TestGoalStepAppliedCheckpointAdvancesWithoutSubmissionHead(t *testing.T) {
	f := newDeliveryFixture(t)
	f.snap.Mode = project.ModeStep
	f.snap.Step = 1
	f.snap.Steps = project.Steps{{Title: "goal", Policy: project.ModeGoal}, {Title: "next"}}
	f.snap.Delivery = sessionstore.Delivery{Status: sessionstore.DeliveryApplied, FinalHead: f.final, AppliedHead: f.final}
	if !f.snap.StepDeliveryIsCurrent() {
		t.Fatal("signed GOAL step must identify its applied checkpoint")
	}
	runGit(t, f.repo, "merge", "--ff-only", f.final)
	out, stop, err := ReconcileDelivery(context.Background(), f.store, f.ws, f.store.OpenEvents(), f.snap)
	if err != nil || stop || !out.Snapshot.Steps[0].Done || out.Snapshot.Step != 2 {
		t.Fatalf("reconcile: %+v stop=%v err=%v", out, stop, err)
	}
}

func TestGoalDeliveryFallsBackToSignedCommitOnlyWhenBranchIsMissing(t *testing.T) {
	ctx := context.Background()
	for _, branchExists := range []bool{false, true} {
		t.Run(strconv.FormatBool(branchExists), func(t *testing.T) {
			f := newDeliveryFixture(t)
			if branchExists {
				runGit(t, f.repo, "branch", "-f", "duo/deliver/austin", f.base)
			} else {
				runGit(t, f.repo, "branch", "-D", "duo/deliver/austin")
			}
			out, err := DeliverAndPersist(ctx, f.store, f.ws, f.snap)
			if branchExists {
				if err == nil || headOf(t, f.repo) != f.base {
					t.Fatal("existing mismatched branch must be refused")
				}
			} else if err != nil || !out.Applied || headOf(t, f.repo) != f.final {
				t.Fatalf("signed commit fallback failed: %+v %v", out, err)
			}
		})
	}
}
