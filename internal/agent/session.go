package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"

	"github.com/atfa/duo/internal/protocol"
)

const recentLimit = 1 << 20

type Config struct {
	Agent   protocol.AgentID
	Dir     string
	Host    string
	Port    string
	Command string
}

type Session struct {
	cfg Config

	mu       sync.RWMutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	recent   []byte
	attached io.Writer
	started  bool
	stopped  chan struct{}
	waitErr  error
}

func NewSession(cfg Config) *Session {
	if strings.TrimSpace(cfg.Command) == "" {
		cfg.Command = "pi"
	}
	return &Session{cfg: cfg, stopped: make(chan struct{})}
}

func (s *Session) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil
	}

	cmd, err := scriptCommand(ctx, s.cfg.Command)
	if err != nil {
		return err
	}
	cmd.Dir = s.cfg.Dir
	cmd.Env = append(os.Environ(),
		"DUO_AGENT="+string(s.cfg.Agent),
		"DUO_HOST="+s.cfg.Host,
		"DUO_PORT="+s.cfg.Port,
		"TERM=xterm-256color",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return err
	}
	s.cmd = cmd
	s.stdin = stdin
	s.started = true

	go s.readLoop(stdout)
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		s.waitErr = err
		select {
		case <-s.stopped:
		default:
			close(s.stopped)
		}
		s.mu.Unlock()
	}()
	return nil
}

func scriptCommand(ctx context.Context, command string) (*exec.Cmd, error) {
	if _, err := exec.LookPath("script"); err != nil {
		return nil, fmt.Errorf("required command 'script' was not found in PATH: %w", err)
	}
	switch runtime.GOOS {
	case "darwin":
		return exec.CommandContext(ctx, "script", "-q", "-t", "0", "/dev/null", "sh", "-lc", "exec "+command), nil
	case "linux":
		return exec.CommandContext(ctx, "script", "-q", "-f", "-c", "exec "+command, "/dev/null"), nil
	default:
		return nil, fmt.Errorf("Duo v0.3.0-alpha.1 PTY supervisor currently supports macOS and Linux, not %s", runtime.GOOS)
	}
}

func (s *Session) readLoop(r io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			s.mu.Lock()
			s.recent = append(s.recent, chunk...)
			if len(s.recent) > recentLimit {
				s.recent = append([]byte(nil), s.recent[len(s.recent)-recentLimit:]...)
			}
			w := s.attached
			s.mu.Unlock()
			if w != nil {
				_, _ = w.Write(chunk)
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *Session) Write(data []byte) error {
	s.mu.RLock()
	stdin := s.stdin
	s.mu.RUnlock()
	if stdin == nil {
		return fmt.Errorf("%s session is not running", s.cfg.Agent)
	}
	_, err := stdin.Write(data)
	return err
}

func (s *Session) Attach(w io.Writer) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attached = w
	return append([]byte(nil), s.recent...)
}

func (s *Session) Detach() {
	s.mu.Lock()
	s.attached = nil
	s.mu.Unlock()
}

func (s *Session) Running() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.started && s.cmd != nil && s.cmd.Process != nil && s.waitErr == nil
}

func (s *Session) Stop() {
	s.mu.RLock()
	cmd := s.cmd
	s.mu.RUnlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	} else {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
}

func (s *Session) WaitError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.waitErr
}

func stripNUL(in []byte) []byte { return bytes.ReplaceAll(in, []byte{0}, nil) }

var _ = stripNUL
