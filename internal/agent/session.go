package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/atfa/duo/internal/protocol"
	"github.com/creack/pty"
)

const recentLimit = 1 << 20

// modeEnv normalizes the session mode for the Pi bridge. Duo Core always sets
// it, and only fast|goal is ever exported: the extension treats a missing value
// as legacy Goal, but an empty or unknown value would be ambiguous.
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

type Config struct {
	Agent                                    protocol.AgentID
	DriverType                               string // "pi" or "agy"
	Mode                                     string
	Dir, Host, Port, Session, Token, Command string
	RepositoryRoot, ScopePath                string
	// PiSessionID is Duo's stable identity for this agent's Pi conversation. It
	// is persisted per session and reused verbatim across restarts and resumes.
	PiSessionID string
	// AgyConversationID is Duo's stable identity for this agent's Agy conversation.
	AgyConversationID string
	// ActivitySink receives observed activity events (used by agy driver).
	ActivitySink AgyActivitySink
	// LogFile is the path to the CLI log file where conversation IDs are logged.
	LogFile string
	// Model is the model name/ID to run with.
	Model string
	// OnExit observes process termination for durable logging.
	OnExit func(ExitEvent)
}

// ExitEvent is delivered once when an agent process ends.
type ExitEvent struct {
	Agent protocol.AgentID
	State ProcessState
	Err   error
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
	watcher  *AgyWatcher
	model    string
	effort   string
}

func NewSession(cfg Config) *Session {
	if strings.TrimSpace(cfg.DriverType) == "" {
		cfg.DriverType = "pi"
	}
	if strings.TrimSpace(cfg.Command) == "" {
		if cfg.DriverType == "agy" {
			cfg.Command = "agy"
		} else {
			cfg.Command = "pi"
		}
	}
	return &Session{cfg: cfg, size: pty.Winsize{Cols: 80, Rows: 24}, model: strings.TrimSpace(cfg.Model)}
}

func NewPiSession(cfg Config) *Session {
	cfg.DriverType = "pi"
	return NewSession(cfg)
}

func NewAgySession(cfg Config) *Session {
	cfg.DriverType = "agy"
	if strings.TrimSpace(cfg.Command) == "" {
		cfg.Command = "agy"
	}
	if strings.TrimSpace(cfg.LogFile) == "" {
		cfg.LogFile = filepath.Join(os.TempDir(), fmt.Sprintf("duo-agy-%s-%d.log", cfg.Agent, time.Now().UnixNano()))
	}
	return NewSession(cfg)
}

func (s *Session) Agent() protocol.AgentID { return s.cfg.Agent }
func (s *Session) DriverType() string {
	if s.cfg.DriverType != "" {
		return s.cfg.DriverType
	}
	return "pi"
}
func (s *Session) SessionID() string {
	if s.DriverType() == "agy" {
		return s.AgyConversationID()
	}
	return s.cfg.PiSessionID
}
func (s *Session) Command() string { return s.cfg.Command }
func (s *Session) SetOnExit(fn func(ExitEvent)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.OnExit = fn
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

// AgyConversationID returns the stable Agy conversation identity for this agent.
func (s *Session) AgyConversationID() string {
	if s.cfg.AgyConversationID != "" {
		return s.cfg.AgyConversationID
	}
	return s.cfg.Session + "-" + string(s.cfg.Agent)
}

// sessionFlags are the Pi CLI flags that select or control a session. When
// DUO_PI_COMMAND already contains one, Duo never injects its own --session-id,
// so an explicit operator choice is preserved rather than overridden.
func hasSessionFlag(command string) bool {
	for _, token := range strings.Fields(command) {
		switch token {
		case "--session-id", "--session", "--continue", "--resume", "--fork", "--no-session", "-c", "-r":
			return true
		}
		if strings.HasPrefix(token, "--session-id=") || strings.HasPrefix(token, "--session=") {
			return true
		}
	}
	return false
}

func (s *Session) isAgy() bool {
	drv := s.DriverType()
	return drv == "agy" || strings.Contains(drv, "agy") || strings.HasPrefix(s.cfg.Command, "agy") || strings.Contains(s.cfg.Command, "duo-agy")
}

// commandLine is the shell command used to launch the agent.
func (s *Session) commandLine() string {
	if s.isAgy() {
		return s.agyCommandLine()
	}
	return s.piCommandLine()
}

func (s *Session) piCommandLine() string {
	base := strings.TrimSpace(s.cfg.Command)
	if base == "" {
		base = "pi"
	}
	if strings.TrimSpace(s.cfg.PiSessionID) == "" || hasSessionFlag(base) {
		return base
	}
	return base + ` --session-id "$DUO_PI_SESSION_ID"`
}

func setAgyModel(base string, model string) string {
	if idx := strings.Index(model, "/"); idx != -1 {
		model = model[idx+1:]
	}
	model = strings.TrimSpace(model)
	tokens := strings.Fields(base)
	hasModel := false
	for i := 0; i < len(tokens); i++ {
		if tokens[i] == "--model" {
			if i+1 < len(tokens) {
				if model != "" {
					tokens[i+1] = fmt.Sprintf("%q", model)
				} else {
					val := strings.Trim(tokens[i+1], `"'`)
					if idx := strings.Index(val, "/"); idx != -1 {
						val = val[idx+1:]
					}
					tokens[i+1] = fmt.Sprintf("%q", val)
				}
				hasModel = true
				break
			}
		} else if strings.HasPrefix(tokens[i], "--model=") {
			if model != "" {
				tokens[i] = fmt.Sprintf("--model=%q", model)
			} else {
				val := strings.TrimPrefix(tokens[i], "--model=")
				val = strings.Trim(val, `"'`)
				if idx := strings.Index(val, "/"); idx != -1 {
					val = val[idx+1:]
				}
				tokens[i] = fmt.Sprintf("--model=%q", val)
			}
			hasModel = true
			break
		}
	}
	if !hasModel && model != "" {
		tokens = append(tokens, fmt.Sprintf("--model %q", model))
	}
	return strings.Join(tokens, " ")
}

func setAgyEffort(base string, effort string) string {
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return base
	}
	tokens := strings.Fields(base)
	hasEffort := false
	for i := 0; i < len(tokens); i++ {
		if tokens[i] == "--effort" {
			if i+1 < len(tokens) {
				tokens[i+1] = fmt.Sprintf("%q", effort)
				hasEffort = true
				break
			}
		} else if strings.HasPrefix(tokens[i], "--effort=") {
			tokens[i] = fmt.Sprintf("--effort=%q", effort)
			hasEffort = true
			break
		}
	}
	if !hasEffort {
		tokens = append(tokens, fmt.Sprintf("--effort %q", effort))
	}
	return strings.Join(tokens, " ")
}

func (s *Session) agyCommandLine() string {
	base := strings.TrimSpace(s.cfg.Command)
	if base == "" {
		base = "agy"
	}
	base = setAgyModel(base, s.model)
	if s.effort != "" {
		base = setAgyEffort(base, s.effort)
	}
	parts := []string{base}
	if !strings.Contains(base, "--conversation") && !strings.Contains(base, "-c") {
		if strings.TrimSpace(s.AgyConversationID()) != "" {
			parts = append(parts, `--conversation "$DUO_AGY_CONVERSATION_ID"`)
		}
	}
	if !strings.Contains(base, "--dangerously-skip-permissions") {
		parts = append(parts, "--dangerously-skip-permissions")
	}
	if s.cfg.LogFile != "" && !strings.Contains(base, "--log-file") {
		parts = append(parts, fmt.Sprintf("--log-file %q", s.cfg.LogFile))
	}
	return strings.Join(parts, " ")
}

// EffectiveCommand returns the command Duo will actually run, for diagnostics.
func (s *Session) EffectiveCommand() string { return s.commandLine() }

// PiSessionID returns the stable Pi session identity for this agent.
func (s *Session) PiSessionID() string { return s.cfg.PiSessionID }

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
	if s.isAgy() {
		if s.cfg.LogFile != "" {
			_ = os.Remove(s.cfg.LogFile)
		}
		_ = EnsureAgyWorkspaceTrusted(s.cfg.Dir, s.cfg.RepositoryRoot)
	}
	// A shell preserves the existing DUO_PI_COMMAND behavior (including arguments)
	// and expands "$DUO_PI_SESSION_ID" safely.
	cmd := exec.Command("sh", "-lc", "exec "+s.commandLine())
	cmd.Dir = s.cfg.Dir
	cmd.Env = append(os.Environ(),
		"DUO_ACTIVE=1",
		"DUO_DRIVER="+s.DriverType(),
		"DUO_AGENT="+string(s.cfg.Agent),
		"DUO_MODE="+modeEnv(s.cfg.Mode),
		"DUO_HOST="+s.cfg.Host,
		"DUO_PORT="+s.cfg.Port,
		"DUO_SESSION="+s.cfg.Session,
		"DUO_TOKEN="+s.cfg.Token,
		"DUO_PI_SESSION_ID="+s.cfg.PiSessionID,
		"DUO_AGY_CONVERSATION_ID="+s.AgyConversationID(),
		"DUO_AGY_LOG_FILE="+s.cfg.LogFile,
		"DUO_REPOSITORY_ROOT="+s.cfg.RepositoryRoot,
		"DUO_SCOPE_PATH="+s.cfg.ScopePath,
		"TERM=xterm-256color",
	)
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
			onExit(ExitEvent{Agent: s.cfg.Agent, State: final, Err: err})
		}
	}()
	if s.isAgy() && s.cfg.ActivitySink != nil {
		go s.startAgyWatcher(ctx)
	}
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
	if s.watcher != nil {
		s.watcher.Stop()
		s.watcher = nil
	}
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

func agyAppDataDir() string {
	if dir := os.Getenv("GEMINI_APP_DATA_DIR"); dir != "" {
		return dir
	}
	if dir := os.Getenv("ANTIGRAVITY_APP_DATA_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gemini", "antigravity-cli")
}

// EnsureAgyWorkspaceTrusted adds the given directory paths to agy's trustedWorkspaces
// in settings.json so the interactive workspace trust prompt is bypassed when
// running autonomous agent turns.
func EnsureAgyWorkspaceTrusted(paths ...string) error {
	dir := agyAppDataDir()
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	settingsPath := filepath.Join(dir, "settings.json")

	settings := make(map[string]any)
	if data, err := os.ReadFile(settingsPath); err == nil {
		_ = json.Unmarshal(data, &settings)
	}

	var trusted []string
	if raw, ok := settings["trustedWorkspaces"].([]any); ok {
		for _, item := range raw {
			if s, ok := item.(string); ok && s != "" {
				trusted = append(trusted, s)
			}
		}
	} else if raw, ok := settings["trustedWorkspaces"].([]string); ok {
		trusted = append(trusted, raw...)
	}

	seen := make(map[string]bool)
	for _, t := range trusted {
		seen[filepath.Clean(t)] = true
	}

	changed := false
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		clean := filepath.Clean(p)
		if !seen[clean] {
			trusted = append(trusted, clean)
			seen[clean] = true
			changed = true
		}
		if real, err := filepath.EvalSymlinks(clean); err == nil && real != clean {
			realClean := filepath.Clean(real)
			if !seen[realClean] {
				trusted = append(trusted, realClean)
				seen[realClean] = true
				changed = true
			}
		}
	}

	if !changed {
		return nil
	}

	settings["trustedWorkspaces"] = trusted
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmpFile := settingsPath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpFile, settingsPath)
}

func agyTranscriptPath(convID string) string {
	base := agyAppDataDir()
	if base == "" || convID == "" {
		return ""
	}
	return filepath.Join(base, "brain", convID, ".system_generated", "logs", "transcript.jsonl")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func (s *Session) startAgyWatcher(ctx context.Context) {
	convID := s.AgyConversationID()
	transcript := agyTranscriptPath(convID)

	// If transcript file doesn't exist yet on disk, poll log file for conversation ID
	if transcript == "" || !fileExists(transcript) {
		for {
			if s.cfg.LogFile != "" && fileExists(s.cfg.LogFile) {
				if f, err := os.Open(s.cfg.LogFile); err == nil {
					id, _ := ExtractConversationID(f)
					_ = f.Close()
					if id != "" {
						convID = id
						s.mu.Lock()
						s.cfg.AgyConversationID = id
						s.mu.Unlock()
						transcript = agyTranscriptPath(id)
						break
					}
				}
			}

			select {
			case <-ctx.Done():
				return
			case <-s.stopped:
				return
			case <-time.After(150 * time.Millisecond):
			}
		}
	}

	if transcript == "" {
		return
	}

	s.mu.Lock()
	if !s.started || s.stopping {
		s.mu.Unlock()
		return
	}
	watcher := NewAgyWatcher(s.cfg.Agent, transcript, s.cfg.ActivitySink)
	s.watcher = watcher
	s.mu.Unlock()

	watcher.Start(ctx)
}
