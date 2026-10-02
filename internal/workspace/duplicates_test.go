package workspace

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/protocol"
)

// TestDuplicateCommitsFindsCherryPicks covers the property the check exists for:
// a commit whose change is already on the peer branch is reported against the
// agent that owns the redundant copy, and genuinely separate work is not
// reported. It also pins the notice, because the fix it asks for is only useful
// if it names the owner and says which agent has to act.
func TestDuplicateCommitsFindsCherryPicks(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)

	m := NewGitManager(GitConfig{
		Repository: repo,
		Root:       t.TempDir() + "/worktrees",
		Session:    "dup-session",
	})
	set, err := m.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Both agents do their own distinct work: neither should be reported.
	writeAndCommit(t, set.Austin.Path, "austin.txt", "austin\n", "feat: austin work")
	writeAndCommit(t, set.Tony.Path, "tony.txt", "tony\n", "feat: tony work")

	if dupes, err := m.DuplicateCommits(ctx); err != nil {
		t.Fatalf("DuplicateCommits: %v", err)
	} else if len(dupes) != 0 {
		t.Fatalf("independent work reported as duplicate: %+v", dupes)
	}

	// Tony then cherry-picks Austin's commit, which is the mistake this catches.
	// The message is edited on the way over, so only a patch comparison finds it.
	austinSHA := headSHA(t, set.Austin.Path)
	cherryPick(t, set.Tony.Path, austinSHA)
	runGit(t, set.Tony.Path, "commit", "--amend", "-m", "feat: austin work (shared)")

	dupes, err := m.DuplicateCommits(ctx)
	if err != nil {
		t.Fatalf("DuplicateCommits: %v", err)
	}
	if len(dupes) != 1 {
		t.Fatalf("got %d duplicated changes, want 1: %+v", len(dupes), dupes)
	}
	sides := map[protocol.AgentID]DuplicateSide{}
	for _, s := range dupes[0].Sides {
		sides[s.Agent] = s
	}
	if len(sides) != 2 {
		t.Fatalf("the change is not reported on both branches: %+v", dupes[0].Sides)
	}
	tony, ok := sides[protocol.Tony]
	if !ok {
		t.Fatalf("Tony's copy missing: %+v", dupes[0].Sides)
	}
	// The cherry-picked commit's message was rewritten, so the subject can only
	// come from the commit itself, not from git cherry's line.
	if tony.Subject != "feat: austin work (shared)" {
		t.Errorf("subject = %q, want the cherry-picked commit's own subject", tony.Subject)
	}
	if tony.SHA != headSHA(t, set.Tony.Path) {
		t.Errorf("SHA = %q, want Tony's tip %q", tony.SHA, headSHA(t, set.Tony.Path))
	}
	if austin, ok := sides[protocol.Austin]; !ok || austin.SHA != austinSHA {
		t.Errorf("Austin's original not paired with the copy: %+v", dupes[0].Sides)
	}

	notice := DuplicateCommitNotice(dupes)
	for _, want := range []string{
		"1 change is carried by both branches",
		tony.Subject,
		austinSHA[:7],
		"Duo cannot name the owner",
		"does not rewrite agent branches",
		"No signature is bound to either branch yet",
	} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice missing %q:\n%s", want, notice)
		}
	}
	// Both agents are named: Duo cannot know who ran the cherry-pick.
	if !strings.Contains(notice, "Austin") || !strings.Contains(notice, "Tony") {
		t.Errorf("notice must show both copies:\n%s", notice)
	}
	if DuplicateCommitNotice(nil) != "" {
		t.Error("notice must be empty when there is nothing to report")
	}
}

// TestDuplicateCommitsIgnoresCommitsThatOnlyShareASubject guards against a
// false positive that would be worse than the bug: two agents can independently
// make the same change. That is not a cherry-pick, and the merge resolves it
// without ambiguity because the patches differ.
func TestDuplicateCommitsIgnoresCommitsThatOnlyShareASubject(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)

	m := NewGitManager(GitConfig{
		Repository: repo,
		Root:       t.TempDir() + "/worktrees",
		Session:    "same-subject",
	})
	set, err := m.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}

	writeAndCommit(t, set.Austin.Path, "a.txt", "a\n", "feat: shared subject")
	writeAndCommit(t, set.Tony.Path, "b.txt", "b\n", "feat: shared subject")

	dupes, err := m.DuplicateCommits(ctx)
	if err != nil {
		t.Fatalf("DuplicateCommits: %v", err)
	}
	if len(dupes) != 0 {
		t.Fatalf("distinct patches with an identical subject were reported: %+v", dupes)
	}
}

func headSHA(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
}

// cherryPick applies a commit onto the current branch. runGit already fails the
// test on a non-zero exit, so nothing is returned to inspect.
func cherryPick(t *testing.T, dir, sha string) {
	t.Helper()
	runGit(t, dir, "cherry-pick", sha)
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
