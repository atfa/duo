package session

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
		LaunchDir: repo,
		BaseDir:   baseDir,
		Session:   "headless",
		Listen:    listen,
		Mode:      project.ModeFast,
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
