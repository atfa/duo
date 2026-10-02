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

	"github.com/atfa/duo/internal/models"
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
	DriverType                               string // "pi", "agy" or "opencode"
	Mode                                     string
	Dir, Host, Port, Session, Token, Command string
	RepositoryRoot, ScopePath                string
	// PiSessionID is Duo's stable identity for this agent's Pi conversation. It
	// is persisted per session and reused verbatim across restarts and resumes.
	PiSessionID string
	// AgyConversationID is Duo's stable identity for this agent's Agy conversation.
	AgyConversationID string
	// OpencodeSessionID is the opencode session this agent reattaches to. Unlike
	// pi and agy, opencode assigns session ids server-side, so this is empty on a
	// first run and learned from the plugin afterwards (see OpencodeSessionFile).
	OpencodeSessionID string
	// OpencodeSessionFile is a path the opencode plugin writes its session id to.
	// Duo reads it once the agent is running so the identity can be persisted.
	OpencodeSessionFile string
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
		switch cfg.DriverType {
		case "agy":
			cfg.Command = "agy"
		case "opencode":
			cfg.Command = "opencode"
		default:
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

func NewOpencodeSession(cfg Config) *Session {
	cfg.DriverType = "opencode"
	if strings.TrimSpace(cfg.Command) == "" {
		cfg.Command = "opencode"
	}
	// opencode rejects --session for an id it has never issued ("Session not
	// found"), and it owns the id format. Anything that is not an opencode
	// session id is ignored rather than passed through: a fresh Duo run has a
	// placeholder identity here, and opencode must be allowed to mint its own.
	if !strings.HasPrefix(strings.TrimSpace(cfg.OpencodeSessionID), "ses_") {
		cfg.OpencodeSessionID = ""
	}
	if strings.TrimSpace(cfg.OpencodeSessionFile) == "" {
		cfg.OpencodeSessionFile = filepath.Join(os.TempDir(), fmt.Sprintf("duo-opencode-%s-%s.session", cfg.Session, strings.ToLower(string(cfg.Agent))))
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

// normalizeDriverType resolves a driver name the same way everywhere, so an
// unset driver means pi rather than depending on the caller's default.
func normalizeDriverType(driverType string) string {
	driverType = strings.ToLower(strings.TrimSpace(driverType))
	if driverType == "" {
		return "pi"
	}
	return driverType
}
func (s *Session) SessionID() string {
	switch {
	case s.isAgy():
		return s.AgyConversationID()
	case s.isOpencode():
		return s.OpencodeSessionID()
	default:
		return s.cfg.PiSessionID
	}
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
// The agy watcher goroutine fills cfg.AgyConversationID in once agy reports it,
// so the read is taken under the same lock as the write.
func (s *Session) AgyConversationID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.agyConversationIDLocked()
}

// agyConversationIDLocked is AgyConversationID for callers already holding s.mu.
func (s *Session) agyConversationIDLocked() string {
	if s.cfg.AgyConversationID != "" {
		return s.cfg.AgyConversationID
	}
	return s.cfg.Session + "-" + string(s.cfg.Agent)
}

// OpencodeSessionID returns the opencode session identity for this agent, which
// opencode assigns itself. It is empty until the plugin has reported it.
func (s *Session) OpencodeSessionID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.opencodeSessionIDLocked()
}

// opencodeSessionIDLocked is OpencodeSessionID for callers already holding s.mu.
func (s *Session) opencodeSessionIDLocked() string {
	return strings.TrimSpace(s.cfg.OpencodeSessionID)
}

// Flags that suppress Duo's own flag injection, per driver. Detection matches
// whole tokens only: a substring test for "-c" also matches "--config", which
// silently cost an agy agent its conversation ID.
var (
	piSessionFlags       = []string{"--session-id", "--session", "--continue", "--resume", "--fork", "--no-session", "-c", "-r"}
	agySessionFlags      = []string{"--conversation", "-c"}
	opencodeSessionFlags = []string{"--session", "-s", "--continue", "-c", "--fork"}
)

// hasSessionFlag reports whether the operator's Pi command already selects a
// session, so an explicit choice is preserved rather than overridden.
func hasSessionFlag(command string) bool {
	return hasAnyFlag(command, piSessionFlags)
}

func hasAnyFlag(command string, names []string) bool {
	for _, token := range strings.Fields(command) {
		for _, name := range names {
			if token == name || strings.HasPrefix(token, name+"=") {
				return true
			}
		}
	}
	return false
}

// shellQuote quotes a value for /bin/sh. It is used instead of %q because %q
// produces Go escaping, not shell escaping: "a$b" would expand $b and
// "x`id`y" would run id. Single quotes suppress both.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// stripProviderPrefix removes a provider prefix from a model reference. A model
// ID may itself contain a slash, so the last segment is the model.
func stripProviderPrefix(model string) string {
	model = strings.TrimSpace(model)
	if idx := strings.LastIndex(model, "/"); idx != -1 {
		return model[idx+1:]
	}
	return model
}

// readFlagValue returns the value of flag in a command string, accepting both
// the "--flag value" and "--flag=value" spellings.
func readFlagValue(command, flag string) (string, bool) {
	tokens := strings.Fields(command)
	for i, token := range tokens {
		if token == flag && i+1 < len(tokens) {
			return strings.Trim(tokens[i+1], `"'`), true
		}
		if strings.HasPrefix(token, flag+"=") {
			return strings.Trim(strings.TrimPrefix(token, flag+"="), `"'`), true
		}
	}
	return "", false
}

// setFlagInCommand sets flag=value in a shell command string, replacing any
// existing occurrence and appending when absent. It returns the command with
// the flag applied. Values are shell-quoted.
func setFlagInCommand(command, flag, value string) string {
	tokens := strings.Fields(command)
	prefix := flag + "="
	for i := 0; i < len(tokens); i++ {
		if tokens[i] == flag && i+1 < len(tokens) {
			tokens[i+1] = shellQuote(value)
			return strings.Join(tokens, " ")
		}
		if strings.HasPrefix(tokens[i], prefix) {
			tokens[i] = prefix + shellQuote(value)
			return strings.Join(tokens, " ")
		}
	}
	if value == "" {
		return command
	}
	return command + " " + flag + " " + shellQuote(value)
}

// The command is classified by its executable alone, through models.DriverKind,
// which is the single implementation of that rule and also recognises the
// duo-agy and duo-opencode wrappers by name.
//
// Matching anywhere in the string let a flag *value* decide the driver:
// `pi --model opencode/space-bunny-free` was built as an opencode command and
// picked up opencode's --auto, which pi rejects, so the agent died at startup
// with nothing but "exit status 1" in the session log.
func (s *Session) isAgy() bool {
	drv := s.DriverType()
	return drv == "agy" || strings.Contains(drv, "agy") || models.DriverKind(s.cfg.Command) == "agy"
}

func (s *Session) isOpencode() bool {
	drv := s.DriverType()
	return drv == "opencode" || strings.Contains(drv, "opencode") || models.DriverKind(s.cfg.Command) == "opencode"
}

// commandLine renders the launch command. The model and the agy conversation id
// are written by other goroutines (SetModel from the TUI, the agy watcher), so
// the read is taken under the same lock as the write.
func (s *Session) commandLine() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.commandLineLocked()
}

// commandLineLocked is commandLine for callers already holding s.mu. Start holds
// the write lock for its whole body, so it must not re-acquire it.
func (s *Session) commandLineLocked() string {
	switch {
	case s.isOpencode():
		return s.opencodeCommandLineLocked()
	case s.isAgy():
		return s.agyCommandLineLocked()
	default:
		return s.piCommandLineLocked()
	}
}

func (s *Session) piCommandLineLocked() string {
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
	if model = stripProviderPrefix(model); model != "" {
		return setFlagInCommand(base, "--model", model)
	}
	// No model was selected, but the operator's own command may still carry a
	// provider-prefixed --model. Normalize it in place so agy sees the bare name.
	if existing, ok := readFlagValue(base, "--model"); ok {
		return setFlagInCommand(base, "--model", stripProviderPrefix(existing))
	}
	return base
}

func setAgyEffort(base string, effort string) string {
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return base
	}
	return setFlagInCommand(base, "--effort", effort)
}

func (s *Session) agyCommandLineLocked() string {
	base := strings.TrimSpace(s.cfg.Command)
	if base == "" {
		base = "agy"
	}
	base = setAgyModel(base, s.model)
	if s.effort != "" {
		base = setAgyEffort(base, s.effort)
	}
	parts := []string{base}
	// Token-exact match: a substring test for "-c" also matches "--config",
	// which would silently drop the conversation ID and lose this agent's
	// identity across restarts.
	if !hasAnyFlag(base, agySessionFlags) {
		if strings.TrimSpace(s.agyConversationIDLocked()) != "" {
			parts = append(parts, `--conversation "$DUO_AGY_CONVERSATION_ID"`)
		}
	}
	if !strings.Contains(base, "--dangerously-skip-permissions") {
		parts = append(parts, "--dangerously-skip-permissions")
	}
	if s.cfg.LogFile != "" && !strings.Contains(base, "--log-file") {
		parts = append(parts, "--log-file "+shellQuote(s.cfg.LogFile))
	}
	return strings.Join(parts, " ")
}

// EffectiveCommand returns the command Duo will actually run, for diagnostics.
func (s *Session) EffectiveCommand() string { return s.commandLine() }

// opencodeCommandLine builds the opencode launch command. opencode's session id
// is assigned by the server, so --session is only injected once Duo knows it
// (a resumed session); on a first run the plugin reports the id back and Duo
// persists it for next time.
func (s *Session) opencodeCommandLineLocked() string {
	base := strings.TrimSpace(s.cfg.Command)
	if base == "" {
		base = "opencode"
	}
	if m := strings.TrimSpace(s.model); m != "" {
		base = setFlagInCommand(base, "--model", m)
	}
	parts := []string{base}
	if sid := strings.TrimSpace(s.cfg.OpencodeSessionID); sid != "" && !hasAnyFlag(base, opencodeSessionFlags) {
		parts = append(parts, "--session "+shellQuote(sid))
	}
	// Agents run unattended in their own worktree, so tool calls must not block
	// on an interactive permission prompt.
	if !strings.Contains(base, "--auto") {
		parts = append(parts, "--auto")
	}
	return strings.Join(parts, " ")
}

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
	if s.isOpencode() {
		// The plugin rewrites this file with the id opencode assigned, so a stale
		// value from a previous run must never be picked up as this run's id.
		if s.cfg.OpencodeSessionFile != "" {
			_ = os.Remove(s.cfg.OpencodeSessionFile)
		}
	}
	// A shell preserves the existing DUO_PI_COMMAND behavior (including arguments)
	// and expands "$DUO_PI_SESSION_ID" safely.
	cmd := exec.Command("sh", "-lc", "exec "+s.commandLineLocked())
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
		"DUO_AGY_CONVERSATION_ID="+s.agyConversationIDLocked(),
		"DUO_AGY_LOG_FILE="+s.cfg.LogFile,
		"DUO_OPENCODE_SESSION_ID="+s.opencodeSessionIDLocked(),
		"DUO_OPENCODE_SESSION_FILE="+s.cfg.OpencodeSessionFile,
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
		go s.startAgyWatcher(ctx, done)
	}
	if s.isOpencode() {
		go s.watchOpencodeSession(ctx)
	}
	return nil
}

// watchOpencodeSession waits for the plugin to report the session id opencode
// assigned. The id is the only thing that survives a restart, so it is read as
// soon as it appears and kept on the session for the snapshot to persist.
func (s *Session) watchOpencodeSession(ctx context.Context) {
	path := strings.TrimSpace(s.cfg.OpencodeSessionFile)
	if path == "" {
		return
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			id := strings.TrimSpace(string(data))
			if id == "" || !strings.HasPrefix(id, "ses_") {
				continue
			}
			s.mu.Lock()
			if s.cfg.OpencodeSessionID == id {
				s.mu.Unlock()
				return
			}
			s.cfg.OpencodeSessionID = id
			s.mu.Unlock()
			return
		}
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

// startAgyWatcher follows the agy transcript for activity. done is the stopped
// channel of the process run it belongs to: the field is reassigned by a
// restart, so a watcher that read it directly could latch onto the next run's
// channel and never notice its own process was gone.
func (s *Session) startAgyWatcher(ctx context.Context, done <-chan struct{}) {
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
			case <-done:
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
