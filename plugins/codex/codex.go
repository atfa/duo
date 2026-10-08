// Package codex implements the Codex CLI driver for Duo.
package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/atfa/duo/internal/driver"
)

const (
	Name          = "codex"
	DriverVersion = "1.0.0"
)

var (
	lookPath       = exec.LookPath
	lookPathDuo    = exec.LookPath
	duoCommand     = findDuoCommand
	watchInterval  = 250 * time.Millisecond
	probeTimeout   = 5 * time.Second
	settleInterval = time.Second
)

type state struct {
	SessionID string    `json:"sessionId,omitempty"`
	CWD       string    `json:"cwd,omitempty"`
	StartedAt time.Time `json:"startedAt,omitempty"`
}

type Plugin struct {
	mu      sync.Mutex
	current state
	stop    context.CancelFunc
	learned bool
}

func New() driver.Handler { return &Plugin{} }

func Register() { driver.Register(Name, func() driver.Handler { return New() }) }

func (p *Plugin) Describe() (*driver.Manifest, error) {
	return &driver.Manifest{
		Protocol:       driver.ProtocolVersion,
		Name:           Name,
		Version:        DriverVersion,
		Description:    "OpenAI Codex CLI coding agent",
		Agent:          driver.AgentInfo{CLI: Name, DefaultCommand: Name},
		ModelReference: driver.ModelBare,
		Capabilities: driver.Capabilities{
			Resume:      driver.ResumeServer,
			Thinking:    true,
			Bridge:      driver.BridgeNone,
			PTYFallback: true,
		},
		MCP: true,
	}, nil
}

func (p *Plugin) Probe() (*driver.ProbeResult, error) {
	path, reason := checkCodexCLI()
	if reason != "" {
		return &driver.ProbeResult{Available: false, Reason: reason}, nil
	}
	if _, err := duoCommand(); err != nil {
		return &driver.ProbeResult{Available: false, Reason: "Codex MCP tools require a runnable duo CLI: " + err.Error()}, nil
	}
	return &driver.ProbeResult{Available: true, AgentPath: path}, nil
}

func (p *Plugin) Prepare(req driver.LaunchRequest) (*driver.LaunchPlan, error) {
	var prior state
	if len(req.State) > 0 {
		if err := json.Unmarshal(req.State, &prior); err != nil {
			return nil, &driver.RPCError{Code: driver.CodeInvalid, Message: "codex resume state is not readable: " + err.Error()}
		}
	}
	mcpCommand, err := duoCommand()
	if err != nil {
		return nil, &driver.RPCError{Code: driver.CodeUnavailable, Message: "Codex MCP tools require a runnable duo CLI: " + err.Error()}
	}

	base := strings.TrimSpace(req.BaseCommand)
	if base == "" || base == Name {
		if _, reason := checkCodexCLI(); reason != "" {
			return nil, &driver.RPCError{Code: driver.CodeUnavailable, Message: reason}
		}
		if base == "" {
			base = Name
		}
	}
	if driver.HasFlag(base, "--sandbox", "-s", "--ask-for-approval", "-a", "--full-auto") {
		return nil, &driver.RPCError{
			Code:    driver.CodeInvalid,
			Message: "Codex Duo mode bypasses sandbox and approvals; remove sandbox or approval flags from the base command",
		}
	}
	command := base
	if prior.SessionID != "" && !hasSubcommand(command, "resume") {
		command += " resume " + driver.ShellQuote(prior.SessionID)
	}
	command = driver.AppendFlag(command, "--model", driver.BareModelID(req.Model))
	if effort := strings.TrimSpace(req.Thinking); effort != "" {
		command += " -c " + driver.ShellQuote("model_reasoning_effort="+effort)
	}
	// YOLO skips approvals and sandboxing, but Codex still needs project trust.
	if trust := projectTrustOverride(req); trust != "projects={}" {
		command += " -c " + driver.ShellQuote(trust)
	}
	// The bridge token is passed through the environment and allowlisted only for
	// the stdio MCP child, never embedded in a command argument.
	for _, config := range []string{
		"mcp_servers.duo.command=" + strconv.Quote(mcpCommand),
		"mcp_servers.duo.args=[\"mcp-server\"]",
		"mcp_servers.duo.env_vars=[\"DUO_AGENT\",\"DUO_SESSION\",\"DUO_TOKEN\",\"DUO_HOST\",\"DUO_PORT\"]",
	} {
		command += " -c " + driver.ShellQuote(config)
	}
	if !driver.HasFlag(command, "--yolo", "--dangerously-bypass-approvals-and-sandbox") {
		command += " --dangerously-bypass-approvals-and-sandbox"
	}

	next := prior
	if next.SessionID == "" {
		next.CWD = req.CWD
		if absolute, err := filepath.Abs(next.CWD); err == nil {
			next.CWD = absolute
		}
		next.StartedAt = time.Now()
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.current = next
	p.mu.Unlock()
	if prior.SessionID == "" && next.CWD != "" {
		p.watch(next)
	}
	return &driver.LaunchPlan{
		Command: command,
		Env: map[string]string{
			"DUO_AGENT":   strings.ToLower(string(req.Agent)),
			"DUO_SESSION": req.Session,
			"DUO_TOKEN":   req.Token,
			"DUO_HOST":    req.Host,
			"DUO_PORT":    req.Port,
		},
		State:           raw,
		SessionIdentity: next.SessionID,
		Notices: func() []string {
			if next.SessionID == "" {
				return []string{"Codex session IDs are discovered from rollout files; simultaneous Codex sessions in the same worktree can make safe resume discovery ambiguous."}
			}
			return nil
		}(),
	}, nil
}

func projectTrustOverride(req driver.LaunchRequest) string {
	paths := make([]string, 0, 2)
	seen := make(map[string]bool)
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if absolute, err := filepath.Abs(path); err == nil {
			path = absolute
		}
		path = filepath.Clean(path)
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	add(req.RepositoryRoot)
	if req.CWD != "" {
		if root, err := exec.Command("git", "-C", req.CWD, "rev-parse", "--show-toplevel").Output(); err == nil {
			add(string(root))
		} else {
			add(req.CWD)
		}
	}
	entries := make([]string, 0, len(paths))
	for _, path := range paths {
		entries = append(entries, strconv.Quote(path)+`={trust_level="trusted"}`)
	}
	return "projects={" + strings.Join(entries, ",") + "}"
}

func checkCodexCLI() (string, string) {
	path, err := lookPath(Name)
	if err != nil {
		return "", "the codex CLI is not on PATH"
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err == nil {
		return path, ""
	}
	reason := strings.TrimSpace(string(output))
	if newline := strings.IndexByte(reason, '\n'); newline >= 0 {
		reason = strings.TrimSpace(reason[:newline])
	}
	if reason == "" {
		reason = err.Error()
	}
	if len(reason) > 200 {
		reason = reason[:200] + "…"
	}
	return "", "the codex CLI could not start: " + reason
}

func (p *Plugin) State() (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.learned {
		if ids := findNewSessionIDs(codexHome(), p.current); len(ids) > 1 || (len(ids) == 1 && ids[0] != p.current.SessionID) {
			p.current.SessionID = ""
			p.learned = false
		}
	}
	return json.Marshal(p.current)
}

func (p *Plugin) watch(target state) {
	interval, settle := watchInterval, settleInterval
	p.mu.Lock()
	if p.stop != nil {
		p.stop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.stop = cancel
	p.mu.Unlock()
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		candidate := ""
		candidateSince := time.Time{}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ids := findNewSessionIDs(codexHome(), target)
				if len(ids) != 1 || (candidate != "" && ids[0] != candidate) {
					candidate, candidateSince = "", time.Time{}
					continue
				}
				if candidate == "" {
					candidate, candidateSince = ids[0], time.Now()
					continue
				}
				if time.Since(candidateSince) < settle {
					continue
				}
				p.mu.Lock()
				if !p.current.StartedAt.Equal(target.StartedAt) || !sameCWD(p.current.CWD, target.CWD) {
					p.mu.Unlock()
					return
				}
				p.current.SessionID = ids[0]
				p.learned = true
				p.mu.Unlock()
				return
			}
		}
	}()
}

func (p *Plugin) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stop != nil {
		p.stop()
		p.stop = nil
	}
	return nil
}

func (*Plugin) Thinking() (*driver.ThinkingOptions, error) {
	return &driver.ThinkingOptions{Levels: []string{"minimal", "low", "medium", "high", "xhigh"}, Default: "medium"}, nil
}

func hasSubcommand(command, want string) bool {
	for _, token := range strings.Fields(command) {
		if token == want {
			return true
		}
	}
	return false
}

type rolloutMeta struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Payload   struct {
		ID  string `json:"id"`
		CWD string `json:"cwd"`
	} `json:"payload"`
}

func findNewSessionIDs(home string, target state) []string {
	start := target.StartedAt
	if start.IsZero() {
		return nil
	}
	root := filepath.Join(home, "sessions")
	var ids []string
	seen := map[string]bool{}
	days := []time.Time{
		start.Local().AddDate(0, 0, -1), start.Local(), start.Local().AddDate(0, 0, 1),
		start.UTC().AddDate(0, 0, -1), start.UTC(), start.UTC().AddDate(0, 0, 1),
	}
	seenDays := map[string]bool{}
	for _, date := range days {
		dir := filepath.Join(root, date.Format("2006"), date.Format("01"), date.Format("02"))
		if seenDays[dir] {
			continue
		}
		seenDays[dir] = true
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), "rollout-") || !strings.HasSuffix(entry.Name(), ".jsonl") {
				continue
			}
			meta, err := readRolloutMeta(filepath.Join(dir, entry.Name()))
			if err != nil || meta.Type != "session_meta" || meta.Payload.ID == "" || !sameCWD(meta.Payload.CWD, target.CWD) {
				continue
			}
			created, err := time.Parse(time.RFC3339Nano, meta.Timestamp)
			if err != nil || created.Before(start) {
				continue
			}
			if !seen[meta.Payload.ID] {
				seen[meta.Payload.ID] = true
				ids = append(ids, meta.Payload.ID)
			}
		}
	}
	return ids
}

func readRolloutMeta(path string) (rolloutMeta, error) {
	file, err := os.Open(path)
	if err != nil {
		return rolloutMeta{}, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 1<<20)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return rolloutMeta{}, err
		}
		return rolloutMeta{}, errors.New("empty rollout")
	}
	var meta rolloutMeta
	if err := json.Unmarshal(scanner.Bytes(), &meta); err != nil {
		return rolloutMeta{}, err
	}
	return meta, nil
}

func sameCWD(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if real, err := filepath.EvalSymlinks(a); err == nil {
		a = real
	}
	if real, err := filepath.EvalSymlinks(b); err == nil {
		b = real
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func codexHome() string {
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
		return home
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

func findDuoCommand() (string, error) {
	current, _ := os.Executable()
	if filepath.Base(current) == "duo" || filepath.Base(current) == "duo.exe" {
		return current, nil
	}
	if current != "" {
		candidate := filepath.Join(filepath.Dir(current), "duo")
		if executable(candidate) && validDuo(candidate) {
			return candidate, nil
		}
	}
	path, err := lookPathDuo("duo")
	if err != nil || !validDuo(path) {
		return "", fmt.Errorf("duo was not found beside the plugin or on PATH")
	}
	return path, nil
}

func validDuo(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	return err == nil && strings.HasPrefix(strings.ToLower(strings.TrimSpace(string(output))), "duo ")
}

func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}
