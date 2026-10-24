package recovery

import (
	"context"
	"reflect"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/workspace"
)

// Reconcile is replayable: resume runs it on every start, so a second pass over
// its own output must be a fixpoint — same snapshot, no new revocation, and no
// second Git write. These cases cover the paths that do mutate something: the
// integration merge it rebuilds, the verifier sync it replays, and signature
// revocation.
func TestReconcileIsIdempotent(t *testing.T) {
	t.Run("a rebuilt integration settles after one pass", func(t *testing.T) {
		ctx := context.Background()
		ws, set, repo := newFixture(t)

		committed(t, set.Austin.Path, "austin.txt", "austin\n")
		committed(t, set.Tony.Path, "tony.txt", "tony\n")
		run(t, set.Austin.Path, "git", "merge", "--no-edit", set.Tony.Branch)

		snap := snapshotFor(set, repo, project.PhaseReview, 2)
		first, err := Reconcile(ctx, ws, snap)
		if err != nil {
			t.Fatal(err)
		}
		if !first.Report.IntegrationRecovered || first.Snapshot.Phase != string(project.PhaseIntegrate) {
			t.Fatalf("the completed merge was not recovered: %s", first.Report.String())
		}

		heads := headTriple(t, ctx, ws, repo)
		second, err := Reconcile(ctx, ws, first.Snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first.Snapshot, second.Snapshot) {
			t.Fatalf("a second pass changed the snapshot:\nfirst  %+v\nsecond %+v", first.Snapshot, second.Snapshot)
		}
		if len(second.Report.Revoked) != 0 {
			t.Fatalf("a second pass revoked signatures: %v", second.Report.Revoked)
		}
		// The report restates the recovery because every pass re-derives it from
		// Git; that is a diagnostic, not a second write. Pinning it here keeps a
		// future change from either silently re-applying the merge or quietly
		// dropping the restatement a reviewer relies on.
		if !second.Report.IntegrationRecovered {
			t.Fatalf("the second pass stopped reporting the integration it re-derived: %s", second.Report.String())
		}
		if got := headTriple(t, ctx, ws, repo); got != heads {
			t.Fatalf("replay moved Git: %s -> %s", heads, got)
		}
	})

	t.Run("an already synced verifier is not fast-forwarded again", func(t *testing.T) {
		ctx := context.Background()
		ws, set, repo := newFixture(t)

		landed := committed(t, set.Austin.Path, "step1.txt", "one\n")
		snap := stepSnapshotWithLanded(set, repo, landed)

		first, err := Reconcile(ctx, ws, snap)
		if err != nil {
			t.Fatal(err)
		}
		if !hasNote(first.Report.Notes, "fast-forwarded to the landed step") {
			t.Fatalf("the stale verifier was not repaired: %v", first.Report.Notes)
		}
		heads := headTriple(t, ctx, ws, repo)

		second, err := Reconcile(ctx, ws, first.Snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if hasNote(second.Report.Notes, "fast-forwarded to the landed step") {
			t.Fatalf("the replay replayed the repair: %v", second.Report.Notes)
		}
		if !reflect.DeepEqual(first.Snapshot, second.Snapshot) {
			t.Fatalf("a second pass changed the snapshot:\nfirst  %+v\nsecond %+v", first.Snapshot, second.Snapshot)
		}
		if got := headTriple(t, ctx, ws, repo); got != heads {
			t.Fatalf("replay moved Git: %s -> %s", heads, got)
		}
	})

	t.Run("a valid signature survives every replay", func(t *testing.T) {
		ctx := context.Background()
		ws, set, repo := newFixture(t)

		head := committed(t, set.Austin.Path, "austin.txt", "austin\n")
		snap := signed(snapshotFor(set, repo, project.PhaseExecute, 1), protocol.Austin, head)

		first, err := Reconcile(ctx, ws, snap)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			next, err := Reconcile(ctx, ws, first.Snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if len(next.Report.Revoked) != 0 {
				t.Fatalf("replay %d revoked a current signature: %v", i+2, next.Report.Revoked)
			}
			if !reflect.DeepEqual(first.Snapshot, next.Snapshot) {
				t.Fatalf("replay %d changed the snapshot:\nfirst %+v\nnext  %+v", i+2, first.Snapshot, next.Snapshot)
			}
			first = next
		}
	})
}

// headTriple is the Git state Reconcile must leave alone once it is a fixpoint:
// the user's branch and both agent worktrees.
func headTriple(t *testing.T, ctx context.Context, ws *workspace.GitManager, repo string) string {
	t.Helper()
	out := revIn(t, repo, "HEAD")
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		head, err := ws.Head(ctx, agent)
		if err != nil {
			t.Fatal(err)
		}
		out += " " + head
	}
	return out
}

// revIn reads a ref through the git helper the other fixtures use.
func revIn(t *testing.T, dir, ref string) string {
	t.Helper()
	return run(t, dir, "git", "rev-parse", ref)
}
