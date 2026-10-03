package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
)

func TestParseSessionsArgs(t *testing.T) {
	cases := []struct {
		args     []string
		wantAll  bool
		wantRepo string
		wantErr  bool
	}{
		{args: []string{}, wantAll: false, wantRepo: ""},
		{args: []string{"--all"}, wantAll: true, wantRepo: ""},
		{args: []string{"-a"}, wantAll: true, wantRepo: ""},
		{args: []string{"/path/to/repo"}, wantAll: false, wantRepo: "/path/to/repo"},
		{args: []string{"--all", "/path"}, wantAll: true, wantRepo: "/path"},
		{args: []string{"-unknown"}, wantErr: true},
		{args: []string{"repo1", "repo2"}, wantErr: true},
	}

	for _, c := range cases {
		got, err := parseSessionsArgs(c.args)
		if (err != nil) != c.wantErr {
			t.Fatalf("parseSessionsArgs(%v) error = %v, wantErr %t", c.args, err, c.wantErr)
		}
		if !c.wantErr {
			if got.all != c.wantAll || got.repository != c.wantRepo {
				t.Fatalf("parseSessionsArgs(%v) = %+v, want all=%t repo=%q", c.args, got, c.wantAll, c.wantRepo)
			}
		}
	}
}

func TestParseCleanArgs(t *testing.T) {
	cases := []struct {
		args      []string
		wantAll   bool
		wantRepos bool
		wantForce bool
		wantDry   bool
		wantID    string
		wantErr   bool
	}{
		{args: []string{}, wantAll: false},
		{args: []string{"--all"}, wantAll: true},
		{args: []string{"-a"}, wantAll: true},
		{args: []string{"--all-repos"}, wantRepos: true},
		{args: []string{"--force", "-n"}, wantForce: true, wantDry: true},
		{args: []string{"session-123"}, wantID: "session-123"},
		{args: []string{"-f", "session-123"}, wantForce: true, wantID: "session-123"},
		{args: []string{"--invalid"}, wantErr: true},
		{args: []string{"s1", "s2"}, wantErr: true},
	}

	for _, c := range cases {
		got, err := parseCleanArgs(c.args)
		if (err != nil) != c.wantErr {
			t.Fatalf("parseCleanArgs(%v) error = %v, wantErr %t", c.args, err, c.wantErr)
		}
		if !c.wantErr {
			if got.all != c.wantAll || got.allRepos != c.wantRepos || got.force != c.wantForce || got.dryRun != c.wantDry || got.sessionID != c.wantID {
				t.Fatalf("parseCleanArgs(%v) = %+v", c.args, got)
			}
		}
	}
}

func setupTestSession(t *testing.T, baseDir, repoID, sessionID, phase string) (*sessionstore.Store, sessionstore.Snapshot) {
	t.Helper()
	store, err := sessionstore.New(baseDir, repoID, sessionID)
	if err != nil {
		t.Fatal(err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	wtDir := filepath.Join(home, ".duo", "worktrees", "test-repo", sessionID)
	if err := os.MkdirAll(filepath.Join(wtDir, "austin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wtDir, "tony"), 0o755); err != nil {
		t.Fatal(err)
	}

	snap := sessionstore.Snapshot{
		SessionID:  sessionID,
		RepoID:     repoID,
		Repository: t.TempDir(),
		Phase:      phase,
		Mode:       project.ModeFast,
		UpdatedAt:  time.Now().UTC(),
		Worktrees: map[protocol.AgentID]sessionstore.Worktree{
			protocol.Austin: {Path: filepath.Join(wtDir, "austin"), Branch: "duo/" + sessionID + "/austin"},
			protocol.Tony:   {Path: filepath.Join(wtDir, "tony"), Branch: "duo/" + sessionID + "/tony"},
		},
	}
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}
	return store, snap
}

func TestRunCleanDoneSession(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()
	repoID := "repo-test-1"
	sessionID := "session-done"

	store, snap := setupTestSession(t, baseDir, repoID, sessionID, "DONE")
	wtPath := snap.Worktrees[protocol.Austin].Path
	defer os.RemoveAll(filepath.Dir(wtPath))

	opts := cleanArgs{all: true}
	cleaned, skipped, err := cleanSummaries(ctx, baseDir, repoID, []sessionstore.Summary{{
		SessionID: sessionID,
		Dir:       store.Dir(),
		Snapshot:  snap,
	}}, opts)

	if err != nil {
		t.Fatal(err)
	}
	if cleaned != 1 || skipped != 0 {
		t.Fatalf("cleaned = %d, skipped = %d; want 1 and 0", cleaned, skipped)
	}

	if store.Exists() {
		t.Fatal("expected session store to be removed")
	}
	if _, err := os.Stat(filepath.Dir(wtPath)); !os.IsNotExist(err) {
		t.Fatalf("expected worktree directory to be removed, err=%v", err)
	}
}

func TestRunCleanUnfinishedRequiresForce(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()
	repoID := "repo-test-2"
	sessionID := "session-running"

	store, snap := setupTestSession(t, baseDir, repoID, sessionID, "RUNNING")
	wtPath := snap.Worktrees[protocol.Austin].Path
	defer os.RemoveAll(filepath.Dir(wtPath))

	summary := sessionstore.Summary{
		SessionID: sessionID,
		Dir:       store.Dir(),
		Snapshot:  snap,
	}

	// Without --force, should skip
	cleaned, skipped, err := cleanSummaries(ctx, baseDir, repoID, []sessionstore.Summary{summary}, cleanArgs{all: true})
	if err != nil {
		t.Fatal(err)
	}
	if cleaned != 0 || skipped != 1 {
		t.Fatalf("without force: cleaned=%d, skipped=%d; want 0 and 1", cleaned, skipped)
	}
	if !store.Exists() {
		t.Fatal("session store should still exist")
	}

	// With --force, should clean
	cleaned, skipped, err = cleanSummaries(ctx, baseDir, repoID, []sessionstore.Summary{summary}, cleanArgs{all: true, force: true})
	if err != nil {
		t.Fatal(err)
	}
	if cleaned != 1 || skipped != 0 {
		t.Fatalf("with force: cleaned=%d, skipped=%d; want 1 and 0", cleaned, skipped)
	}
	if store.Exists() {
		t.Fatal("session store should be removed")
	}
}

func TestRunCleanActiveSessionSkipped(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()
	repoID := "repo-test-3"
	sessionID := "session-active"

	store, snap := setupTestSession(t, baseDir, repoID, sessionID, "DONE")
	defer os.RemoveAll(filepath.Dir(snap.Worktrees[protocol.Austin].Path))

	lock, err := store.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	summary := sessionstore.Summary{
		SessionID: sessionID,
		Dir:       store.Dir(),
		Snapshot:  snap,
	}

	cleaned, skipped, err := cleanSummaries(ctx, baseDir, repoID, []sessionstore.Summary{summary}, cleanArgs{all: true})
	if err != nil {
		t.Fatal(err)
	}
	if cleaned != 0 || skipped != 1 {
		t.Fatalf("active session must be skipped: cleaned=%d, skipped=%d", cleaned, skipped)
	}
	if !store.Exists() {
		t.Fatal("active session store must not be deleted")
	}
}

func TestRunCleanDryRunPreservesFiles(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()
	repoID := "repo-test-4"
	sessionID := "session-dry"

	store, snap := setupTestSession(t, baseDir, repoID, sessionID, "DONE")
	defer os.RemoveAll(filepath.Dir(snap.Worktrees[protocol.Austin].Path))

	summary := sessionstore.Summary{
		SessionID: sessionID,
		Dir:       store.Dir(),
		Snapshot:  snap,
	}

	cleaned, skipped, err := cleanSummaries(ctx, baseDir, repoID, []sessionstore.Summary{summary}, cleanArgs{all: true, dryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if cleaned != 1 || skipped != 0 {
		t.Fatalf("dry run count: cleaned=%d, skipped=%d", cleaned, skipped)
	}
	if !store.Exists() {
		t.Fatal("dry-run must not delete store files")
	}
}
