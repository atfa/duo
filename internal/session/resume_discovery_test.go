package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/sessionstore"
)

func TestSelectSessionReportsUnreadableSessions(t *testing.T) {
	for _, tc := range []struct {
		name          string
		withCandidate bool
		want          []string
	}{
		{name: "broken only", want: []string{"could not be read", "corrupt"}},
		{name: "candidate and broken", withCandidate: true, want: []string{"readable", "unfinished", "corrupt", "duo resume live"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, root := t.TempDir(), t.TempDir()
			repoID := sessionstore.RepoID(root)
			brokenDir := filepath.Join(base, repoID, "corrupt")
			if err := os.MkdirAll(brokenDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(brokenDir, "state.json"), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.withCandidate {
				store, err := sessionstore.New(base, repoID, "live")
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Save(sessionstore.Snapshot{SessionID: "live", Phase: "EXECUTE"}); err != nil {
					t.Fatal(err)
				}
			}

			selected, broken, err := SelectSession(base, repoID, "", root)
			if tc.withCandidate {
				if err != nil || selected.SessionID != "live" {
					t.Fatalf("SelectSession = (%q, %v), want readable candidate despite broken state", selected.SessionID, err)
				}
				if len(broken) != 1 || broken[0].SessionID != "corrupt" {
					t.Fatalf("broken diagnostics = %#v, want corrupt session", broken)
				}
				return
			}
			if err == nil {
				t.Fatal("SelectSession unexpectedly succeeded")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestSelectSessionCanSelectCompletedSession(t *testing.T) {
	base, root := t.TempDir(), t.TempDir()
	repoID := sessionstore.RepoID(root)
	store, err := sessionstore.New(base, repoID, "completed")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(sessionstore.Snapshot{SessionID: "completed", Phase: "DONE"}); err != nil {
		t.Fatal(err)
	}

	selected, _, err := SelectSession(base, repoID, "", root)
	if err != nil || selected.SessionID != "completed" {
		t.Fatalf("SelectSession = (%q, %v), want completed session", selected.SessionID, err)
	}
}

func TestOtherCheckoutHintFindsLinkedWorktreeSession(t *testing.T) {
	repo := t.TempDir()
	resumeGit(t, repo, "init", "-b", "main")
	resumeGit(t, repo, "config", "user.email", "duo@example.invalid")
	resumeGit(t, repo, "config", "user.name", "Duo Test")
	resumeCommit(t, repo, "README", "root")
	worktree := filepath.Join(t.TempDir(), "linked")
	resumeGit(t, repo, "worktree", "add", "-b", "duo/test", worktree)

	base := t.TempDir()
	otherID := sessionstore.RepoID(worktree)
	store, err := sessionstore.New(base, otherID, "linked-session")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(sessionstore.Snapshot{SessionID: "linked-session", Phase: "EXECUTE", Repository: worktree}); err != nil {
		t.Fatal(err)
	}

	hint := otherCheckoutHint(base, sessionstore.RepoID(repo), repo)
	if !strings.Contains(hint, worktree) || !strings.Contains(hint, "duo resume") {
		t.Fatalf("other checkout hint = %q, want linked checkout and resume guidance", hint)
	}
}
