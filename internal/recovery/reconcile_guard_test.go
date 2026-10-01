package recovery

import (
	"context"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
)

// Capturing an artifact requires only a clean worktree, not a new commit, so
// Tony legitimately sits on the base commit. Base is an ancestor of every
// descendant, so the ancestor check alone cannot prove a merge landed.
func TestReconcileIgnoresUncommittedTony(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)
	snap := snapshotFor(set, repo, project.PhaseReview, 1)

	committed(t, set.Austin.Path, "austin.txt", "austin\n")
	// Tony stays on the base commit with a clean worktree.

	result, err := Reconcile(ctx, ws, snap)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.IntegrationRecovered {
		t.Errorf("Tony never committed, but recovery reported integration (MergedTony=%s, base=%s)",
			result.Snapshot.Integration.MergedTony, snap.BaseCommit)
	}
	if result.Snapshot.Phase != string(project.PhaseReview) {
		t.Errorf("phase = %s, want REVIEW", result.Snapshot.Phase)
	}
}

// A corrupt state.json can decode without the notes/ready/evidence objects, and
// the Fast revocation rules assign into them.
func TestReconcileToleratesMissingMaps(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)
	snap := snapshotFor(set, repo, project.PhaseVerify, 1)
	snap.Mode = project.ModeFast
	snap.Notes = nil
	snap.Ready = nil
	snap.Evidence = nil
	snap.Verification = &sessionstore.Verification{Status: string(project.VerificationPassed), Head: "deadbeef"}

	if _, err := Reconcile(ctx, ws, snap); err != nil {
		t.Fatalf("reconcile of a snapshot without maps failed: %v", err)
	}
}

// The revoked signature is Austin's completion request, so the report has to
// name Austin.
func TestReconcileNamesAustinAsRevoked(t *testing.T) {
	ctx := context.Background()
	ws, set, repo := newFixture(t)
	snap := snapshotFor(set, repo, project.PhaseVerify, 1)
	snap.Mode = project.ModeFast
	// A HEAD that cannot match the real one makes verification stale.
	snap.Verification = &sessionstore.Verification{Status: string(project.VerificationPassed), Head: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}

	result, err := Reconcile(ctx, ws, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Report.Revoked) != 1 || result.Report.Revoked[0] != protocol.Austin {
		t.Errorf("Revoked = %v, want [protocol.Austin]", result.Report.Revoked)
	}
	if got := result.Report.String(); !contains(got, "revoked stale signatures: Austin") {
		t.Errorf("report = %q, want it to name Austin", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
