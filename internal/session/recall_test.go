package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
)

// gitText runs one Git command in dir and returns its trimmed output.
func gitText(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// commitIn writes one file in dir and commits it, the way an agent's work reaches
// its branch.
func commitIn(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "-q", "-m", "work: "+name)
	return gitText(t, dir, "rev-parse", "HEAD")
}

// TestRecallRefusedWhenDelivered covers the states a recall must never touch: the
// artifact already reached the human's repository, so the way back is git revert,
// not a branch moved out from under them.
func TestRecallRefusedWhenDelivered(t *testing.T) {
	svc := New(Options{})
	svc.delivery = sessionstore.Delivery{Status: sessionstore.DeliveryApplied, AppliedHead: "abc"}
	reason := svc.recallBlocked()
	if reason == "" {
		t.Fatal("a delivered session was reported as recallable")
	}
	if !strings.Contains(reason, "git revert") {
		t.Errorf("the refusal does not name the way back: %q", reason)
	}

	done := New(Options{})
	state := project.NewStateFor(project.ModeGoal)
	if err := state.Restore(project.Snapshot{Mode: project.ModeGoal, Phase: project.PhaseDone}); err != nil {
		t.Fatalf("restore DONE state: %v", err)
	}
	done.state = state
	if reason := done.recallBlocked(); reason == "" {
		t.Fatal("a DONE session was reported as recallable")
	}

	// The same refusal through the verb a frontend sends: nothing is stopped and
	// nothing is deleted, so the session is exactly as resumable as before.
	round := startSessionRound(t, t.Context(), project.ModeGoal)
	if err := round.svc.state.Restore(project.Snapshot{Mode: project.ModeGoal, Phase: project.PhaseDone}); err != nil {
		t.Fatalf("restore DONE state on a live session: %v", err)
	}
	if err := round.svc.Do(t.Context(), Recall{}); err == nil {
		t.Fatal("a DONE session accepted a recall")
	}
	if round.svc.View().NewSession {
		t.Error("a refused recall still asked for a fresh session")
	}
	austin, _ := round.svc.View().Worktrees.For(protocol.Austin)
	if _, err := os.Stat(austin.Path); err != nil {
		t.Errorf("a refused recall removed the worktree: %v", err)
	}
	record := filepath.Join(round.baseDir, sessionstore.RepoID(round.repo), round.session)
	if _, err := os.Stat(record); err != nil {
		t.Errorf("a refused recall removed the session record: %v", err)
	}
}

// TestRecallRefusedWhenStepLanded covers the per-step delivery of a Step session:
// a queue whose first step already landed must not be recallable even though the
// session itself is not DONE.
func TestRecallRefusedWhenStepLanded(t *testing.T) {
	svc := New(Options{})
	state := project.NewStateFor(project.ModeStep)
	if err := state.Restore(project.Snapshot{
		Mode:  project.ModeStep,
		Phase: project.PhasePlan,
		Steps: project.Steps{
			{Title: "one", Done: true, DeliveryHead: "abc123"},
			{Title: "two"},
		},
	}); err != nil {
		t.Fatalf("restore step state: %v", err)
	}
	svc.state = state

	reason := svc.recallBlocked()
	if !strings.Contains(reason, "step 1") {
		t.Fatalf("a landed step was not named in the refusal: %q", reason)
	}
}

// TestRecallPreviewReportsTheWorktrees covers what the confirmation panel is built
// from: the commits each branch holds on top of the base, and whether its worktree
// has uncommitted changes.
func TestRecallPreviewReportsTheWorktrees(t *testing.T) {
	round := startSessionRound(t, t.Context(), project.ModeGoal)
	austin, ok := round.svc.View().Worktrees.For(protocol.Austin)
	if !ok {
		t.Fatal("the session recorded no Austin worktree")
	}

	preview, err := round.svc.RecallPreview(t.Context())
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !preview.Allowed {
		t.Fatalf("a fresh session was not recallable: %s", preview.Reason)
	}
	if preview.BaseCommit == "" {
		t.Error("the preview does not say where the session started")
	}
	if preview.Austin.Commits != 0 || preview.Austin.Dirty {
		t.Errorf("a fresh worktree reported %+v", preview.Austin)
	}

	commitIn(t, austin.Path, "austin.txt")
	if err := os.WriteFile(filepath.Join(austin.Path, "uncommitted.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	preview, err = round.svc.RecallPreview(t.Context())
	if err != nil {
		t.Fatalf("preview after work: %v", err)
	}
	if preview.Austin.Commits != 1 || !preview.Austin.Dirty {
		t.Errorf("Austin worktree reported %+v, want one commit and uncommitted changes", preview.Austin)
	}
	if preview.Austin.Branch != austin.Branch {
		t.Errorf("branch = %q, want %q", preview.Austin.Branch, austin.Branch)
	}
}

// TestRecallDiscardsWorktreesRecordAndTranscripts is the whole feature: a started
// session with commits and uncommitted work is recalled, and everything it owned
// is gone while a note naming the discarded branch heads survives.
func TestRecallDiscardsWorktreesRecordAndTranscripts(t *testing.T) {
	round := startSessionRound(t, t.Context(), project.ModeGoal)
	round.svc.state.MarkStarted()

	set := round.svc.View().Worktrees
	heads := make(map[protocol.AgentID]string)
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		wt, ok := set.For(agent)
		if !ok || wt.Path == "" {
			t.Fatalf("no worktree for %s", agent)
		}
		heads[agent] = commitIn(t, wt.Path, strings.ToLower(string(agent))+".txt")
	}
	austin, _ := set.For(protocol.Austin)
	if err := os.WriteFile(filepath.Join(austin.Path, "wip.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := round.svc.Do(t.Context(), Recall{}); err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !round.svc.View().NewSession {
		t.Fatal("the recall did not ask for a fresh session")
	}

	closed := round.svc.Close()
	if !closed.Discarded {
		t.Fatalf("the recalled session was not reported as discarded: %+v", closed)
	}

	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		wt, _ := set.For(agent)
		if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
			t.Errorf("%s worktree still exists at %s (err=%v)", agent, wt.Path, err)
		}
		if branches := gitText(t, round.repo, "branch", "--list", wt.Branch); branches != "" {
			t.Errorf("%s branch %s still exists: %q", agent, wt.Branch, branches)
		}
	}

	record := filepath.Join(round.baseDir, sessionstore.RepoID(round.repo), round.session)
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Errorf("the session record still exists at %s (err=%v)", record, err)
	}

	note, err := os.ReadFile(filepath.Join(round.repo, ".duo", sessionstore.LogsDirName, sessionstore.RecallNoteName(round.session)))
	if err != nil {
		t.Fatalf("the recall note did not survive: %v", err)
	}
	text := string(note)
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		if !strings.Contains(text, heads[agent]) {
			t.Errorf("the note does not name %s's discarded head %s:\n%s", agent, heads[agent], text)
		}
	}
	if !strings.Contains(text, set.BaseCommit) {
		t.Errorf("the note does not say where the session started:\n%s", text)
	}
}

// TestRecallKeepsSessionWhenTheNoteCannotBeWritten covers the half-finished
// recall: when the record of the discarded work cannot be written, nothing is
// deleted and the session is left exactly as resumable as it was.
func TestRecallKeepsSessionWhenTheNoteCannotBeWritten(t *testing.T) {
	round := startSessionRound(t, t.Context(), project.ModeGoal)
	round.svc.state.MarkStarted()
	set := round.svc.View().Worktrees
	austin, _ := set.For(protocol.Austin)
	commitIn(t, austin.Path, "austin.txt")

	// .duo/logs is where the note goes; a file in its place is the failure every
	// read-only or foreign-owned logs directory would produce.
	logs := filepath.Join(round.repo, ".duo", sessionstore.LogsDirName)
	if err := os.RemoveAll(logs); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logs, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := round.svc.Do(t.Context(), Recall{}); err != nil {
		t.Fatalf("recall: %v", err)
	}
	closed := round.svc.Close()
	if closed.Discarded {
		t.Fatal("a recall whose note failed was reported as discarded")
	}
	if round.svc.View().NewSession {
		t.Error("a failed recall still asked for a fresh session")
	}
	if _, err := os.Stat(austin.Path); err != nil {
		t.Errorf("the worktree was removed even though the recall failed: %v", err)
	}
	record := filepath.Join(round.baseDir, sessionstore.RepoID(round.repo), round.session)
	if _, err := os.Stat(record); err != nil {
		t.Errorf("the session record was removed even though the recall failed: %v", err)
	}
}

// TestCloseWithoutRecallStillPreservesSession pins the behaviour recall must not
// change: an ordinary close keeps the worktrees and the record.
func TestCloseWithoutRecallStillPreservesSession(t *testing.T) {
	round := startSessionRound(t, t.Context(), project.ModeGoal)
	round.svc.state.MarkStarted()
	austin, _ := round.svc.View().Worktrees.For(protocol.Austin)
	commitIn(t, austin.Path, "austin.txt")

	closed := round.svc.Close()
	if closed.Discarded {
		t.Fatal("an ordinary close reported the session as discarded")
	}
	if round.svc.View().NewSession {
		t.Error("an ordinary close asked for a fresh session")
	}
	if _, err := os.Stat(austin.Path); err != nil {
		t.Errorf("an ordinary close removed the worktree: %v", err)
	}
}
