package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/atfa/duo/internal/protocol"
	"github.com/creack/pty"
)

const recentLimit = 1 << 20

type ProcessState int

const (
	ProcessStarting ProcessState = iota
	ProcessRunning
	ProcessStopping
	ProcessExited
	ProcessFailed
)

type Config struct {
	Agent                                    protocol.AgentID
	Dir, Host, Port, Session, Token, Command string
}

type Session struct {
	cfg      Config
	mu       sync.RWMutex
	cmd      *exec.Cmd
	ptmx     *os.File
	recent   []byte
	attached io.Writer
	state    ProcessState
	started  bool
	stopping bool
	stopped  chan struct{}
	waitErr  error
	size     pty.Winsize
}

func NewSession(cfg Config) *Session {
	if strings.TrimSpace(cfg.Command) == "" {
		cfg.Command = "pi"
	}
	return &Session{cfg: cfg, size: pty.Winsize{Cols: 80, Rows: 24}}
}

func (s *Session) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started && (s.state == ProcessStarting || s.state == ProcessRunning || s.state == ProcessStopping) {
		return fmt.Errorf("%s is already running", s.cfg.Agent)
	}
	if ctx.Err() != nil {
		return fmt.Errorf("start %s: %w", s.cfg.Agent, ctx.Err())
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return fmt.Errorf("%s: unsupported platform %s", s.cfg.Agent, runtime.GOOS)
	}
	s.state = ProcessStarting
	// A shell preserves the existing DUO_PI_COMMAND behavior (including arguments).
	cmd := exec.Command("sh", "-lc", "exec "+s.cfg.Command)
	cmd.Dir = s.cfg.Dir
	cmd.Env = append(os.Environ(), "DUO_ACTIVE=1", "DUO_AGENT="+string(s.cfg.Agent), "DUO_HOST="+s.cfg.Host, "DUO_PORT="+s.cfg.Port, "DUO_SESSION="+s.cfg.Session, "DUO_TOKEN="+s.cfg.Token, "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &s.size)
	if err != nil {
		s.state = ProcessFailed
		s.waitErr = err
		return fmt.Errorf("start %s PTY: %w", s.cfg.Agent, err)
	}
	s.cmd = cmd
	s.ptmx = ptmx
	s.started = true
	s.stopping = false
	s.state = ProcessRunning
	s.waitErr = nil
	s.recent = nil
	s.stopped = make(chan struct{})
	done := s.stopped
	readerDone := make(chan struct{})
	go func() { s.readLoop(ptmx, done); close(readerDone) }()
	go func() {
		err := cmd.Wait()
		// The reader owns the PTY until EOF; close only after wait to unblock reads.
		select {
		case <-readerDone:
		case <-time.After(100 * time.Millisecond):
			_ = ptmx.Close()
			<-readerDone
		}
		_ = ptmx.Close()
		s.mu.Lock()
		s.waitErr = err
		switch {
		case s.stopping, err == nil:
			// A Duo-initiated stop (SIGTERM/SIGKILL) is a normal shutdown even
			// though Wait reports a signal error.
			s.state = ProcessExited
		default:
			s.state = ProcessFailed
		}
		s.stopping = false
		close(done)
		s.mu.Unlock()
	}()
	return nil
}

func (s *Session) readLoop(r io.Reader, done <-chan struct{}) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			s.mu.Lock()
			// A previous reader must never write into a restarted process's buffer.
			if s.stopped == done {
				s.recent = append(s.recent, chunk...)
				if len(s.recent) > recentLimit {
					s.recent = append([]byte(nil), s.recent[len(s.recent)-recentLimit:]...)
				}
				if s.attached != nil {
					_, _ = s.attached.Write(chunk)
				}
			}
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (s *Session) Write(data []byte) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state != ProcessRunning || s.ptmx == nil {
		return fmt.Errorf("%s session is not running", s.cfg.Agent)
	}
	_, err := s.ptmx.Write(data)
	return err
}
func (s *Session) Attach(w io.Writer) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attached = w
	return append([]byte(nil), s.recent...)
}
func (s *Session) Detach()             { s.mu.Lock(); s.attached = nil; s.mu.Unlock() }
func (s *Session) State() ProcessState { s.mu.RLock(); defer s.mu.RUnlock(); return s.state }
func (s *Session) Running() bool       { return s.State() == ProcessRunning }
func (s *Session) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("%s: invalid PTY size %dx%d", s.cfg.Agent, cols, rows)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.size = pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}
	if s.state == ProcessRunning {
		if err := pty.Setsize(s.ptmx, &s.size); err != nil {
			return fmt.Errorf("resize %s PTY: %w", s.cfg.Agent, err)
		}
	}
	return nil
}
func (s *Session) Stop() {
	s.mu.Lock()
	cmd := s.cmd
	done := s.stopped
	running := s.state == ProcessRunning
	if running {
		s.state = ProcessStopping
		s.stopping = true
	}
	s.mu.Unlock()
	if !running || cmd == nil {
		return
	}
	// The process group includes the shell's descendants; do not rely on CommandContext,
	// which only kills the immediate child.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
}
func (s *Session) WaitError() error { s.mu.RLock(); defer s.mu.RUnlock(); return s.waitErr }
