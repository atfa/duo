package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/atfa/duo/internal/protocol"
)

// stepSyncFixture prepares the two worktrees and returns the manager, the set and
// a helper commit on either side, so each case below is one short scenario.
func stepSyncFixture(t *testing.T) (*GitManager, Set) {
	t.Helper()
	ctx := context.Background()
	m := NewGitManager(GitConfig{
		Repository: initRepo(t),
		Root:       filepath.Join(t.TempDir(), "worktrees"),
		Session:    "step-sync",
	})
	set, err := m.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return m, set
}

// TestSyncTonyToFastForwardsOntoTheLandedStep is the Step-mode fix: the verifier's
// worktree has to contain the step that reached the user, or Tony reviews and
// builds a tree the user never received.
func TestSyncTonyToFastForwardsOntoTheLandedStep(t *testing.T) {
	ctx := context.Background()
	m, set := stepSyncFixture(t)

	writeAndCommit(t, set.Austin.Path, "step1.txt", "one\n", "step 1")
	landed, err := m.Head(ctx, protocol.Austin)
	if err != nil {
		t.Fatal(err)
	}

	sync, err := m.SyncTonyTo(ctx, landed)
	if err != nil {
		t.Fatal(err)
	}
	if !sync.Moved {
		t.Fatalf("expected Tony to move onto the landed step, got %+v", sync)
	}
	if sync.Head != landed {
		t.Fatalf("Tony head = %s, want landed %s", sync.Head, landed)
	}
	// The landed file must be present, not merely recorded as a ref.
	if _, err := os.Stat(filepath.Join(set.Tony.Path, "step1.txt")); err != nil {
		t.Fatalf("landed step content missing from Tony's worktree: %v", err)
	}
}

// TestSyncTonyToIsIdempotent is what makes replaying it after a crash between the
// landing and the sync safe.
func TestSyncTonyToIsIdempotent(t *testing.T) {
	ctx := context.Background()
	m, set := stepSyncFixture(t)
	writeAndCommit(t, set.Austin.Path, "step1.txt", "one\n", "step 1")
	landed, err := m.Head(ctx, protocol.Austin)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := m.SyncTonyTo(ctx, landed); err != nil {
		t.Fatal(err)
	}
	second, err := m.SyncTonyTo(ctx, landed)
	if err != nil {
		t.Fatal(err)
	}
	if second.Moved {
		t.Fatalf("a replay must be a no-op, got %+v", second)
	}
	if second.Head != landed {
		t.Fatalf("replay changed Tony's head: %s, want %s", second.Head, landed)
	}
}

// TestSyncTonyToNeverRewinds pins the safety property: a target that is already
// contained in Tony's history leaves him where he is, so a stale or replayed
// target can never discard commits already in his worktree.
func TestSyncTonyToNeverRewinds(t *testing.T) {
	ctx := context.Background()
	m, set := stepSyncFixture(t)

	writeAndCommit(t, set.Austin.Path, "step1.txt", "one\n", "step 1")
	first, err := m.Head(ctx, protocol.Austin)
	if err != nil {
		t.Fatal(err)
	}
	writeAndCommit(t, set.Austin.Path, "step2.txt", "two\n", "step 2")
	second, err := m.Head(ctx, protocol.Austin)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := m.SyncTonyTo(ctx, second); err != nil {
		t.Fatal(err)
	}
	// Syncing to the older step must not move Tony backwards.
	back, err := m.SyncTonyTo(ctx, first)
	if err != nil {
		t.Fatalf("an already-contained target must not error: %v", err)
	}
	if back.Moved || back.Head != second {
		t.Fatalf("sync rewound Tony to an older step: %+v", back)
	}
}

// TestSyncTonyToSkipsADirtyWorktree keeps uncommitted verifier work from being
// silently overwritten by the fast-forward.
func TestSyncTonyToSkipsADirtyWorktree(t *testing.T) {
	ctx := context.Background()
	m, set := stepSyncFixture(t)
	writeAndCommit(t, set.Austin.Path, "step1.txt", "one\n", "step 1")
	landed, err := m.Head(ctx, protocol.Austin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(set.Tony.Path, "scratch.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sync, err := m.SyncTonyTo(ctx, landed)
	if err != nil {
		t.Fatalf("a dirty worktree is reported, not an error: %v", err)
	}
	if !sync.Skipped || sync.Reason == "" {
		t.Fatalf("expected a reported skip, got %+v", sync)
	}
	// The verifier's own file must survive.
	if _, err := os.Stat(filepath.Join(set.Tony.Path, "scratch.txt")); err != nil {
		t.Fatalf("dirty verifier work was lost: %v", err)
	}
}

// TestSyncTonyToWithoutALandedHead is the pre-first-step state: there is nothing
// to sync to, and Duo must not invent a target.
func TestSyncTonyToWithoutALandedHead(t *testing.T) {
	ctx := context.Background()
	m, _ := stepSyncFixture(t)

	sync, err := m.SyncTonyTo(ctx, "   ")
	if err != nil {
		t.Fatal(err)
	}
	if !sync.Skipped || sync.Moved {
		t.Fatalf("an empty target must be a reported skip, got %+v", sync)
	}
}
