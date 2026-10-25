package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/atfa/duo/internal/sessionstore"
)

// TestResumeCandidatesFiltersWhatCanBeResumed pins the one definition of
// "resumable" the /sessions picker and `duo resume` share: unfinished, readable,
// not the open session, and not owned by another live Duo.
func TestResumeCandidatesFiltersWhatCanBeResumed(t *testing.T) {
	base, root := t.TempDir(), t.TempDir()
	repoID := sessionstore.RepoID(root)
	makeSession := func(id, phase string) sessionstore.Summary {
		t.Helper()
		store, err := sessionstore.New(base, repoID, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Save(sessionstore.Snapshot{SessionID: id, RepoID: repoID, Phase: phase}); err != nil {
			t.Fatal(err)
		}
		return sessionstore.Summary{SessionID: id, Dir: store.Dir(), Snapshot: sessionstore.Snapshot{SessionID: id, Phase: phase}}
	}

	open := makeSession("open-session", "EXECUTE")
	done := makeSession("finished", "DONE")
	unfinished := makeSession("unfinished", "REVIEW")
	started := makeSession("never-started", "")
	locked := makeSession("locked-elsewhere", "EXECUTE")
	lock, err := sessionstore.Open(base, repoID, "locked-elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	held, err := lock.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	brokenDir := filepath.Join(base, repoID, "broken")
	if err := os.MkdirAll(brokenDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(brokenDir, "state.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	list, err := sessionstore.List(base, repoID)
	if err != nil {
		t.Fatal(err)
	}
	got := resumeCandidates(list, open.SessionID)

	if len(got) != 1 || got[0].SessionID != unfinished.SessionID {
		ids := make([]string, 0, len(got))
		for _, item := range got {
			ids = append(ids, item.SessionID)
		}
		t.Fatalf("resumeCandidates = %v, want only %q (excluded: current %q, done %q, unreadable, unlocked %q, locked %q)",
			ids, unfinished.SessionID, open.SessionID, done.SessionID, started.SessionID, locked.SessionID)
	}
}
