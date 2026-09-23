package recovery

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

func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run(t, repo, "git", "init")
	run(t, repo, "git", "config", "user.email", "duo@example.invalid")
	run(t, repo, "git", "config", "user.name", "Duo Test")
	writeFile(t, repo, "README.md", "base\n")
	run(t, repo, "git", "add", "README.md")
	run(t, repo, "git", "commit", "-m", "base")
	return repo
}

func newFixture(t *testing.T) (*workspace.GitManager, workspace.Set, string) {
	t.Helper()
	repo := initRepo(t)
	ws := workspace.NewGitManager(workspace.GitConfig{
		Repository: repo,
		Root:       filepath.Join(t.TempDir(), "worktrees"),
		Session:    "recovery-test",
	})
	set, err := ws.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return ws, set, repo
}

func snapshotFor(set workspace.Set, repo string, phase project.Phase, planVersion int) sessionstore.Snapshot {
	return Compose(ComposeInput{
		DuoVersion: "v0.4.0-test",
		SessionID:  set.Session,
		RepoID:     "repo-test",
		Repository: repo,
		BaseBranch: set.BaseBranch,
		BaseCommit: set.BaseCommit,
		CreatedAt:  time.Now().UTC(),
		Project: project.Snapshot{
			Phase:       phase,
			Plan:        "shared plan",
			PlanVersion: planVersion,
			Started:     true,
		},
		Worktrees: set,
		PiSessions: map[protocol.AgentID]string{
			protocol.Austin: "11111111-1111-4111-8111-111111111111",
			protocol.Tony:   "22222222-2222-4222-8222-222222222222",
		},
	})
}

func signed(snap sessionstore.Snapshot, agent protocol.AgentID, evidence string) sessionstore.Snapshot {
	snap.Ready[agent] = true
	snap.Evidence[agent] = evidence
	snap.Notes[agent] = "signed"
	return snap
}

func committed(t *testing.T, dir, name, content string) string {
	t.Helper()
	writeFile(t, dir, name, content)
	run(t, dir, "git", "add", name)
	run(t, dir, "git", "commit", "-m", "add "+name)
	return strings.TrimSpace(run(t, dir, "git", "rev-parse", "HEAD"))
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

// runFails runs a command that is expected to exit non-zero, such as a
// conflicting `git merge`.
func runFails(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("%v unexpectedly succeeded:\n%s", args, out)
	}
}

func TestExpectedEvidenceMatchesPhaseRules(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)
	snap := snapshotFor(set, repo, project.PhasePlan, 4)

	got, err := ExpectedEvidence(ctx, ws, project.PhasePlan, protocol.Austin, 4)
	if err != nil || got != "plan-v4" {
		t.Fatalf("PLAN evidence = %q, %v", got, err)
	}

	austinHead := committed(t, set.Austin.Path, "austin.txt", "austin\n")
	tonyHead := committed(t, set.Tony.Path, "tony.txt", "tony\n")

	// EXECUTE signs the signer's own work.
	if got, err := ExpectedEvidence(ctx, ws, project.PhaseExecute, protocol.Austin, 1); err != nil || got != austinHead {
		t.Fatalf("EXECUTE Austin evidence = %q (%v), want %q", got, err, austinHead)
	}
	// REVIEW signs the peer's work, never your own.
	if got, err := ExpectedEvidence(ctx, ws, project.PhaseReview, protocol.Austin, 1); err != nil || got != tonyHead {
		t.Fatalf("REVIEW Austin evidence = %q (%v), want Tony's %q", got, err, tonyHead)
	}
	// INTEGRATE always signs Austin's integrated branch.
	if got, err := ExpectedEvidence(ctx, ws, project.PhaseIntegrate, protocol.Tony, 1); err != nil || got != austinHead {
		t.Fatalf("INTEGRATE evidence = %q (%v), want %q", got, err, austinHead)
	}

	// Unknown phases must not resolve to a meaningless signature.
	if _, err := ExpectedEvidence(ctx, ws, project.PhaseDone, protocol.Austin, 1); err == nil {
		t.Fatal("DONE must not be signable")
	}
	_ = snap
}

// TestReconcileKeepsCurrentSignature is the positive path: a valid signature
// must survive a resume untouched.
func TestReconcileKeepsCurrentSignature(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)
	head := committed(t, set.Austin.Path, "austin.txt", "austin\n")

	snap := signed(snapshotFor(set, repo, project.PhaseExecute, 1), protocol.Austin, head)
	result, err := Reconcile(ctx, ws, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Report.Revoked) != 0 {
		t.Fatalf("valid signature was revoked: %v", result.Report.Revoked)
	}
	if !result.Snapshot.Ready[protocol.Austin] || result.Snapshot.Evidence[protocol.Austin] != head {
		t.Fatalf("valid signature lost: %+v", result.Snapshot)
	}
	if result.Snapshot.Phase != string(project.PhaseExecute) {
		t.Fatalf("phase changed to %s", result.Snapshot.Phase)
	}
}

// TestReconcileRevokesStaleSignature is the core trust guarantee: after the
// reviewed work changed, the old signature must not be believed.
func TestReconcileRevokesStaleSignature(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)
	first := committed(t, set.Austin.Path, "austin.txt", "austin\n")

	snap := signed(snapshotFor(set, repo, project.PhaseExecute, 1), protocol.Austin, first)

	// The work moves on after the signature was recorded.
	second := committed(t, set.Austin.Path, "more.txt", "more\n")
	if second == first {
		t.Fatal("test setup did not produce a new commit")
	}

	result, err := Reconcile(ctx, ws, snap)
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Ready[protocol.Austin] {
		t.Fatal("stale signature is still considered ready")
	}
	if result.Snapshot.Evidence[protocol.Austin] != "" {
		t.Fatalf("stale evidence was kept: %q", result.Snapshot.Evidence[protocol.Austin])
	}
	if len(result.Report.Revoked) != 1 || result.Report.Revoked[0] != protocol.Austin {
		t.Fatalf("expected Austin revoked, got %v", result.Report.Revoked)
	}
	if !strings.Contains(result.Snapshot.Notes[protocol.Austin], "revoked") {
		t.Fatalf("revocation was not explained: %q", result.Snapshot.Notes[protocol.Austin])
	}
	// Recovery never rewinds the phase.
	if result.Snapshot.Phase != string(project.PhaseExecute) {
		t.Fatalf("phase moved backwards to %s", result.Snapshot.Phase)
	}
}

// TestReconcileRevokesStalePlanSignature covers the PLAN phase, where the
// signature is tied to the plan version rather than to Git.
func TestReconcileRevokesStalePlanSignature(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)

	// Signed against plan version 1, but the persisted plan is now version 2.
	snap := signed(snapshotFor(set, repo, project.PhasePlan, 2), protocol.Tony, "plan-v1")
	result, err := Reconcile(ctx, ws, snap)
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Ready[protocol.Tony] {
		t.Fatal("signature for an older plan version must be revoked")
	}
	if len(result.Report.Revoked) != 1 {
		t.Fatalf("expected one revocation, got %v", result.Report.Revoked)
	}

	// A signature for the current plan version stays valid.
	fresh := signed(snapshotFor(set, repo, project.PhasePlan, 2), protocol.Tony, "plan-v2")
	result, err = Reconcile(ctx, ws, fresh)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Snapshot.Ready[protocol.Tony] {
		t.Fatalf("current plan signature was revoked: %+v", result.Report)
	}
}

// TestReconcileDirtyWorktreeRevokesWithoutFailing covers requirement 9: an
// uncommitted worktree must not make resume fail, but its signature must go.
func TestReconcileDirtyWorktreeRevokesWithoutFailing(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)
	head := committed(t, set.Austin.Path, "austin.txt", "austin\n")

	snap := signed(snapshotFor(set, repo, project.PhaseExecute, 1), protocol.Austin, head)
	snap = signed(snap, protocol.Tony, head)
	snap.Ready[protocol.Tony] = true
	snap.Evidence[protocol.Tony] = head

	// Austin leaves uncommitted work behind when the crash happens.
	writeFile(t, set.Austin.Path, "half-finished.txt", "partial\n")

	result, err := Reconcile(ctx, ws, snap)
	if err != nil {
		t.Fatalf("dirty worktree must not fail recovery: %v", err)
	}
	if result.Snapshot.Ready[protocol.Austin] {
		t.Fatal("Austin's stale signature survived a dirty worktree")
	}
	if result.Snapshot.Phase != string(project.PhaseExecute) {
		t.Fatalf("dirty recovery changed the phase to %s", result.Snapshot.Phase)
	}
	// The uncommitted work is left exactly where it was: recovery repairs
	// bookkeeping, it never discards a user's files.
	if _, err := os.Stat(filepath.Join(set.Austin.Path, "half-finished.txt")); err != nil {
		t.Fatalf("recovery touched the worktree: %v", err)
	}
}

// TestReconcileDetectsInterruptedMerge covers requirement 10: a merge left in
// progress must be adopted, never repeated.
func TestReconcileDetectsInterruptedMerge(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)

	committed(t, set.Austin.Path, "conflict.txt", "austin\n")
	tonyHead := committed(t, set.Tony.Path, "conflict.txt", "tony\n")

	// Produce a real conflict: the merge stops with MERGE_HEAD set and the
	// working tree left mid-resolution, exactly as a crash would.
	runFails(t, set.Austin.Path, "git", "merge", set.Tony.Branch)
	state, err := ws.MergeState(ctx, protocol.Austin)
	if err != nil {
		t.Fatal(err)
	}
	if !state.InProgress || !state.Conflicted {
		t.Fatalf("test setup did not leave a conflicted merge: %+v", state)
	}
	austinHead := strings.TrimSpace(run(t, set.Austin.Path, "git", "rev-parse", "HEAD"))

	result, err := Reconcile(ctx, ws, snapshotFor(set, repo, project.PhaseReview, 1))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Report.IntegrationRecovered {
		t.Fatal("interrupted merge was not reported as recovered")
	}
	if result.Snapshot.Phase != string(project.PhaseIntegrate) {
		t.Fatalf("phase = %s, want INTEGRATE", result.Snapshot.Phase)
	}
	if !result.Snapshot.Integration.Started || !result.Snapshot.Integration.Conflicted {
		t.Fatalf("integration state not rebuilt: %+v", result.Snapshot.Integration)
	}
	if result.Snapshot.Integration.MergedTony != tonyHead {
		t.Fatalf("MergedTony = %q, want %q", result.Snapshot.Integration.MergedTony, tonyHead)
	}
	// The merge must not have been run a second time.
	if now := strings.TrimSpace(run(t, set.Austin.Path, "git", "rev-parse", "HEAD")); now != austinHead {
		t.Fatalf("recovery moved Austin's HEAD from %s to %s by merging again", austinHead, now)
	}
	if !strings.Contains(strings.Join(result.Report.Notes, " "), "will not merge again") {
		t.Fatalf("recovery did not explain the skipped merge: %v", result.Report.Notes)
	}
}

// TestReconcileDetectsCompletedMerge covers the crash window between `git merge`
// succeeding and the checkpoint being written.
func TestReconcileDetectsCompletedMerge(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)

	committed(t, set.Austin.Path, "austin.txt", "austin\n")
	tonyHead := committed(t, set.Tony.Path, "tony.txt", "tony\n")

	run(t, set.Austin.Path, "git", "merge", "--no-edit", set.Tony.Branch)
	austinHead := strings.TrimSpace(run(t, set.Austin.Path, "git", "rev-parse", "HEAD"))

	result, err := Reconcile(ctx, ws, snapshotFor(set, repo, project.PhaseReview, 1))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Report.IntegrationRecovered || result.Snapshot.Phase != string(project.PhaseIntegrate) {
		t.Fatalf("completed merge not detected: %s", result.Report.String())
	}
	if result.Snapshot.Integration.Head != austinHead || result.Snapshot.Integration.MergedTony != tonyHead {
		t.Fatalf("integration state = %+v", result.Snapshot.Integration)
	}
	if result.Snapshot.Integration.Conflicted {
		t.Fatal("a completed merge must not be marked conflicted")
	}

	// Re-running recovery must be idempotent: same phase, same integration.
	again, err := Reconcile(ctx, ws, result.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if again.Snapshot.Phase != result.Snapshot.Phase || again.Snapshot.Integration != result.Snapshot.Integration {
		t.Fatalf("recovery is not idempotent:\nfirst  %+v\nsecond %+v", result.Snapshot.Integration, again.Snapshot.Integration)
	}
}

// TestReconcileNeverMovesPhaseBackwards is the anti-"silently back to PLAN" rule.
func TestReconcileNeverMovesPhaseBackwards(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)

	cases := []struct {
		name  string
		phase project.Phase
	}{
		{name: "plan", phase: project.PhasePlan},
		{name: "execute", phase: project.PhaseExecute},
		{name: "review", phase: project.PhaseReview},
		{name: "integrate", phase: project.PhaseIntegrate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Nothing has happened in Git, so no phase may be inferred.
			snap := snapshotFor(set, repo, tc.phase, 1)
			result, err := Reconcile(ctx, ws, snap)
			if err != nil {
				t.Fatal(err)
			}
			if result.Snapshot.Phase != string(tc.phase) {
				t.Fatalf("phase %s became %s", tc.phase, result.Snapshot.Phase)
			}
			if result.Report.IntegrationRecovered {
				t.Fatalf("nothing was merged, yet integration was reported recovered")
			}
			if result.Snapshot.Integration.Started {
				t.Fatalf("integration invented out of nothing: %+v", result.Snapshot.Integration)
			}
		})
	}
}

// TestReconcileIgnoresBaseOnlyHistory guards the false positive where both
// worktrees still sit on the base commit: that is "nothing merged", not
// "Tony merged into Austin".
func TestReconcileIgnoresBaseOnlyHistory(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)

	snap := snapshotFor(set, repo, project.PhaseReview, 1)
	result, err := Reconcile(ctx, ws, snap)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.IntegrationRecovered {
		t.Fatal("unmodified worktrees were reported as an integration")
	}
	if result.Snapshot.Phase != string(project.PhaseReview) {
		t.Fatalf("phase = %s", result.Snapshot.Phase)
	}
}

// TestValidateKeepsExistingWorktrees covers requirement 7: resume validates and
// reuses the persisted worktrees instead of recreating them.
func TestValidateKeepsExistingWorktrees(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)
	snap := snapshotFor(set, repo, project.PhasePlan, 1)

	marker := filepath.Join(set.Tony.Path, "untracked-marker")
	if err := os.WriteFile(marker, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	branchBefore := strings.TrimSpace(run(t, set.Tony.Path, "git", "rev-parse", "--abbrev-ref", "HEAD"))

	if err := Validate(ctx, ws, snap); err != nil {
		t.Fatalf("healthy session failed validation: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("validation recreated the worktree and lost local files: %v", err)
	}
	if branchAfter := strings.TrimSpace(run(t, set.Tony.Path, "git", "rev-parse", "--abbrev-ref", "HEAD")); branchAfter != branchBefore {
		t.Fatalf("validation changed Tony's branch from %s to %s", branchBefore, branchAfter)
	}
}

func TestValidateRejectsMissingWorktree(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)
	snap := snapshotFor(set, repo, project.PhasePlan, 1)

	if err := os.RemoveAll(set.Austin.Path); err != nil {
		t.Fatal(err)
	}
	err := Validate(ctx, ws, snap)
	if err == nil {
		t.Fatal("a missing worktree must fail validation")
	}
	if !strings.Contains(err.Error(), "Austin") {
		t.Fatalf("error does not name the missing worktree: %v", err)
	}
}

func TestValidateRejectsRepositoryWithoutWorktreeRecord(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)
	snap := snapshotFor(set, repo, project.PhasePlan, 1)
	delete(snap.Worktrees, protocol.Tony)

	err := Validate(ctx, ws, snap)
	if err == nil || !strings.Contains(err.Error(), "Tony") {
		t.Fatalf("expected a missing-worktree-record error, got %v", err)
	}
}

func TestValidateRejectsNonRepository(t *testing.T) {
	ctx := context.Background()
	ws, set, _ := newFixture(t)
	snap := snapshotFor(set, t.TempDir(), project.PhasePlan, 1)

	if err := Validate(ctx, ws, snap); err == nil {
		t.Fatal("expected validation to reject a directory that is not the session's repository")
	}
}

// TestApplyRestoresProjectState proves the reconciled snapshot really becomes
// the live state, including revoked signatures.
func TestApplyRestoresProjectState(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)
	head := committed(t, set.Austin.Path, "austin.txt", "austin\n")

	snap := snapshotFor(set, repo, project.PhaseExecute, 1)
	snap = signed(snap, protocol.Austin, head)
	snap = signed(snap, protocol.Tony, "not-the-right-head")

	state := project.NewState()
	result, err := ReconcileAndApply(ctx, ws, state, snap)
	if err != nil {
		t.Fatal(err)
	}

	live := state.Snapshot()
	if !live.Ready[protocol.Austin] {
		t.Fatal("Austin's valid signature was lost when applying")
	}
	if live.Ready[protocol.Tony] {
		t.Fatal("Tony's stale signature was applied as valid")
	}
	if live.Phase != project.PhaseExecute || live.PlanVersion != 1 || live.Plan != "shared plan" {
		t.Fatalf("domain state not restored: %+v", live)
	}
	if !live.Started {
		t.Fatal("restored state lost the started flag")
	}
	if len(result.Report.Revoked) != 1 {
		t.Fatalf("report = %+v", result.Report)
	}
}

// TestComposeRoundTripThroughDisk covers requirements 1 and 2 end to end: live
// domain state, persisted through the mutation hook, reloaded after a "crash".
func TestComposeRoundTripThroughDisk(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)

	store, err := sessionstore.New(t.TempDir(), "repo-test", set.Session)
	if err != nil {
		t.Fatal(err)
	}

	state := project.NewState()
	// Every mutation persists a full snapshot, exactly as the coordinator wires it.
	state.SetPersistence(func(snapshot project.Snapshot) {
		snap := Compose(ComposeInput{
			DuoVersion: "v0.4.0-test",
			SessionID:  set.Session,
			RepoID:     "repo-test",
			Repository: repo,
			BaseBranch: set.BaseBranch,
			BaseCommit: set.BaseCommit,
			CreatedAt:  time.Now().UTC(),
			Project:    snapshot,
			Worktrees:  set,
			PiSessions: map[protocol.AgentID]string{
				protocol.Austin: "11111111-1111-4111-8111-111111111111",
				protocol.Tony:   "22222222-2222-4222-8222-222222222222",
			},
		})
		if err := store.Save(snap); err != nil {
			t.Errorf("persist snapshot: %v", err)
		}
	})

	if _, err := state.SetPlan(protocol.Austin, "shared plan"); err != nil {
		t.Fatal(err)
	}
	state.MarkStarted()
	// In PLAN a signature is tied to the plan version, not to a commit.
	if _, _, err := state.SetReady(protocol.Austin, true, "approved", "plan-v1"); err != nil {
		t.Fatal(err)
	}

	// Simulate a crash: read back only what is on disk.
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Phase != string(project.PhasePlan) || loaded.Plan != "shared plan" || loaded.PlanVersion != 1 {
		t.Fatalf("persisted plan state is wrong: %+v", loaded)
	}
	if !loaded.Ready[protocol.Austin] || loaded.Evidence[protocol.Austin] != "plan-v1" {
		t.Fatalf("persisted signature is wrong: %+v", loaded)
	}
	if loaded.Worktrees[protocol.Tony].Branch != set.Tony.Branch {
		t.Fatalf("persisted worktrees are wrong: %+v", loaded.Worktrees)
	}

	// And recovery accepts what was persisted: the signature is still current.
	result, err := Reconcile(ctx, ws, loaded)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Snapshot.Ready[protocol.Austin] {
		t.Fatalf("a crash-free round trip revoked a valid signature: %s", result.Report.String())
	}
	if result.Snapshot.PiSessions[protocol.Austin] != loaded.PiSessions[protocol.Austin] {
		t.Fatal("Pi session identity was not preserved across the crash")
	}
}

func TestReportStringIsUseful(t *testing.T) {
	report := Report{
		Phase:                project.PhaseIntegrate,
		Revoked:              []protocol.AgentID{protocol.Austin, protocol.Tony},
		IntegrationRecovered: true,
	}
	text := report.String()
	for _, want := range []string{"INTEGRATE", "Austin", "Tony", "integration state rebuilt"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report %q is missing %q", text, want)
		}
	}
}
