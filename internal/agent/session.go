package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/protocol"
	"github.com/creack/pty"
)

const recentLimit = 1 << 20

// modeEnv normalizes the session mode for an Agent Adapter. Duo Core always sets
// it, and only fast|goal is ever exported: an adapter treats a missing value as
// legacy Goal, but an empty or unknown value would be ambiguous.
func modeEnv(mode string) string {
	if strings.TrimSpace(mode) == "fast" {
		return "fast"
	}
	return "goal"
}

type ProcessState int

const (
	ProcessStarting ProcessState = iota
	ProcessRunning
	ProcessStopping
	ProcessExited
	ProcessFailed
)

func (s ProcessState) String() string {
	switch s {
	case ProcessStarting:
		return "starting"
	case ProcessRunning:
		return "running"
	case ProcessStopping:
		return "stopping"
	case ProcessExited:
		return "exited"
	case ProcessFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// Config is everything one agent session needs from Duo Core. Every field is Core
// state: which agent, which mode, where to run, how to reach Duo's bridge, and the
// driver's own opaque blob from the previous run.
//
// There is deliberately no field for a specific agent's session identity, log file
// or activity sink. Those belong to a Driver Plugin, which answers prepare with
// whatever it needs; a new coding agent therefore requires no change here.
type Config struct {
	Agent          protocol.AgentID
	Mode           string
	Dir            string
	RepositoryRoot string
	ScopePath      string
	Host, Port     string
	Session        string
	Token          string

	// Plugin is the driver that owns this agent. Everything agent-specific is
	// asked of it.
	Plugin driver.Caller
	// PluginState is the opaque blob Plugin returned from the previous launch.
	PluginState json.RawMessage
	// BaseCommand is the operator's own command for this agent, if any.
	BaseCommand string
	// Model is the model to run with, empty when none was selected.
	Model string
	// OnExit observes process termination for durable logging.
	OnExit func(ExitEvent)
	// Notices receives the diagnostics a plugin reported while preparing a launch.
	// They are the operator's, verbatim: Duo Core hard-codes no per-driver warning.
	Notices func([]string)
}

// ExitEvent is delivered once when an agent process ends.
type ExitEvent struct {
	Agent protocol.AgentID
	State ProcessState
	Err   error
	// Output is the tail of what the process wrote before it ended, present only
	// when it failed. A driver launched into a PTY merges its stderr into that
	// stream, so this is where the driver's own diagnosis lands: a rejected flag or
	// an unknown model is printed there and nowhere else. Without it the operator
	// sees only "exit status 1" and has to guess.
	Output string
}

type Session struct {
	cfg Config

	manifest *driver.Manifest

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

	// model and effort are set by the interface, which the model picker drives
	// while the process runs, and read when a launch is prepared. The read is
	// taken under the same lock as the write.
	model  string
	effort string

	// plan is the launch specification the plugin returned, and identity the
	// label it reported. Both come from the plugin, so Core never interprets them.
	plan     *driver.LaunchPlan
	identity string
}

// NewSession builds a session for one agent from its plugin. The plugin's manifest
// is fetched immediately: an agent that cannot describe itself must fail here,
// before any process is started, with the driver's own reason.
func NewSession(ctx context.Context, cfg Config) (*Session, error) {
	if cfg.Plugin == nil {
		return nil, fmt.Errorf("%s: no driver plugin", cfg.Agent)
	}
	man, err := cfg.Plugin.Describe(ctx)
	if err != nil {
		return nil, err
	}
	return &Session{
		cfg:      cfg,
		manifest: man,
		size:     pty.Winsize{Cols: 80, Rows: 24},
		model:    strings.TrimSpace(cfg.Model),
	}, nil
}

func (s *Session) Agent() protocol.AgentID { return s.cfg.Agent }

// DriverType is the plugin's name. It is used for display and for `duo plugins`;
// Core takes no behaviour from it.
func (s *Session) DriverType() string { return s.cfg.Plugin.Name() }

// Command is the operator's own launch command for this agent, which is what the
// model picker needs to read the catalog from the same installation the agents run.
func (s *Session) Command() string { return s.cfg.BaseCommand }

// Manifest is the driver's self-description. It is the only source Core has for
// how this agent behaves.
func (s *Session) Manifest() *driver.Manifest { return s.manifest }

// Capabilities is the driver's declared behaviour, read by every delivery decision.
func (s *Session) Capabilities() driver.Capabilities {
	if s.manifest == nil {
		return driver.Capabilities{}
	}
	return s.manifest.Capabilities
}

// DriverState is the plugin's opaque resume blob for this agent. Core stores it and
// hands it back on the next launch without reading it. It is named for its owner
// because State() is already the process state, which is Core's and unrelated.
//
// The value is read live rather than taken from the launch plan, because a driver
// with resume: server learns its identity after prepare has already run. Only such a
// driver is asked: a client driver mints its identity up front and has already put
// it in the plan.
//
// A blank answer falls back to the blob from the last successful prepare. "Blank"
// means null, an empty string, an empty object or an empty array — the shapes a
// plugin produces when it has nothing to say, and the shapes a plugin process
// produces when it has just been respawned and has not run prepare since. Core does
// not try to interpret a blob any further than that: deciding whether some other
// object is meaningful would mean reading a plugin's private schema, which is the
// one thing this protocol exists to prevent. A plugin that has nothing new must
// omit the field rather than send a placeholder.
func (s *Session) DriverState() json.RawMessage {
	s.mu.RLock()
	cached, launched := s.plan, s.plan != nil
	asks := s.manifest != nil && s.manifest.Capabilities.Resume == driver.ResumeServer
	s.mu.RUnlock()

	if launched && asks && s.cfg.Plugin != nil {
		// The lock is deliberately not held: this crosses a process boundary and
		// would otherwise block every reader for the length of a plugin call.
		if state, err := s.cfg.Plugin.State(context.Background()); err == nil && !blankState(state) {
			return state
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if !launched || cached == nil || len(cached.State) == 0 {
		return s.cfg.PluginState
	}
	return cached.State
}

// blankState reports a resume blob that carries nothing worth saving. The four
// shapes are the only ones judged: null is what a plugin process answers with when
// it has no state at all, which after a crash and a lazy respawn is every save
// until the replacement process has run a prepare of its own.
func blankState(raw json.RawMessage) bool {
	switch s := strings.TrimSpace(string(raw)); s {
	case "", "null", `""`, "{}", "[]":
		return true
	default:
		return false
	}
}

// SessionID is the label the plugin reported for this agent's conversation. It goes
// into logs and diagnostics; Core never parses it.
func (s *Session) SessionID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.identity != "" {
		return s.identity
	}
	// Before the first launch there is nothing to report, which is honest: the
	// identity is whatever the plugin decides when it prepares the run.
	return ""
}

func (s *Session) Restart(ctx context.Context) error {
	if s.Running() {
		return fmt.Errorf("%s is still running; restart refused", s.cfg.Agent)
	}
	return s.Start(ctx)
}

func (s *Session) SetModel(model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.model = strings.TrimSpace(model)
}

func (s *Session) Model() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.model
}

func (s *Session) SetEffort(effort string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.effort = strings.TrimSpace(effort)
}

func (s *Session) Effort() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.effort
}

func (s *Session) RestartRunning(ctx context.Context) error {
	if s.Running() {
		s.Stop()
		return s.Start(ctx)
	}
	return nil
}

// EffectiveCommand returns the command Duo will actually run, for diagnostics.
func (s *Session) EffectiveCommand() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.plan != nil {
		return s.plan.Command
	}
	return s.cfg.BaseCommand
}

func (s *Session) SetOnExit(fn func(ExitEvent)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.OnExit = fn
}

// prepare asks the plugin for the launch specification, under the lock that Start
// already holds.
func (s *Session) prepareLocked(ctx context.Context) (*driver.LaunchPlan, error) {
	plan, err := s.cfg.Plugin.Prepare(ctx, driver.LaunchRequest{
		Agent:          driver.AgentID(s.cfg.Agent),
		Mode:           modeEnv(s.cfg.Mode),
		CWD:            s.cfg.Dir,
		RepositoryRoot: s.cfg.RepositoryRoot,
		ScopePath:      s.cfg.ScopePath,
		Session:        s.cfg.Session,
		Token:          s.cfg.Token,
		Host:           s.cfg.Host,
		Port:           s.cfg.Port,
		BaseCommand:    s.cfg.BaseCommand,
		Model:          s.model,
		Thinking:       s.effort,
		State:          s.cfg.PluginState,
	})
	if err != nil {
		return nil, fmt.Errorf("prepare %s: %w", s.cfg.Agent, err)
	}
	return plan, nil
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

	plan, err := s.prepareLocked(ctx)
	if err != nil {
		s.state = ProcessFailed
		s.waitErr = err
		return err
	}
	s.plan = plan
	s.identity = plan.SessionIdentity

	// The plugin names the paths it will overwrite this run, so a value left by an
	// earlier one can never be read as this run's.
	for _, path := range plan.Cleanup {
		if strings.TrimSpace(path) != "" {
			_ = os.Remove(path)
		}
	}
	if s.cfg.Notices != nil && len(plan.Notices) > 0 {
		s.cfg.Notices(plan.Notices)
	}

	s.state = ProcessStarting

	// A shell preserves the existing DUO_PI_COMMAND behaviour (including arguments)
	// and expands any variable the plugin's own environment supplies.
	cmd := exec.Command("sh", "-lc", "exec "+plan.Command)
	cmd.Dir = s.cfg.Dir
	cmd.Env = append(os.Environ(), coreEnv(s)...)
	for name, value := range plan.Env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	cmd.Env = append(cmd.Env, "TERM=xterm-256color")

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
		final := s.state
		s.stopping = false
		close(done)
		onExit := s.cfg.OnExit
		s.mu.Unlock()
		if onExit != nil {
			event := ExitEvent{Agent: s.cfg.Agent, State: final, Err: err}
			if final == ProcessFailed {
				event.Output = s.failureOutput()
			}
			onExit(event)
		}
	}()
	return nil
}

// coreEnv is the environment Duo Core defines for every agent and for every driver
// plugin. It is the whole contract: a variable an Agent Adapter needs to reach Duo
// Core is here, and a variable only one agent understands comes from that agent's
// plugin.
func coreEnv(s *Session) []string {
	return []string{
		"DUO_ACTIVE=1",
		"DUO_DRIVER=" + s.cfg.Plugin.Name(),
		"DUO_AGENT=" + string(s.cfg.Agent),
		"DUO_MODE=" + modeEnv(s.cfg.Mode),
		"DUO_HOST=" + s.cfg.Host,
		"DUO_PORT=" + s.cfg.Port,
		"DUO_SESSION=" + s.cfg.Session,
		"DUO_TOKEN=" + s.cfg.Token,
		"DUO_REPOSITORY_ROOT=" + s.cfg.RepositoryRoot,
		"DUO_SCOPE_PATH=" + s.cfg.ScopePath,
	}
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

// failureOutput returns the tail of what the process wrote before it failed, or
// an empty string when there was nothing to report. It is read under the lock
// because the PTY reader appends to the same buffer.
func (s *Session) failureOutput() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return failureTail(s.recent)
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
	// The process group includes the shell's descendants; do not rely on
	// CommandContext, which only kills the immediate child.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
}
func (s *Session) WaitError() error { s.mu.RLock(); defer s.mu.RUnlock(); return s.waitErr }

// Close releases the driver plugin behind this session.
func (s *Session) Close() {
	if s.cfg.Plugin != nil {
		s.cfg.Plugin.Close()
	}
}
