package xduo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/atfa/duo/internal/workspace"
)

// Tmux invokes tmux directly, with each argument kept separate.
type Tmux struct {
	Path string
	// Socket optionally selects a private server, useful for tests.
	Socket string
	Runner func(context.Context, string, ...string) ([]byte, error)
}

func FindTmux() (Tmux, error) {
	path, err := exec.LookPath("tmux")
	if err != nil {
		return Tmux{}, fmt.Errorf("xduo: tmux is required: %w", err)
	}
	return Tmux{Path: path}, nil
}

func (t Tmux) execute(ctx context.Context, args ...string) ([]byte, error) {
	if t.Socket != "" {
		args = append([]string{"-L", t.Socket}, args...)
	}
	if t.Runner != nil {
		return t.Runner(ctx, t.Path, args...)
	}
	return exec.CommandContext(ctx, t.Path, args...).CombinedOutput()
}

func (t Tmux) run(ctx context.Context, args ...string) error {
	out, err := t.execute(ctx, args...)
	if err != nil {
		return fmt.Errorf("tmux %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}

func ResolveDuo(path string) (string, error) {
	if path == "" {
		path = "duo"
	}
	resolved, err := exec.LookPath(path)
	if err != nil {
		return "", fmt.Errorf("xduo: Duo executable %q: %w; configure the xduo Duo path", path, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("xduo: Duo executable %q is not an executable regular file; configure the xduo Duo path", path)
	}
	return resolved, nil
}

func ValidateProject(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("xduo: project path %q: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("xduo: project path %q is not a directory", path)
	}
	root, err := workspace.FindRoot(context.Background(), path)
	if err != nil {
		return "", fmt.Errorf("xduo: %q is not a Git working tree: %w", path, err)
	}
	return root, nil
}

// Start uses tmux's multiple-command-argument form, which executes directly
// without interpreting the project path or executable as shell syntax.
func (t Tmux) Start(ctx context.Context, i Instance, duo string) error {
	return t.run(ctx, "new-session", "-d", "-s", i.Session, "-c", i.Project,
		"-e", "XDUO_STATUS_FILE="+i.StatusFile, "-e", "DUO_REMOTE_PAIRING_FILE="+i.PairingFile,
		duo, i.Project)
}

// Alive distinguishes an absent session from a tmux server failure where possible.
func (t Tmux) Alive(session string) (bool, error) {
	out, err := t.execute(context.Background(), "has-session", "-t", "="+session)
	if err == nil {
		return true, nil
	}
	msg := strings.TrimSpace(string(out))
	if strings.Contains(msg, "can't find session") || strings.Contains(msg, "no server running") || strings.Contains(msg, "no sessions") || (strings.Contains(msg, "error connecting to") && strings.Contains(msg, "No such file or directory")) {
		return false, nil
	}
	return false, fmt.Errorf("tmux has-session: %w: %s", err, msg)
}

var ErrConfirmationRequired = errors.New("xduo: confirmation required to close a running instance")

// Close sends the standard Duo quit key and waits for a normal exit. If it
// does not exit within timeout the tmux session is terminated.
func (t Tmux) Close(ctx context.Context, i Instance, state DisplayStatus, confirmed bool, timeout time.Duration) error {
	alive, err := t.Alive(i.Session)
	if err != nil || !alive {
		return err
	}
	if !(state.Fresh && state.Snapshot.Phase == "DONE") && !confirmed {
		return ErrConfirmationRequired
	}
	if state.Fresh && state.Snapshot.Phase == "DONE" {
		return t.run(ctx, "kill-session", "-t", "="+i.Session)
	}
	if err := t.run(ctx, "send-keys", "-t", "="+i.Session, "C-q"); err != nil {
		return err
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return t.run(ctx, "kill-session", "-t", "="+i.Session)
		case <-tick.C:
			alive, err := t.Alive(i.Session)
			if err != nil {
				return err
			}
			if !alive {
				return nil
			}
		}
	}
}

// Switch activates an existing instance for the current tmux client.
func (t Tmux) Switch(ctx context.Context, session string) error {
	return t.run(ctx, "switch-client", "-t", "="+session)
}
