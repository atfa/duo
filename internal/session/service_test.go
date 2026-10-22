package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// gitRepo builds a repository with one commit, which is all a session needs to
// create its worktrees.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "initial")
	return dir
}

// fakeAgent is a stand-in for a coding agent: it holds the process open so the
// session has something to start, and does nothing else. This test is about the
// session lifecycle, not about what an agent does with a prompt.
func fakeAgent(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-agent.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// freeAddr reserves a port and gives the address back, so a test can hand the
// same address to two sessions in a row. The default of :0 would defeat that:
// a fresh random port is never already in use, so it could not catch a listener
// the previous session failed to close.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func headlessOptions(t *testing.T, repo, baseDir, listen string) Options {
	agent := fakeAgent(t)
	return Options{
		DefaultAgentDriver: "pi",
		LaunchDir:          repo,
		BaseDir:            baseDir,
		Session:            "headless",
		Listen:             listen,
		Mode:               project.ModeFast,
		AgentCommands: map[protocol.AgentID]string{
			protocol.Austin: agent,
			protocol.Tony:   agent,
		},
	}
}

// resumeOptions is the same session, resumed rather than created.
func resumeOptions(t *testing.T, repo, baseDir, listen string) Options {
	opts := headlessOptions(t, repo, baseDir, listen)
	opts.Resume = true
	opts.ResumeSession = opts.Session
	return opts
}

// A frontend that is not a terminal must be able to run a session: start it, read
// its View, receive its events, and close it.
func TestSessionRunsHeadless(t *testing.T) {
	repo := gitRepo(t)
	svc := New(headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0"))

	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Close()

	view := svc.View()
	if view.SessionID == "" {
		t.Error("View has no session id")
	}
	if view.Version == "" {
		t.Error("View has no version")
	}
	if view.Mode != project.ModeFast {
		t.Errorf("View.Mode = %v, want Fast", view.Mode)
	}
	if view.Project.Phase == "" {
		t.Error("View has no workflow phase")
	}
	if view.Worktrees.Austin.Path == "" || view.Worktrees.Tony.Path == "" {
		t.Errorf("View has no worktrees: %+v", view.Worktrees)
	}

	sub, unsubscribe := svc.Subscribe(64)
	defer unsubscribe()
	// The View arrives with the subscription, so a renderer that connects late
	// never has to ask for state it missed.
	if sub.View.SessionID != view.SessionID {
		t.Errorf("Subscribed View session = %q, want %q", sub.View.SessionID, view.SessionID)
	}
}

func TestResumePendingOnlyForStartedWorkAndClearsOnExplicitResume(t *testing.T) {
	repo, baseDir := gitRepo(t), t.TempDir()
	first := New(headlessOptions(t, repo, baseDir, "127.0.0.1:0"))
	if _, err := first.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := first.Do(context.Background(), SubmitTask{Text: "start work"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	first.Close()

	resumed := New(resumeOptions(t, repo, baseDir, "127.0.0.1:0"))
	if _, err := resumed.Start(context.Background()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	defer resumed.Close()
	if !resumed.View().ResumePending {
		t.Fatal("started resumed session should expose ResumePending")
	}
	if err := resumed.Do(context.Background(), ResumeWork{}); err != nil {
		t.Fatalf("explicit resume: %v", err)
	}
	if resumed.View().ResumePending || resumed.coord.HarnessHeld() {
		t.Fatal("ResumeWork did not clear ResumePending")
	}
	if err := resumed.Do(context.Background(), ResumeWork{}); err == nil || !strings.Contains(err.Error(), "already resumed") {
		t.Fatalf("duplicate ResumeWork error = %v", err)
	}
	resumed.coord.HoldResume()
	if err := resumed.Do(context.Background(), SubmitTask{Text: "new human task"}); err != nil {
		t.Fatalf("submit after resume: %v", err)
	}
	if resumed.View().ResumePending || resumed.coord.HarnessHeld() {
		t.Fatal("SubmitTask did not clear pending resume and harness hold")
	}
}

func TestUnstartedResumeDoesNotShowResumePending(t *testing.T) {
	repo, baseDir := gitRepo(t), t.TempDir()
	first := New(headlessOptions(t, repo, baseDir, "127.0.0.1:0"))
	if _, err := first.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	id := first.View().SessionID
	if err := first.Do(context.Background(), SubmitTask{Text: "create resumable snapshot"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	first.Close()
	store, err := sessionstore.Open(baseDir, sessionstore.RepoID(repo), id)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	snap.Started = false
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}

	opts := resumeOptions(t, repo, baseDir, "127.0.0.1:0")
	opts.ResumeSession = id
	resumed := New(opts)
	if _, err := resumed.Start(context.Background()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	defer resumed.Close()
	if resumed.View().ResumePending {
		t.Fatal("unstarted resumed session must not advertise /resume")
	}
	if err := resumed.Do(context.Background(), ResumeWork{}); err == nil || !strings.Contains(err.Error(), "submit a task first") {
		t.Fatalf("ResumeWork on unstarted session error = %v", err)
	}
}

func TestResumeCompletedSessionKeepsDoneUntilFollowUpTask(t *testing.T) {
	repo, baseDir := gitRepo(t), t.TempDir()
	listen := "127.0.0.1:0"
	started := New(headlessOptions(t, repo, baseDir, listen))
	if _, err := started.Start(context.Background()); err != nil {
		t.Fatalf("start initial session: %v", err)
	}
	sessionID := started.View().SessionID
	if err := started.Do(context.Background(), SubmitTask{Text: "initial task"}); err != nil {
		t.Fatalf("submit initial task: %v", err)
	}
	started.Close()

	store, err := sessionstore.Open(baseDir, sessionstore.RepoID(repo), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	snap.Phase = string(project.PhaseDone)
	snap.Integration = sessionstore.Integration{Head: "delivered-head", MergedTony: "tony-head"}
	snap.Delivery = sessionstore.Delivery{
		Status:      sessionstore.DeliveryApplied,
		FinalHead:   "delivered-head",
		AppliedHead: "delivered-head",
	}
	if err := store.Save(snap); err != nil {
		t.Fatal(err)
	}

	resumed := New(resumeOptions(t, repo, baseDir, listen))
	result, err := resumed.Start(context.Background())
	if err != nil {
		t.Fatalf("resume completed session: %v", err)
	}
	defer resumed.Close()
	if result.Phase != project.PhaseDone || resumed.View().Project.Phase != project.PhaseDone {
		t.Fatalf("resumed phase = result:%s view:%s, want DONE until follow-up task", result.Phase, resumed.View().Project.Phase)
	}
	if err := resumed.Do(context.Background(), ResumeWork{}); err == nil || !strings.Contains(err.Error(), "DONE") {
		t.Fatalf("ResumeWork in DONE error = %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != string(project.PhaseDone) || got.Delivery.Status != sessionstore.DeliveryApplied || got.Integration.Head != "delivered-head" {
		t.Fatalf("completed round changed during resume: phase=%s delivery=%+v integration=%+v", got.Phase, got.Delivery, got.Integration)
	}
}

func TestPauseCommandsPersistAndRefreshTheSessionView(t *testing.T) {
	repo := gitRepo(t)
	svc := New(headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0"))
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Close()
	sub, cancel := svc.Subscribe(32)
	defer cancel()
	if sub.View.Pause.State != PauseRunning || len(sub.View.Pause.Paused) != 2 {
		t.Fatalf("initial pause view = %+v", sub.View.Pause)
	}
	if err := svc.Do(context.Background(), Pause{}); err != nil {
		t.Fatalf("pause: %v", err)
	}
	view := svc.View()
	if view.Pause.State != PausePaused || !view.Pause.Paused[protocol.Austin] || !view.Pause.Paused[protocol.Tony] {
		t.Fatalf("paused view = %+v", view.Pause)
	}
	found := false
	for len(sub.Events) > 0 {
		if event := <-sub.Events; event.Kind == events.KindSystem && strings.Contains(event.Text, "pause") {
			found = true
		}
	}
	if !found {
		t.Fatal("pause transition did not emit a system event")
	}
	snap, err := svc.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if snap.PauseState != string(PausePaused) {
		t.Fatalf("persisted pause = %q", snap.PauseState)
	}
	if err := svc.Do(context.Background(), SubmitTask{Text: "not while paused"}); err == nil {
		t.Fatal("task accepted while paused")
	}
	if err := svc.Do(context.Background(), Unpause{}); err == nil {
		t.Fatal("unpause after completed pause should fail")
	}
	svc.coord.HoldResume()
	if !svc.coord.ResumePending() || !svc.coord.HarnessHeld() {
		t.Fatal("test setup did not hold resume state")
	}
	if err := svc.Do(context.Background(), Continue{}); err != nil {
		t.Fatalf("continue: %v", err)
	}
	if svc.coord.ResumePending() || svc.coord.HarnessHeld() {
		t.Fatal("Continue did not clear pending resume and harness hold")
	}
	if got := svc.View().Pause.State; got != PauseRunning {
		t.Fatalf("state after Continue = %s", got)
	}
}

func TestPausedSessionResumesWithoutDroppingPauseState(t *testing.T) {
	repo, baseDir := gitRepo(t), t.TempDir()
	first := New(headlessOptions(t, repo, baseDir, "127.0.0.1:0"))
	if _, err := first.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	first.state.MarkStarted()
	if err := first.Do(context.Background(), Pause{}); err != nil {
		t.Fatalf("pause: %v", err)
	}
	first.Close()

	resumed := New(resumeOptions(t, repo, baseDir, "127.0.0.1:0"))
	if _, err := resumed.Start(context.Background()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	defer resumed.Close()
	view := resumed.View()
	if view.Pause.State != PausePaused || !view.Pause.Paused[protocol.Austin] || !view.Pause.Paused[protocol.Tony] {
		t.Fatalf("resumed pause view = %+v", view.Pause)
	}
	if err := resumed.Do(context.Background(), ResumeWork{}); err == nil || !strings.Contains(err.Error(), "/continue") {
		t.Fatalf("ResumeWork while paused error = %v", err)
	}
}

// The session lock and the bridge listener are both exclusive and both last as
// long as the process, and Start acquires them while Close releases them. A
// frontend that closes a session and opens another in the same process must
// therefore work, on the same address: a leaked lock or a leaked listener turns
// this into a refusal, and neither shows up in a test that starts one session.
func TestStartCloseStartCloseInOneProcess(t *testing.T) {
	repo := gitRepo(t)
	baseDir := t.TempDir()
	listen := freeAddr(t)

	first := New(headlessOptions(t, repo, baseDir, listen))
	if _, err := first.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	// A session that never started is discarded on close, which is the contract
	// TestAnUnstartedSessionLeavesNothingBehind covers. These tests need one that
	// survives Close, so the started flag is set the way the workflow sets it.
	first.state.MarkStarted()
	first.Close()

	second := New(resumeOptions(t, repo, baseDir, listen))
	if _, err := second.Start(context.Background()); err != nil {
		t.Fatalf("resume after close: %v", err)
	}
	second.Close()
}

// Starting the same session twice while the first is still running must fail on
// the lock. The lock is the only thing standing between two processes and one
// session's records.
func TestConcurrentStartOfTheSameSessionIsRefusedByTheLock(t *testing.T) {
	repo := gitRepo(t)
	baseDir := t.TempDir()

	seed := New(headlessOptions(t, repo, baseDir, "127.0.0.1:0"))
	if _, err := seed.Start(context.Background()); err != nil {
		t.Fatalf("seed start: %v", err)
	}
	// See TestStartCloseStartCloseInOneProcess: a session that never started is
	// discarded, and this test needs one on disk to contend for.
	seed.state.MarkStarted()
	seed.Close()

	first := New(resumeOptions(t, repo, baseDir, "127.0.0.1:0"))
	if _, err := first.Start(context.Background()); err != nil {
		t.Fatalf("first resume: %v", err)
	}
	defer first.Close()

	second := New(resumeOptions(t, repo, baseDir, "127.0.0.1:0"))
	_, err := second.Start(context.Background())
	if err == nil {
		second.Close()
		t.Fatal("a second start of a running session succeeded")
	}
	var locked *sessionstore.LockedError
	if !errors.As(err, &locked) {
		t.Fatalf("second start = %v, want a LockedError", err)
	}
}

// Before Start and after Close there is no session, and a command must say so
// rather than acting on a half-built one.
func TestCommandsAreRefusedOutsideARunningSession(t *testing.T) {
	repo := gitRepo(t)
	svc := New(headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0"))

	if err := svc.Do(context.Background(), SetModel{Agent: protocol.Austin, Provider: "pi", Model: "x"}); !errors.Is(err, errNotRunning) {
		t.Fatalf("Do before Start = %v, want errNotRunning", err)
	}

	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	svc.Close()

	// After Close the refusal is the stronger one: the session is finished, not
	// merely unstarted.
	if err := svc.Do(context.Background(), SetModel{Agent: protocol.Austin, Provider: "pi", Model: "x"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Do after Close = %v, want ErrClosed", err)
	}
	// Close is idempotent: a frontend may call it from a deferred cleanup as well.
	svc.Close()
}

// testSessionStore opens a throwaway session directory, because the lifecycle
// observer records every transition before it reports it.
func testSessionStore(t *testing.T) *sessionstore.Store {
	t.Helper()
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "repo", "s1"), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := sessionstore.Open(base, "repo", "s1")
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// errorsIn drains the events of one kind a session published, so a test can ask
// what the human would have been shown.
func errorsIn(ch <-chan events.Event) []string {
	var found []string
	for len(ch) > 0 {
		if e := <-ch; e.Kind == events.KindError {
			found = append(found, e.Text)
		}
	}
	return found
}

// An agent that ends on its own has to reach the human even when it printed
// nothing and exited 0. Codex gives up during TUI bootstrap that way, and a
// session that has quietly lost Tony otherwise waits on Tony forever.
func TestAnAgentThatExitsOnItsOwnIsReportedWithoutOutput(t *testing.T) {
	store := testSessionStore(t)
	svc := &Service{bus: events.NewBus(), journal: store.OpenEvents(), logger: store.OpenLog()}
	ch, cancel := svc.bus.Subscribe(8)
	defer cancel()

	svc.observeLifecycle(agent.LifecycleEvent{
		Kind: "agent_exit", Agent: protocol.Tony, State: agent.ProcessExited,
	})

	reported := errorsIn(ch)
	if len(reported) != 1 {
		t.Fatalf("exit errors = %v, want exactly one", reported)
	}
	if !strings.Contains(reported[0], "Tony") {
		t.Errorf("exit error does not name the agent that died:\n%s", reported[0])
	}
}

// A shutdown is not a failure: Duo asked for it, so it must stay out of the
// transcript or every session log ends with two invented errors.
func TestAnExitDuoAskedForIsNotReported(t *testing.T) {
	store := testSessionStore(t)
	svc := &Service{bus: events.NewBus(), journal: store.OpenEvents(), logger: store.OpenLog()}
	ch, cancel := svc.bus.Subscribe(8)
	defer cancel()

	svc.observeLifecycle(agent.LifecycleEvent{
		Kind: "agent_exit", Agent: protocol.Tony, State: agent.ProcessExited, Requested: true,
	})

	if reported := errorsIn(ch); len(reported) != 0 {
		t.Fatalf("requested stop reported %d error(s): %v", len(reported), reported)
	}
}

// A running agent with no bridge client must still count as reachable, because
// that is the property the watchdog depends on. A driver whose bridge is lazy —
// Codex only starts its MCP bridge the first time it calls a Duo tool — holds no
// client on the socket server at all, so a reachability gate that asks the
// server can never see such an agent and never nudges it.
func TestAReachabilityGateMustNotBeTheSocketServer(t *testing.T) {
	repo := gitRepo(t)
	svc := New(headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0"))
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	if svc.server.IsConnected(protocol.Tony) {
		t.Fatal("the stub agent is not expected to hold a bridge client; this test proves nothing if it does")
	}
	var sender harness.Sender = svc.coord
	if !sender.IsAgentConnected(protocol.Tony) {
		t.Fatal("a running agent with no bridge client must still be reachable, or the harness can never nudge it")
	}
}

// Leaving Duo without ever giving it a task must leave the repository and the
// disk exactly as they were. Today it leaves two worktrees, two branches, a
// resumable session and three transcript files; the point of this is that none of
// them exist afterwards, and that `duo sessions` therefore has nothing to offer a
// resume that would fail.
func TestAnUnstartedSessionLeavesNothingBehind(t *testing.T) {
	repo := gitRepo(t)
	baseDir := t.TempDir()

	svc := New(headlessOptions(t, repo, baseDir, "127.0.0.1:0"))
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	set := svc.set
	transcripts := transcriptsFor(t, repo, set.Session)

	if ended := svc.Close(); !ended.Discarded {
		t.Fatal("closing a session that never started did not report a discard")
	}

	if _, err := os.Stat(set.Austin.Path); !os.IsNotExist(err) {
		t.Errorf("Austin's worktree survived a discard: %v", err)
	}
	if _, err := os.Stat(set.Tony.Path); !os.IsNotExist(err) {
		t.Errorf("Tony's worktree survived a discard: %v", err)
	}
	for _, branch := range []string{set.Austin.Branch, set.Tony.Branch} {
		if out, err := gitOn(t, repo, "branch", "--list", branch); err != nil {
			t.Fatalf("git branch --list: %v", err)
		} else if strings.TrimSpace(out) != "" {
			t.Errorf("branch %s survived a discard", branch)
		}
	}
	for _, path := range transcripts {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("transcript %s survived a discard", filepath.Base(path))
		}
	}
	if _, err := sessionstore.Open(baseDir, sessionstore.RepoID(repo), set.Session); err == nil {
		t.Error("the session record survived a discard, so a resume would offer a session that no longer exists")
	}
}

// The discard is the only irreversible thing Duo does without being asked, so the
// case that must not reach it is a session that looks untouched but is not. Here
// the started flag lies — a driver that never reports a turn would produce exactly
// this — and Git is what says otherwise.
func TestAnUnstartedSessionWithWorkIsKept(t *testing.T) {
	repo := gitRepo(t)
	baseDir := t.TempDir()

	svc := New(headlessOptions(t, repo, baseDir, "127.0.0.1:0"))
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	set := svc.set
	// An uncommitted edit: nothing to resume would be able to reproduce it, and
	// the worktrees are where the human's work still is.
	writeFile(t, filepath.Join(set.Austin.Path, "hand-written.txt"), "mine\n")

	if ended := svc.Close(); ended.Discarded {
		t.Fatal("a session with uncommitted work was discarded")
	}
	if _, err := os.Stat(set.Austin.Path); err != nil {
		t.Errorf("Austin's worktree was removed despite holding work: %v", err)
	}
	if _, err := sessionstore.Open(baseDir, sessionstore.RepoID(repo), set.Session); err != nil {
		t.Errorf("a kept session must stay resumable: %v", err)
	}
}

// A session that started is a session the human may want to resume, whatever the
// worktrees look like: an agent that answered and then made nothing is still a
// conversation worth having.
func TestAStartedSessionIsNeverDiscarded(t *testing.T) {
	repo := gitRepo(t)
	baseDir := t.TempDir()

	svc := New(headlessOptions(t, repo, baseDir, "127.0.0.1:0"))
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	set := svc.set
	svc.state.MarkStarted()

	if ended := svc.Close(); ended.Discarded {
		t.Fatal("a started session was discarded")
	}
	if _, err := os.Stat(set.Austin.Path); err != nil {
		t.Errorf("a started session lost its worktree: %v", err)
	}
}

// transcriptsFor lists the three transcript files a session writes into the
// repository, which is where an unstarted run used to leave the only trace the
// human could see.
func transcriptsFor(t *testing.T, repo, sessionID string) []string {
	t.Helper()
	dir := filepath.Join(repo, ".duo", "logs")
	return []string{
		filepath.Join(dir, sessionID+".md"),
		filepath.Join(dir, sessionID+"-austin.md"),
		filepath.Join(dir, sessionID+"-tony.md"),
	}
}

func gitOn(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// /language must switch the language Duo writes in right now, report it through
// the View, persist it to the project config so a resume and the next session
// start in it, and refuse anything that is not zh or en without touching state.
func TestSetLanguageSwitchesAndPersists(t *testing.T) {
	repo := gitRepo(t)
	svc := New(headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0"))
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Close()

	if got := svc.View().Language; got != "en" {
		t.Fatalf("initial View.Language = %q, want en", got)
	}
	if err := svc.Do(context.Background(), SetLanguage{Language: " ZH "}); err != nil {
		t.Fatalf("SetLanguage zh: %v", err)
	}
	if got := svc.View().Language; got != "zh" {
		t.Fatalf("View.Language = %q, want zh", got)
	}
	data, err := os.ReadFile(filepath.Join(repo, ".duo", "config.json"))
	if err != nil {
		t.Fatalf("project config not written: %v", err)
	}
	var cfg struct {
		Language string `json:"language"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse project config: %v\n%s", err, data)
	}
	if cfg.Language != "zh" {
		t.Fatalf("persisted language = %q, want zh\n%s", cfg.Language, data)
	}

	if err := svc.Do(context.Background(), SetLanguage{Language: "fr"}); err == nil {
		t.Fatal("SetLanguage accepted fr")
	}
	if got := svc.View().Language; got != "zh" {
		t.Fatalf("rejected command changed language to %q, want zh", got)
	}
}

// /model's thinking control must write the level into the same project entry
// that carries the agent's driver and model, so the next launch in this
// repository starts with that effort.
func TestRememberThinkingPersistsConfirmedLevel(t *testing.T) {
	repo := gitRepo(t)
	svc := New(headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0"))
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Close()

	svc.rememberThinking(protocol.Tony, "xhigh")
	if got := svc.View().Agent(protocol.Tony).Thinking; got != "xhigh" {
		t.Fatalf("confirmed Tony thinking = %q, want xhigh", got)
	}
	data, err := os.ReadFile(filepath.Join(repo, ".duo", "config.json"))
	if err != nil {
		t.Fatalf("project config not written: %v", err)
	}
	var cfg struct {
		Agents map[string]struct {
			Thinking string `json:"thinking"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse project config: %v\n%s", err, data)
	}
	if got := cfg.Agents["tony"].Thinking; got != "xhigh" {
		t.Fatalf("persisted Tony thinking = %q, want xhigh\n%s", got, data)
	}
}

func TestCycleThinkingPersistsProjectConfig(t *testing.T) {
	repo := gitRepo(t)
	svc := New(headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0"))
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer svc.Close()

	if err := svc.Do(context.Background(), CycleThinking{Agent: protocol.Tony}); err != nil {
		t.Fatalf("CycleThinking: %v", err)
	}
	want := svc.View().Agent(protocol.Tony).Thinking
	if want == "" {
		t.Fatal("the session did not report the new Tony thinking level")
	}
	data, err := os.ReadFile(filepath.Join(repo, ".duo", "config.json"))
	if err != nil {
		t.Fatalf("project config not written: %v", err)
	}
	var cfg struct {
		Driver string `json:"driver"`
		Agents map[string]struct {
			Driver   string `json:"driver"`
			Thinking string `json:"thinking"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse project config: %v\n%s", err, data)
	}
	tony := cfg.Agents["tony"]
	if tony.Thinking != want {
		t.Fatalf("persisted Tony thinking = %q, want %q\n%s", tony.Thinking, want, data)
	}
	if tony.Driver == "" && cfg.Driver == "" {
		t.Fatalf("persisted thinking has no driver beside it:\n%s", data)
	}
}
