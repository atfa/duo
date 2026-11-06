package xduo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/workspace"
)

// paneDeadline is how long a test waits for tmux to show an effect. It is
// generous because a full `go test ./...` run competes with the tmux server it
// polls, and a busy machine must not turn a slow pane into a false failure.
const paneDeadline = 10 * time.Second

func TestTmuxLifecycleAndLiteralArguments(t *testing.T) {
	tm, err := FindTmux()
	if err != nil {
		t.Skip(err)
	}
	tm.Socket = "xduo-test-" + strings.ReplaceAll(filepath.Base(t.TempDir()), ".", "")
	ctx := context.Background()
	defer tm.run(ctx, "kill-server")
	dir := t.TempDir()
	project := filepath.Join(dir, "project $(touch injected) with spaces")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", project).CombinedOutput(); err != nil {
		t.Fatalf("git: %s %v", out, err)
	}
	executable := filepath.Join(dir, "duo with spaces")
	script := "#!/bin/sh\nprintf '%s\n%s\n%s\n' \"$1\" \"$XDUO_STATUS_FILE\" \"$DUO_REMOTE_PAIRING_FILE\" > \"$1/result\"\ntrap 'exit 0' TERM\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	i, err := Create(ctx, tm, filepath.Join(dir, "config"), project, executable)
	if err != nil {
		t.Fatal(err)
	}
	option, err := tm.execute(ctx, "show-options", "-v", "-t", i.Session, "detach-on-destroy")
	if err != nil || strings.TrimSpace(string(option)) != "off" {
		t.Fatalf("detach-on-destroy: %q %v", option, err)
	}
	hint, err := tm.execute(ctx, "display-message", "-p", "-t", i.Session, "#{status-right}")
	if err != nil || !strings.Contains(string(hint), "back to xduo") {
		t.Fatalf("return hint: %q %v", hint, err)
	}
	// Create canonicalises the project through Git, which reports resolved paths
	// (a t.TempDir() on macOS is /var/..., Git says /private/var/...). The pane must
	// receive that spelling literally rather than re-derived by a shell.
	if !workspace.SamePath(i.Project, project) {
		t.Fatalf("instance project %q does not name the requested %q", i.Project, project)
	}
	want := i.Project + "\n" + i.StatusFile + "\n" + i.PairingFile + "\n"
	for deadline := time.Now().Add(paneDeadline); ; {
		data, err := os.ReadFile(filepath.Join(project, "result"))
		if err == nil {
			if string(data) != want {
				t.Fatalf("arguments: %q, want %q", data, want)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(project, "injected")); !os.IsNotExist(err) {
		t.Fatal("shell expansion occurred")
	}
	alive, err := tm.Alive(i.Session)
	if err != nil || !alive {
		t.Fatalf("alive: %v %v", alive, err)
	}
	if err := tm.Close(ctx, i, DisplayStatus{}, false, time.Second); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("confirmation: %v", err)
	}
	if err := tm.Close(ctx, i, DisplayStatus{Fresh: true, Snapshot: Status{Phase: "DONE"}}, false, time.Second); err != nil {
		t.Fatal(err)
	}
	alive, err = tm.Alive(i.Session)
	if err != nil || alive {
		t.Fatalf("closed: %v %v", alive, err)
	}
}

func TestValidateProjectAndDuoErrors(t *testing.T) {
	if _, err := ValidateProject(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing project accepted")
	}
	if _, err := ValidateProject(t.TempDir()); err == nil {
		t.Fatal("non-git project accepted")
	}
	if _, err := ResolveDuo(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing executable accepted")
	}
}

func TestTmuxArgumentsAndGracefulTimeout(t *testing.T) {
	var calls [][]string
	tm := Tmux{Path: "tmux", Runner: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return nil, nil
	}}
	i := Instance{Session: "xduo-abc", Project: "/tmp/project with spaces", StatusFile: "/tmp/status path", PairingFile: "/tmp/pair path"}
	if err := tm.Start(context.Background(), i, "/tmp/duo path"); err != nil {
		t.Fatal(err)
	}
	want := []string{"new-session", "-d", "-s", i.Session, "-c", i.Project, "-e", "XDUO_STATUS_FILE=" + i.StatusFile, "-e", "DUO_REMOTE_PAIRING_FILE=" + i.PairingFile, "/tmp/duo path", i.Project}
	if !reflect.DeepEqual(calls[0], want) {
		t.Fatalf("start argv: %#v", calls[0])
	}
	if err := tm.Close(context.Background(), i, DisplayStatus{}, true, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	n := 4 + len(StatusStyle()) + len(StatusContent("")) + len(StatusHint()) + 1
	if !reflect.DeepEqual(calls[1], []string{"set-option", "-t", "xduo-abc", "detach-on-destroy", "off"}) || !reflect.DeepEqual(calls[2], []string{"set-option", "-t", "xduo-abc", "status-right-length", "60"}) || !reflect.DeepEqual(calls[3], []string{"set-option", "-t", "xduo-abc", "status-right", StatusRightText}) || !reflect.DeepEqual(calls[n], []string{"has-session", "-t", "=xduo-abc"}) || !reflect.DeepEqual(calls[n+1], []string{"send-keys", "-t", "=xduo-abc:", "C-q"}) || !reflect.DeepEqual(calls[n+2], []string{"kill-session", "-t", "=xduo-abc"}) {
		t.Fatalf("close argv: %#v", calls)
	}
}

func TestTmuxStatusFormattingFailureDoesNotStopSession(t *testing.T) {
	var switched bool
	tm := Tmux{Runner: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "set-option" && args[3] == "status-left" {
			return nil, errors.New("unsupported format")
		}
		if args[0] == "switch-client" {
			switched = true
		}
		return nil, nil
	}}
	i := Instance{Session: "xduo-test", Project: "/project"}
	if err := tm.Start(context.Background(), i, "duo"); err != nil {
		t.Fatal(err)
	}
	if err := tm.Switch(context.Background(), i); err != nil || !switched {
		t.Fatalf("switch: %v, called: %v", err, switched)
	}
}

func TestTmuxStatusStyleIsSessionScoped(t *testing.T) {
	t.Setenv("TMUX", "")
	tm, err := FindTmux()
	if err != nil {
		t.Skip(err)
	}
	tm.Socket = "xduo-style-" + filepath.Base(t.TempDir())
	ctx := context.Background()
	defer tm.run(ctx, "kill-server")
	if err := tm.run(ctx, "new-session", "-d", "-s", "other", "sleep", "30"); err != nil {
		t.Fatal(err)
	}
	global, err := tm.execute(ctx, "show-options", "-gv", "status-style")
	if err != nil {
		t.Fatal(err)
	}
	globalLeft, err := tm.execute(ctx, "show-options", "-gv", "status-left")
	if err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(t.TempDir(), "project-#{session_name}")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	i := Instance{Session: "xduo-test", Project: project}
	if err := tm.start(ctx, i, "sleep", "30"); err != nil {
		t.Fatal(err)
	}
	check := func(option, want string) {
		t.Helper()
		got, err := tm.execute(ctx, "show-options", "-Av", "-t", i.Session, option)
		if err != nil || strings.TrimSpace(string(got)) != want {
			t.Fatalf("%s: %q: %v", option, got, err)
		}
	}
	check("status-style", "bg=#141b22,fg=#8b98a8")
	check("window-status-current-style", "bg=#1d262d,fg=#6fdac1,bold")
	expand := func(format string) string {
		t.Helper()
		out, err := tm.execute(ctx, "display-message", "-p", "-t", i.Session, format)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	if got := expand("#{E:status-left}"); !strings.Contains(got, "Duo · project-#{session_name}") {
		t.Fatalf("project label: %q", got)
	}
	if got := expand("#{E:status-right}"); !strings.Contains(got, "C-b + Shift+L: back to xduo") {
		t.Fatalf("return hint: %q", got)
	}
	if got := expand("#{E:window-status-current-format}"); got != "" {
		t.Fatalf("single window: %q", got)
	}
	if err := tm.run(ctx, "new-window", "-d", "-t", i.Session, "-n", "second", "sleep", "30"); err != nil {
		t.Fatal(err)
	}
	if err := tm.run(ctx, "select-window", "-t", i.Session+":1"); err != nil {
		t.Fatal(err)
	}
	if got := expand("#{E:window-status-current-format}"); !strings.Contains(got, "second") {
		t.Fatalf("multiple windows: %q", got)
	}
	if err := tm.run(ctx, "set-option", "-t", i.Session, "status-style", "bg=red"); err != nil {
		t.Fatal(err)
	}
	if err := tm.run(ctx, "set-option", "-t", i.Session, "status-right", "old hint"); err != nil {
		t.Fatal(err)
	}
	_ = tm.Switch(ctx, i) // No attached client; styling precedes switching.
	check("status-style", "bg=#141b22,fg=#8b98a8")
	check("status-right", StatusRightText)
	other, err := tm.execute(ctx, "show-options", "-Av", "-t", "other", "status-style")
	if err != nil || string(other) != string(global) {
		t.Fatalf("other: %q, global: %q, err: %v", other, global, err)
	}
	after, err := tm.execute(ctx, "show-options", "-gv", "status-style")
	if err != nil || string(after) != string(global) {
		t.Fatalf("global changed: %q to %q: %v", global, after, err)
	}
	otherLeft, err := tm.execute(ctx, "show-options", "-Av", "-t", "other", "status-left")
	if err != nil || string(otherLeft) != string(globalLeft) {
		t.Fatalf("other status-left: %q: %v", otherLeft, err)
	}
	afterLeft, err := tm.execute(ctx, "show-options", "-gv", "status-left")
	if err != nil || string(afterLeft) != string(globalLeft) {
		t.Fatalf("global status-left: %q: %v", afterLeft, err)
	}
}

// Exercise the real target-pane syntax and verify normal exit before timeout.
func TestRemoveSendsQuitToRealTmuxPane(t *testing.T) {
	tm, err := FindTmux()
	if err != nil {
		t.Skip(err)
	}
	tm.Socket = "xduo-quit-" + filepath.Base(t.TempDir())
	ctx := context.Background()
	defer tm.run(ctx, "kill-server")
	dir := t.TempDir()
	i, err := NewInstance(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(dir, "instances.json")
	if err = SaveRegistry(registry, []Instance{i}); err != nil {
		t.Fatal(err)
	}
	// A raw terminal reader exits upon receiving Ctrl+Q, emulating Duo's quit key.
	script := filepath.Join(dir, "reader")
	if err = os.WriteFile(script, []byte("#!/bin/sh\nstty raw -echo\ntouch \"$1/reader-ready\"\ndd bs=1 count=1 of=/dev/null 2>/dev/null\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = tm.Start(ctx, i, script); err != nil {
		t.Fatal(err)
	}
	// Wait until the reader has configured its terminal before sending the key.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "reader-ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reader did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	start := time.Now()
	if err = Remove(ctx, tm, dir, i, DisplayStatus{}, true, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) >= 2*time.Second {
		t.Fatal("quit required timeout fallback")
	}
	alive, err := tm.Alive(i.Session)
	if err != nil || alive {
		t.Fatalf("alive=%v err=%v", alive, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "instances", i.ID)); !os.IsNotExist(err) {
		t.Fatalf("instance directory remains: %v", err)
	}
	records, err := LoadRegistry(registry)
	if err != nil || len(records) != 0 {
		t.Fatalf("records=%v err=%v", records, err)
	}
}

func TestDuoExitReturnsTmuxClient(t *testing.T) {
	tm, err := FindTmux()
	if err != nil {
		t.Skip(err)
	}
	tm.Socket = "xduo-return-" + filepath.Base(t.TempDir())
	ctx := context.Background()
	defer tm.run(ctx, "kill-server")
	if err := tm.run(ctx, "new-session", "-d", "-s", "xduo-manager", "sleep", "30"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "reader")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nstty raw -echo\ntouch \"$1/ready\"\ndd bs=1 count=1 of=/dev/null 2>/dev/null\n"), 0700); err != nil {
		t.Fatal(err)
	}
	i := Instance{Session: "xduo-test", Project: dir}
	if err := tm.Start(ctx, i, script); err != nil {
		t.Fatal(err)
	}
	client := exec.Command(tm.Path, "-L", tm.Socket, "-C", "attach-session", "-t", "xduo-manager")
	stdin, err := client.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { stdin.Close(); client.Process.Kill(); client.Wait() }()
	deadline := time.Now().Add(paneDeadline)
	for {
		out, _ := tm.execute(ctx, "list-clients", "-F", "#{session_name}")
		_, ready := os.Stat(filepath.Join(dir, "ready"))
		if strings.Contains(string(out), "xduo-manager") && ready == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("client or reader did not start: clients %q, reader %v", out, ready)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := tm.Switch(ctx, i); err != nil {
		t.Fatal(err)
	}
	if err := tm.run(ctx, "send-keys", "-t", i.Session+":", "C-d"); err != nil {
		t.Fatal(err)
	}
	// A fresh budget: the wait above may have used nearly all of the first one.
	deadline = time.Now().Add(paneDeadline)
	for {
		out, _ := tm.execute(ctx, "list-clients", "-F", "#{session_name}")
		if strings.TrimSpace(string(out)) == "xduo-manager" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("client did not return to manager: %q", out)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestResumeUsesRecordedSession(t *testing.T) {
	ctx := context.Background()
	project, err := ValidateProject(".")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	i, err := NewInstance(dir, project)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	tm := Tmux{Runner: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return nil, nil
	}}
	if err := Resume(ctx, tm, i, executable); err == nil || len(calls) != 0 {
		t.Fatal("missing status must not start a different session", err, calls)
	}
	if err := os.WriteFile(i.StatusFile, []byte(`{"session_id":"saved-session-123","process":"exited"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Resume(ctx, tm, i, executable); err != nil {
		t.Fatal(err)
	}
	args := calls[0]
	if !reflect.DeepEqual(args[len(args)-3:], []string{executable, "resume", "saved-session-123"}) {
		t.Fatal(args)
	}
	if !reflect.DeepEqual(args[:7], []string{"new-session", "-d", "-s", i.Session, "-c", i.Project, "-e"}) {
		t.Fatal(args)
	}
	if _, err := os.Stat(i.StatusFile); err != nil {
		t.Fatal("resume removed original status", err)
	}
}

func TestFailedResumeKeepsErrorUntilAcknowledged(t *testing.T) {
	tm, err := FindTmux()
	if err != nil {
		t.Skip(err)
	}
	tm.Socket = "xduo-resume-error-" + filepath.Base(t.TempDir())
	ctx := context.Background()
	defer tm.run(ctx, "kill-server")
	// Keep the manager alive, as in the application, so the final probe does
	// not race tmux's automatic shutdown after its last session exits.
	if err := tm.run(ctx, "new-session", "-d", "-s", "xduo-manager", "sleep", "30"); err != nil {
		t.Fatal(err)
	}
	project, err := ValidateProject(".")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	i, err := NewInstance(dir, project)
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(dir, "duo with spaces")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf 'Cannot resume: session does not exist\\n'\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(i.StatusFile, []byte(`{"session_id":"missing-session","process":"exited"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Resume(ctx, tm, i, executable); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		out, err := tm.execute(ctx, "capture-pane", "-p", "-t", i.Session+":")
		if err == nil && strings.Contains(string(out), "Press Enter") {
			if !strings.Contains(string(out), "Cannot resume: session does not exist") || !strings.Contains(string(out), "exit 7") {
				t.Fatalf("missing error details: %s", out)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("error disappeared: %s %v", out, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := tm.run(ctx, "send-keys", "-t", i.Session+":", "Enter"); err != nil {
		t.Fatal(err)
	}
	for {
		alive, err := tm.Alive(i.Session)
		if err != nil {
			t.Fatal(err)
		}
		if !alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("acknowledged error did not close")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
