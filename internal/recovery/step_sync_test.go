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

// stepSnapshotWithLanded builds a STEP snapshot whose first step landed at the
// given head and whose second step is in flight, which is the exact state a crash
// between the landing and the verifier sync leaves behind.
func stepSnapshotWithLanded(set workspace.Set, repo, landed string) sessionstore.Snapshot {
	snap := snapshotFor(set, repo, project.PhaseExecute, 1)
	snap.Mode = project.ModeStep
	snap.Step = 2
	snap.Steps = project.Steps{
		{Title: "step one", Done: true, DeliveryHead: landed},
		{Title: "step two"},
	}
	return snap
}

// TestStepReconcileSyncsTonyOntoTheLandedStep is the crash window the plan calls
// out: the step reached the user's repository, but the verifier's worktree was
// never fast-forwarded. Reconcile has to repair it, because Tony would otherwise
// review and build a tree the user never received.
func TestStepReconcileSyncsTonyOntoTheLandedStep(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)

	landed := committed(t, set.Austin.Path, "step1.txt", "one\n")
	snap := stepSnapshotWithLanded(set, repo, landed)

	result, err := Reconcile(ctx, ws, snap)
	if err != nil {
		t.Fatal(err)
	}
	head, err := ws.Head(ctx, protocol.Tony)
	if err != nil {
		t.Fatal(err)
	}
	if head != landed {
		t.Fatalf("Tony head = %s, want the landed step %s", head, landed)
	}
	if !hasNote(result.Report.Notes, "fast-forwarded to the landed step") {
		t.Fatalf("reconcile did not report the verifier sync: %v", result.Report.Notes)
	}
}

// TestStepReconcileSyncIsIdempotent covers the ordinary case: the live coordinator
// already synced Tony, so replaying it on resume must change nothing and must not
// be reported as a repair.
func TestStepReconcileSyncIsIdempotent(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)

	landed := committed(t, set.Austin.Path, "step1.txt", "one\n")
	if _, err := ws.SyncTonyTo(ctx, landed); err != nil {
		t.Fatal(err)
	}
	snap := stepSnapshotWithLanded(set, repo, landed)

	result, err := Reconcile(ctx, ws, snap)
	if err != nil {
		t.Fatal(err)
	}
	if hasNote(result.Report.Notes, "fast-forwarded to the landed step") {
		t.Fatalf("an already-synced verifier must not be reported as repaired: %v", result.Report.Notes)
	}
	head, err := ws.Head(ctx, protocol.Tony)
	if err != nil {
		t.Fatal(err)
	}
	if head != landed {
		t.Fatalf("Tony head moved to %s, want %s", head, landed)
	}
}

// TestStepReconcileDoesNotSyncWithoutALandedStep keeps a QUEUE that has not landed
// anything from pulling Austin's in-flight work into the verifier's worktree.
func TestStepReconcileDoesNotSyncWithoutALandedStep(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)

	// Austin commits work for step one, but nothing has landed yet.
	committed(t, set.Austin.Path, "inflight.txt", "wip\n")
	base := set.BaseCommit

	snap := snapshotFor(set, repo, project.PhasePlan, 1)
	snap.Mode = project.ModeStep
	snap.Step = 1
	snap.Steps = project.Steps{{Title: "step one"}}

	if _, err := Reconcile(ctx, ws, snap); err != nil {
		t.Fatal(err)
	}
	head, err := ws.Head(ctx, protocol.Tony)
	if err != nil {
		t.Fatal(err)
	}
	if head != base {
		t.Fatalf("Tony was moved to %s before any step landed (base %s)", head, base)
	}
}

// TestStepReconcileReportsADirtyVerifier keeps a verifier's uncommitted work from
// being overwritten silently: the run must survive and say why it did not sync.
func TestStepReconcileReportsADirtyVerifier(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)

	landed := committed(t, set.Austin.Path, "step1.txt", "one\n")
	writeFile(t, set.Tony.Path, "scratch.txt", "wip\n")
	snap := stepSnapshotWithLanded(set, repo, landed)

	result, err := Reconcile(ctx, ws, snap)
	if err != nil {
		t.Fatal(err)
	}
	if !hasNote(result.Report.Notes, "uncommitted changes") {
		t.Fatalf("a dirty verifier must be reported, not forced: %v", result.Report.Notes)
	}
}

func hasNote(notes []string, want string) bool {
	for _, note := range notes {
		if strings.Contains(note, want) {
			return true
		}
	}
	return false
}
