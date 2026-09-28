package recovery

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// TestFastVerificationTargetMatchesGoalExecuteRule pins the shared rule that
// lets live verification and crash recovery agree: a Fast verdict is bound to
// the same clean, committed Austin HEAD that a Goal EXECUTE signature uses.
func TestFastVerificationTargetMatchesGoalExecuteRule(t *testing.T) {
	ctx := context.Background()
	ws, set, _ := newFixture(t)
	writeFile(t, set.Austin.Path, "a.txt", "1\n")
	run(t, set.Austin.Path, "git", "add", "a.txt")
	run(t, set.Austin.Path, "git", "commit", "-m", "a")

	fast, err := FastVerificationTarget(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	goal, err := ExpectedEvidence(ctx, ws, project.PhaseExecute, protocol.Austin, 0)
	if err != nil {
		t.Fatal(err)
	}
	if fast == "" || fast != goal {
		t.Fatalf("Fast target %q != Goal EXECUTE evidence %q", fast, goal)
	}

	// A dirty Austin worktree is not a verifiable artifact in either mode.
	writeFile(t, set.Austin.Path, "a.txt", "uncommitted\n")
	if _, err := FastVerificationTarget(ctx, ws); err == nil {
		t.Fatal("a dirty worktree must not be a verification target")
	}
}

// TestComposeRoundTripsModeAndVerification covers the persistence boundary: the
// snapshot the coordinator writes must restore mode, driver and verification.
func TestComposeRoundTripsModeAndVerification(t *testing.T) {
	_, set, repo := newFixture(t)
	at := time.Now().UTC()
	snap := Compose(ComposeInput{
		DuoVersion: "v0.5.0-test",
		SessionID:  set.Session,
		RepoID:     "repo-test",
		Repository: repo,
		BaseBranch: set.BaseBranch,
		BaseCommit: set.BaseCommit,
		CreatedAt:  at,
		Project: project.Snapshot{
			Mode:         project.ModeFast,
			Phase:        project.PhaseVerify,
			Started:      true,
			Verification: project.Verification{Status: project.VerificationPassed, Head: "abcdef0123456789", Note: "ok", At: &at},
		},
		Worktrees:  set,
		PiSessions: map[protocol.AgentID]string{},
	})
	if snap.EffectiveMode() != project.ModeFast {
		t.Fatalf("composed mode = %q, want fast", snap.Mode)
	}
	if snap.Driver != string(protocol.Austin) {
		t.Fatalf("driver = %q, want Austin (derived from fast mode)", snap.Driver)
	}
	if snap.Verification == nil || snap.Verification.Status != string(project.VerificationPassed) {
		t.Fatalf("composed verification = %+v", snap.Verification)
	}

	back := ProjectSnapshot(snap)
	if back.Mode != project.ModeFast || back.Phase != project.PhaseVerify {
		t.Fatalf("restored = mode %q phase %s", back.Mode, back.Phase)
	}
	if back.Verification.Status != project.VerificationPassed || back.Verification.Head != "abcdef0123456789" {
		t.Fatalf("restored verification = %+v", back.Verification)
	}

	// A Goal snapshot records its mode but no Fast-only fields.
	goal := Compose(ComposeInput{
		DuoVersion: "v0.5.0-test",
		SessionID:  set.Session,
		RepoID:     "repo-test",
		Repository: repo,
		BaseBranch: set.BaseBranch,
		BaseCommit: set.BaseCommit,
		CreatedAt:  at,
		Project:    project.Snapshot{Mode: project.ModeGoal, Phase: project.PhasePlan, Started: true},
		Worktrees:  set,
		PiSessions: map[protocol.AgentID]string{},
	})
	if goal.EffectiveMode() != project.ModeGoal || goal.Driver != "" || goal.Verification != nil {
		t.Fatalf("goal snapshot = mode %q driver %q verification %+v", goal.Mode, goal.Driver, goal.Verification)
	}
}

// TestReconcileFastVerificationHonoursTheHeadBinding is the crash-recovery half
// of the head-bound rule: a pass for the current Austin HEAD survives resume; a
// pass for a HEAD that no longer exists is deterministically revoked back to
// RUNNING so the new artifact is verified on a fresh request.
func TestReconcileFastVerificationHonoursTheHeadBinding(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)
	writeFile(t, set.Austin.Path, "a.txt", "1\n")
	run(t, set.Austin.Path, "git", "add", "a.txt")
	run(t, set.Austin.Path, "git", "commit", "-m", "a")

	target, err := FastVerificationTarget(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	snap := Compose(ComposeInput{
		DuoVersion: "v0.5.0-test",
		SessionID:  set.Session,
		RepoID:     "repo-test",
		Repository: repo,
		BaseBranch: set.BaseBranch,
		BaseCommit: set.BaseCommit,
		CreatedAt:  at,
		Project: project.Snapshot{
			Mode:         project.ModeFast,
			Phase:        project.PhaseVerify,
			Started:      true,
			Verification: project.Verification{Status: project.VerificationPassed, Head: target, At: &at},
		},
		Worktrees:  set,
		PiSessions: map[protocol.AgentID]string{},
	})

	result, err := Reconcile(ctx, ws, snap)
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Phase != string(project.PhaseVerify) {
		t.Fatalf("a pass for the current HEAD must stay in VERIFY, got %s", result.Snapshot.Phase)
	}
	if v := result.Snapshot.VerificationResult(); v.Status != project.VerificationPassed {
		t.Fatalf("a matching pass must be preserved, got %+v", v)
	}
	if len(result.Report.Revoked) != 0 {
		t.Fatalf("a matching pass must not be reported as revoked: %+v", result.Report)
	}

	// Move Austin's HEAD: the pass now describes a different artifact.
	writeFile(t, set.Austin.Path, "a.txt", "2\n")
	run(t, set.Austin.Path, "git", "add", "a.txt")
	run(t, set.Austin.Path, "git", "commit", "-m", "a2")
	if newHead := strings.TrimSpace(run(t, set.Austin.Path, "git", "rev-parse", "HEAD")); newHead == target {
		t.Fatal("test did not move Austin's HEAD")
	}

	result, err = Reconcile(ctx, ws, snap)
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Phase != string(project.PhaseRunning) {
		t.Fatalf("a stale pass must return to RUNNING, got %s", result.Snapshot.Phase)
	}
	if result.Snapshot.Verification != nil {
		t.Fatalf("a stale pass must be cleared, got %+v", result.Snapshot.Verification)
	}
	if len(result.Report.Revoked) == 0 {
		t.Fatalf("the revocation must be reported: %+v", result.Report)
	}
	if result.Report.Phase != project.PhaseRunning {
		t.Fatalf("report phase = %s, want RUNNING", result.Report.Phase)
	}
}

// TestReconcileLegacyGoalSnapshotIsUnchanged keeps recovery's Goal contract: a
// legacy snapshot with no mode still reconciles as Goal.
func TestReconcileLegacyGoalSnapshotIsUnchanged(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)
	snap := snapshotFor(set, repo, project.PhasePlan, 1)

	result, err := Reconcile(ctx, ws, snap)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Snapshot.EffectiveMode(); got != project.ModeGoal {
		t.Fatalf("legacy reconcile mode = %q, want goal", got)
	}
	if result.Snapshot.Phase != string(project.PhasePlan) {
		t.Fatalf("legacy reconcile phase = %s, want PLAN", result.Snapshot.Phase)
	}
	if result.Snapshot.Verification != nil {
		t.Fatalf("legacy reconcile must not invent verification: %+v", result.Snapshot.Verification)
	}
}
