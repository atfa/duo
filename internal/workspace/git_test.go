package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/protocol"
)

func TestGitManagerPrepareCaptureAndIntegrate(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	root := filepath.Join(t.TempDir(), "worktrees")

	m := NewGitManager(GitConfig{
		Repository: repo,
		Root:       root,
		Session:    "test-session",
	})
	set, err := m.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if set.Austin.Path == set.Tony.Path || set.Austin.Branch == set.Tony.Branch {
		t.Fatal("expected isolated worktrees and branches")
	}

	writeAndCommit(t, set.Austin.Path, "austin.txt", "austin\n", "austin change")
	writeAndCommit(t, set.Tony.Path, "tony.txt", "tony\n", "tony change")

	a, err := m.CaptureArtifact(ctx, protocol.Austin)
	if err != nil {
		t.Fatal(err)
	}
	if a.Ahead != 1 {
		t.Fatalf("Austin ahead=%d, want 1", a.Ahead)
	}

	res, err := m.IntegrateTonyIntoAustin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Conflicted {
		t.Fatal("unexpected merge conflict")
	}
	if _, err := os.Stat(filepath.Join(set.Austin.Path, "tony.txt")); err != nil {
		t.Fatalf("Tony change not present in Austin integration worktree: %v", err)
	}
}

func TestScopePath(t *testing.T) {
	repo := t.TempDir()
	for _, tc := range []struct {
		launch string
		want   string
	}{
		{repo, "."},
		{filepath.Join(repo, "code1"), "code1"},
		{filepath.Join(repo, "a", "b"), filepath.Join("a", "b")},
	} {
		if err := os.MkdirAll(tc.launch, 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := ScopePath(repo, tc.launch)
		if err != nil || got != tc.want {
			t.Fatalf("ScopePath(%q, %q) = %q, %v; want %q", repo, tc.launch, got, err, tc.want)
		}
	}
	if _, err := ScopePath(repo, t.TempDir()); err == nil {
		t.Fatal("outside launch directory was accepted")
	}
}

func TestScopedPathRejectsEscape(t *testing.T) {
	root := t.TempDir()
	for _, scope := range []string{"../outside", "../../x", filepath.Join(root, "absolute")} {
		if _, err := ScopedPath(root, scope); err == nil {
			t.Fatalf("scope %q was accepted", scope)
		}
	}
}

func TestScopedPathRejectsSymlinkEscape(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ScopedPath(root, filepath.Join("link", "x")); err == nil {
		t.Fatal("scope through outside symlink was accepted")
	}
}

func TestPrepareCreatesEmptyScopeDirectories(t *testing.T) {
	repo := initRepo(t)
	if err := os.Mkdir(filepath.Join(repo, "code1"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewGitManager(GitConfig{Repository: repo, ScopePath: "code1", Root: filepath.Join(t.TempDir(), "worktrees"), Session: "scope"})
	set, err := m.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		dir, ok, err := set.AgentDir(agent)
		if err != nil || !ok {
			t.Fatalf("AgentDir(%s) = %q, %t, %v", agent, dir, ok, err)
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			t.Fatalf("scoped dir %s missing: %v", dir, err)
		}
	}
}

func TestAgentDirAtRootScopeIsWorktreeRoot(t *testing.T) {
	set := Set{ScopePath: ".", Austin: Worktree{Path: t.TempDir()}, Tony: Worktree{Path: t.TempDir()}}
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		wt, _ := set.For(agent)
		dir, ok, err := set.AgentDir(agent)
		if err != nil || !ok || dir != wt.Path {
			t.Fatalf("AgentDir(%s) = %q, %t, %v; want %q", agent, dir, ok, err, wt.Path)
		}
	}
}

func TestPrepareRejectsScopeFileCollision(t *testing.T) {
	repo := initRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "code1"), []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "git", "add", "code1")
	run(t, repo, "git", "commit", "-m", "add scope file")
	_, err := NewGitManager(GitConfig{Repository: repo, ScopePath: "code1", Root: filepath.Join(t.TempDir(), "worktrees"), Session: "collision"}).Prepare(context.Background())
	if err == nil || !strings.Contains(err.Error(), "path exists but is not a directory") {
		t.Fatalf("scope file collision error = %v", err)
	}
}

func TestCaptureArtifactRejectsDirtyWorktree(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	m := NewGitManager(GitConfig{
		Repository: repo,
		Root:       filepath.Join(t.TempDir(), "worktrees"),
		Session:    "dirty-test",
	})
	set, err := m.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(set.Austin.Path, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = m.CaptureArtifact(ctx, protocol.Austin)
	if err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("expected uncommitted-change error, got %v", err)
	}
}

func TestGitManagerPrepareRequiresExistingRepository(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("not a repository\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := NewGitManager(GitConfig{Repository: dir, Session: "test"}).Prepare(context.Background())
	if err == nil {
		t.Fatal("expected repository error")
	}
	for _, want := range []string{
		"requires an existing Git repository",
		"will not initialize one automatically",
		"git init",
		".gitignore",
		"git add .",
		`git commit --allow-empty -m "Initial commit"`,
		"duo",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%s", want, err)
		}
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(statErr) {
		t.Fatalf("Prepare created .git: %v", statErr)
	}
}

func TestFindRootRequiresExistingRepository(t *testing.T) {
	dir := t.TempDir()

	_, err := FindRoot(context.Background(), dir)
	if err == nil {
		t.Fatal("expected repository error")
	}
	for _, want := range []string{
		"requires an existing Git repository",
		"will not initialize one automatically",
		"git init",
		".gitignore",
		"git add .",
		`git commit --allow-empty -m "Initial commit"`,
		"duo",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%s", want, err)
		}
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(statErr) {
		t.Fatalf("FindRoot created .git: %v", statErr)
	}
}

func TestGitManagerPrepareRequiresInitialCommit(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	run(t, repo, "git", "init")

	_, err := NewGitManager(GitConfig{Repository: repo, Session: "test", BaseRef: "HEAD"}).Prepare(ctx)
	if err == nil {
		t.Fatal("expected initial-commit error")
	}
	for _, want := range []string{
		"requires at least one commit",
		"will not create one automatically",
		".gitignore",
		"git add .",
		`git commit --allow-empty -m "Initial commit"`,
		"duo",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%s", want, err)
		}
	}

	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "HEAD^{commit}")
	cmd.Dir = repo
	if output, verifyErr := cmd.CombinedOutput(); verifyErr == nil {
		t.Fatalf("Prepare created a commit: %s", output)
	}
}

func TestGitManagerPrepareKeepsCustomBaseRefError(t *testing.T) {
	_, err := NewGitManager(GitConfig{
		Repository: initRepo(t),
		Session:    "test",
		BaseRef:    "not-a-ref",
	}).Prepare(context.Background())
	if err == nil || !strings.Contains(err.Error(), `resolve base ref "not-a-ref"`) {
		t.Fatalf("expected base-ref error, got %v", err)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run(t, repo, "git", "init")
	run(t, repo, "git", "config", "user.email", "duo@example.invalid")
	run(t, repo, "git", "config", "user.name", "Duo Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "git", "add", "README.md")
	run(t, repo, "git", "commit", "-m", "base")
	return repo
}

func writeAndCommit(t *testing.T, dir, name, content, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "git", "add", name)
	run(t, dir, "git", "commit", "-m", message)
}

func run(t *testing.T, dir, command string, args ...string) string {
	t.Helper()
	cmd := exec.Command(command, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", command, args, err, out)
	}
	return string(out)
}
