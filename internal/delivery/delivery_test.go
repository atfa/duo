package delivery

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run(t, repo, "git", "init")
	run(t, repo, "git", "config", "user.email", "duo@example.invalid")
	run(t, repo, "git", "config", "user.name", "Duo Test")
	write(t, repo, "README.md", "base\n")
	run(t, repo, "git", "add", "README.md")
	run(t, repo, "git", "commit", "-m", "base")
	return repo
}

func run(t *testing.T, dir string, command string, args ...string) string {
	t.Helper()
	cmd := exec.Command(command, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", command, args, err, out)
	}
	return string(out)
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func head(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(run(t, dir, "git", "rev-parse", "HEAD"))
}

func branch(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(run(t, dir, "git", "symbolic-ref", "--short", "HEAD"))
}

// produceFinal simulates the Duo integration branch: a commit on a separate
// branch that descends from base, then return the original checkout to base.
func produceFinal(t *testing.T, repo, branchName, file, content, message string) string {
	t.Helper()
	baseBranch := branch(t, repo)
	run(t, repo, "git", "checkout", "-b", branchName)
	write(t, repo, file, content)
	run(t, repo, "git", "add", file)
	run(t, repo, "git", "commit", "-m", message)
	final := head(t, repo)
	run(t, repo, "git", "checkout", baseBranch)
	return final
}

// TestDeliverFastForwardsAndMaterializesTheDeliverable is the core v0.4.1
// regression: after delivery the requested file exists in the directory the
// user launched Duo from.
func TestDeliverFastForwardsAndMaterializesTheDeliverable(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	base := head(t, repo)
	baseBranch := branch(t, repo)
	final := produceFinal(t, repo, "duo/s/austin", "result.md", "hello Duo\n", "add result.md")

	m := Manager{
		Repository:  repo,
		BaseBranch:  baseBranch,
		BaseCommit:  base,
		FinalBranch: "duo/s/austin",
		FinalHead:   final,
	}

	res, err := m.Deliver(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied {
		t.Fatalf("delivery was not applied: %+v", res.Check)
	}
	if got := head(t, repo); got != final {
		t.Fatalf("original HEAD = %s, want %s", got, final)
	}
	if got := branch(t, repo); got != baseBranch {
		t.Fatalf("delivery switched branches to %q", got)
	}

	data, err := os.ReadFile(filepath.Join(repo, "result.md"))
	if err != nil {
		t.Fatalf("result.md is missing from the original repository: %v", err)
	}
	if string(data) != "hello Duo\n" {
		t.Fatalf("result.md = %q", data)
	}

	if len(res.Changes) != 1 || res.Changes[0].Status != "A" || res.Changes[0].Path != "result.md" {
		t.Fatalf("changed files = %+v, want a single added result.md", res.Changes)
	}
}

// TestDeliverRefusesDirtyOriginalRepository proves an uncommitted change in the
// user's repository blocks delivery and is never overwritten.
func TestDeliverRefusesDirtyOriginalRepository(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	base := head(t, repo)
	baseBranch := branch(t, repo)
	final := produceFinal(t, repo, "duo/s/austin", "result.md", "hello Duo\n", "add result.md")
	write(t, repo, "user-work.md", "important uncommitted work\n")

	res, err := Manager{
		Repository: repo, BaseBranch: baseBranch, BaseCommit: base,
		FinalBranch: "duo/s/austin", FinalHead: final,
	}.Deliver(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied {
		t.Fatal("delivery must not apply over a dirty repository")
	}
	if !strings.Contains(res.Check.Reason, "uncommitted") {
		t.Fatalf("reason = %q", res.Check.Reason)
	}
	if got := head(t, repo); got != base {
		t.Fatalf("original HEAD moved to %s despite dirty tree", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "user-work.md")); err != nil {
		t.Fatalf("user file was disturbed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "result.md")); !os.IsNotExist(err) {
		t.Fatalf("result.md should not exist before delivery: %v", err)
	}
}

// TestDeliverRefusesDifferentBranch proves Duo never switches the user's branch.
func TestDeliverRefusesDifferentBranch(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	base := head(t, repo)
	baseBranch := branch(t, repo)
	final := produceFinal(t, repo, "duo/s/austin", "result.md", "hello Duo\n", "add result.md")

	run(t, repo, "git", "checkout", "-b", "other-branch")
	other := head(t, repo)

	res, err := Manager{
		Repository: repo, BaseBranch: baseBranch, BaseCommit: base,
		FinalBranch: "duo/s/austin", FinalHead: final,
	}.Deliver(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied {
		t.Fatal("delivery must not apply from a different branch")
	}
	if !strings.Contains(res.Check.Reason, "refusing to switch branches") {
		t.Fatalf("reason = %q", res.Check.Reason)
	}
	if got := branch(t, repo); got != "other-branch" {
		t.Fatalf("branch changed to %q", got)
	}
	if got := head(t, repo); got != other {
		t.Fatalf("HEAD moved to %s", got)
	}
}

// TestDeliverRefusesDivergedHistory proves that a user commit on the original
// branch is never merged automatically.
func TestDeliverRefusesDivergedHistory(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	baseBranch := branch(t, repo)
	final := produceFinal(t, repo, "duo/s/austin", "result.md", "hello Duo\n", "add result.md")

	write(t, repo, "user.md", "user commit\n")
	run(t, repo, "git", "add", "user.md")
	run(t, repo, "git", "commit", "-m", "user commit")
	userHead := head(t, repo)

	res, err := Manager{
		Repository: repo, BaseBranch: baseBranch, BaseCommit: baseOf(t, repo, final),
		FinalBranch: "duo/s/austin", FinalHead: final,
	}.Deliver(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied {
		t.Fatal("diverged history must not be merged automatically")
	}
	if !strings.Contains(res.Check.Reason, "diverged") {
		t.Fatalf("reason = %q", res.Check.Reason)
	}
	if got := head(t, repo); got != userHead {
		t.Fatalf("user commit was disturbed: HEAD = %s, want %s", got, userHead)
	}
}

// TestDeliverIsIdempotentWhenAlreadyApplied covers the crash window where the
// fast-forward succeeded but Duo died before recording it.
func TestDeliverIsIdempotentWhenAlreadyApplied(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	base := head(t, repo)
	baseBranch := branch(t, repo)
	final := produceFinal(t, repo, "duo/s/austin", "result.md", "hello Duo\n", "add result.md")

	run(t, repo, "git", "merge", "--ff-only", final)

	m := Manager{
		Repository: repo, BaseBranch: baseBranch, BaseCommit: base,
		FinalBranch: "duo/s/austin", FinalHead: final,
	}
	for i := 0; i < 2; i++ {
		res, err := m.Deliver(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Applied {
			t.Fatalf("run %d was not idempotent: %+v", i, res.Check)
		}
		if !res.Check.AlreadyApplied && i == 1 {
			t.Fatalf("run %d reported a fresh merge instead of AlreadyApplied", i)
		}
		if got := head(t, repo); got != final {
			t.Fatalf("run %d moved HEAD to %s, want %s", i, got, final)
		}
	}
}

// TestDeliverRefusesDetachedHead proves a detached original repository is left
// alone.
func TestDeliverRefusesDetachedHead(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	base := head(t, repo)
	baseBranch := branch(t, repo)
	final := produceFinal(t, repo, "duo/s/austin", "result.md", "hello Duo\n", "add result.md")

	run(t, repo, "git", "checkout", "--detach", base)

	res, err := Manager{
		Repository: repo, BaseBranch: baseBranch, BaseCommit: base,
		FinalBranch: "duo/s/austin", FinalHead: final,
	}.Deliver(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied {
		t.Fatal("detached HEAD must not be delivered automatically")
	}
	if !strings.Contains(res.Check.Reason, "detached") {
		t.Fatalf("reason = %q", res.Check.Reason)
	}
}

// TestBranchHeadResolvesPersistedAustinRef covers the legacy fallback where the
// snapshot lost its integration head but the branch still exists.
func TestBranchHeadResolvesPersistedAustinRef(t *testing.T) {
	repo := initRepo(t)
	final := produceFinal(t, repo, "duo/s/austin", "result.md", "hello Duo\n", "add result.md")

	got, err := BranchHead(context.Background(), repo, "duo/s/austin")
	if err != nil {
		t.Fatal(err)
	}
	if got != final {
		t.Fatalf("BranchHead = %s, want %s", got, final)
	}
}

func baseOf(t *testing.T, repo, final string) string {
	t.Helper()
	return strings.TrimSpace(run(t, repo, "git", "merge-base", final, "HEAD"))
}
