// Package codex implements the Codex CLI driver for Duo.
package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/mcp"
	"github.com/atfa/duo/internal/protocol"
)

const (
	Name          = "codex"
	DriverVersion = "1.0.0"
)

var (
	lookPath       = exec.LookPath
	lookPathDuo    = exec.LookPath
	duoCommand     = findDuoCommand
	codexAppPaths  = defaultCodexAppPaths
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
	mu           sync.Mutex
	current      state
	stop         context.CancelFunc
	activityStop context.CancelFunc
	activitySink *codexActivitySink
	learned      bool
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
			Models:      true,
			Thinking:    true,
			Bridge:      driver.BridgeNone,
			PTYFallback: true,
			// The launch command wires Duo's MCP server, so the agent can call
			// every coordination tool.
			Tools: mcp.ToolNames(),
		},
		MCP:      true,
		Activity: true,
	}, nil
}

// Models asks Codex's app-server for the models it currently exposes in its
// picker. The server is authoritative for account, provider and CLI-version
// specific availability.
func (*Plugin) Models() (*driver.ModelList, error) {
	path, reason := findCodexCLI()
	if reason != "" {
		return nil, errors.New(reason)
	}
	ctx, cancel := context.WithTimeout(context.Background(), driver.ModelListTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "app-server", "--stdio")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}
	stopped := false
	stop := func() {
		if !stopped {
			_ = stdin.Close()
			_ = cmd.Wait()
			stopped = true
		}
	}
	defer func() {
		stop()
	}()
	fail := func(err error) (*driver.ModelList, error) {
		stop()
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return nil, fmt.Errorf("Codex app-server: %w: %s", err, detail)
		}
		return nil, fmt.Errorf("Codex app-server: %w", err)
	}

	w := bufio.NewWriter(stdin)
	if err := writeAppServerMessage(w, map[string]any{"method": "initialize", "id": 1, "params": map[string]any{"clientInfo": map[string]string{"name": "duo", "version": DriverVersion}, "capabilities": map[string]bool{"experimentalApi": true}}}); err != nil {
		return fail(err)
	}
	reader := bufio.NewScanner(stdout)
	reader.Buffer(make([]byte, 4096), 4<<20)
	if _, err := appServerResponse(reader, 1); err != nil {
		return fail(fmt.Errorf("initialize: %w", err))
	}
	if err := writeAppServerMessage(w, map[string]any{"method": "initialized"}); err != nil {
		return fail(err)
	}
	var models []driver.Model
	cursor := ""
	seenCursors := make(map[string]bool)
	requestID := 2
	for {
		params := map[string]any{"includeHidden": false, "limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := writeAppServerMessage(w, map[string]any{"method": "model/list", "id": requestID, "params": params}); err != nil {
			return fail(err)
		}
		var result struct {
			Data []struct {
				ID          string            `json:"id"`
				Model       string            `json:"model"`
				DisplayName string            `json:"displayName"`
				Input       []string          `json:"inputModalities"`
				Reasoning   []json.RawMessage `json:"supportedReasoningEfforts"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		response, err := appServerResponse(reader, requestID)
		if err != nil {
			return fail(fmt.Errorf("model/list: %w", err))
		}
		if err := json.Unmarshal(response, &result); err != nil {
			return fail(fmt.Errorf("decode model catalog: %w", err))
		}
		for _, item := range result.Data {
			id := item.Model
			if id == "" {
				id = item.ID
			}
			if id == "" {
				continue
			}
			name := item.DisplayName
			if name == "" {
				name = id
			}
			images := false
			for _, modality := range item.Input {
				images = images || modality == "image"
			}
			models = append(models, driver.Model{Provider: "openai", ID: id, DisplayName: name, Images: images, Thinking: len(item.Reasoning) > 0})
		}
		if result.NextCursor == nil || *result.NextCursor == "" {
			if len(models) == 0 {
				return fail(errors.New("Codex returned no selectable models"))
			}
			stop()
			return &driver.ModelList{Models: models}, nil
		}
		if seenCursors[*result.NextCursor] {
			return fail(errors.New("Codex model catalog repeated a pagination cursor"))
		}
		seenCursors[*result.NextCursor] = true
		cursor = *result.NextCursor
		requestID++
	}
}

func writeAppServerMessage(w *bufio.Writer, message any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if _, err = w.Write(data); err == nil {
		_, err = w.WriteString("\n")
	}
	if err == nil {
		err = w.Flush()
	}
	return err
}

func appServerResponse(reader *bufio.Scanner, id int) (json.RawMessage, error) {
	for reader.Scan() {
		var message struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(reader.Bytes(), &message) != nil || message.ID == nil || *message.ID != id {
			continue
		}
		if message.Error != nil {
			return nil, errors.New(message.Error.Message)
		}
		return message.Result, nil
	}
	if err := reader.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
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
	if prior.SessionID == "" && prior.CWD != "" && !prior.StartedAt.IsZero() {
		// A previous Duo checkpoint may have captured the prepared state before
		// Codex wrote its rollout metadata. On resume, accept only one matching
		// rollout from the worktree's recent day; ambiguity remains unresolved.
		ids := findNewSessionIDs(codexHome(), state{CWD: prior.CWD, StartedAt: prior.StartedAt.Add(-24 * time.Hour)})
		if len(ids) == 1 {
			prior.SessionID = ids[0]
		}
	}
	mcpCommand, err := duoCommand()
	if err != nil {
		return nil, &driver.RPCError{Code: driver.CodeUnavailable, Message: "Codex MCP tools require a runnable duo CLI: " + err.Error()}
	}

	base := strings.TrimSpace(req.BaseCommand)
	if base == "" || base == Name || strings.HasPrefix(base, Name+" ") {
		path, reason := checkCodexCLI()
		if reason != "" {
			return nil, &driver.RPCError{Code: driver.CodeUnavailable, Message: reason}
		}
		base = driver.ShellQuote(path) + strings.TrimPrefix(base, Name)
	}
	if driver.HasFlag(base, "--sandbox", "-s", "--ask-for-approval", "-a", "--full-auto") {
		return nil, &driver.RPCError{
			Code:    driver.CodeInvalid,
			Message: "Codex Duo mode bypasses sandbox and approvals; remove sandbox or approval flags from the base command",
		}
	}
	command := base
	// Keep Duo's Codex process out of a potentially stale shared daemon.
	if !driver.HasFlag(command, "--no-daemon") {
		command += " --no-daemon"
	}
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
	if next.CWD != "" {
		p.watchActivity(next, mcp.Config{
			Agent: protocol.CanonicalAgent(string(req.Agent)), SessionID: req.Session,
			Token: req.Token, Host: req.Host, Port: req.Port, ClientType: "observer",
		})
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
	path, reason := findCodexCLI()
	if reason != "" {
		return "", "the codex CLI is not on PATH"
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err == nil {
		return path, ""
	}
	reason = strings.TrimSpace(string(output))
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

func findCodexCLI() (string, string) {
	if path, err := lookPath(Name); err == nil {
		return path, ""
	}
	home, _ := os.UserHomeDir()
	for _, path := range codexAppPaths(home) {
		if executable(path) {
			return path, ""
		}
	}
	return "", "the codex CLI is not on PATH"
}

func defaultCodexAppPaths(home string) []string {
	return []string{
		filepath.Join("/Applications/ChatGPT.app", "Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex"),
		filepath.Join(home, "Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex"),
	}
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

func (p *Plugin) watchActivity(target state, cfg mcp.Config) {
	p.mu.Lock()
	if p.activityStop != nil {
		p.activityStop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.activityStop = cancel
	if p.activitySink != nil {
		p.activitySink.Close()
	}
	p.activitySink = &codexActivitySink{cfg: cfg}
	sink := p.activitySink
	p.mu.Unlock()

	// A resumed rollout contains old turns. Start at its current end so only
	// activity from the newly resumed turn affects Duo's work state.
	var offset int64
	initialized := target.SessionID == ""
	if target.SessionID != "" {
		if path := findRolloutPath(codexHome(), target, target.SessionID); path != "" {
			if info, err := os.Stat(path); err == nil {
				offset, initialized = info.Size(), true
			}
		}
	}
	interval := watchInterval
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var pending []byte
		path := ""
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			p.mu.Lock()
			id := p.current.SessionID
			p.mu.Unlock()
			sink.Retry()
			if id == "" {
				continue
			}
			if id != target.SessionID {
				target.SessionID = id
				path = ""
			}
			if path == "" {
				path = findRolloutPath(codexHome(), target, id)
				if path == "" {
					continue
				}
				if !initialized {
					if info, err := os.Stat(path); err == nil {
						offset, initialized = info.Size(), true
					}
				}
			}
			for _, message := range readRolloutMessages(path, &offset, &pending) {
				message.Version = protocol.Version
				message.Agent = cfg.Agent
				message.Timestamp = time.Now().UnixMilli()
				if message.Type == protocol.MsgAssistantMessage {
					sink.OnAssistantMessage(message)
				} else {
					sink.OnActivity(message)
				}
			}
		}
	}()
}

type codexActivitySink struct {
	cfg      mcp.Config
	mu       sync.Mutex
	bc       *mcp.BridgeClient
	pending  *protocol.Message
	messages []protocol.Message
	closed   bool
}

func (s *codexActivitySink) OnActivity(message protocol.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.pending = &message
	s.retryLocked()
}

func (s *codexActivitySink) OnAssistantMessage(message protocol.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.messages = append(s.messages, message)
	s.retryLocked()
}

func (s *codexActivitySink) Retry() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retryLocked()
}

func (s *codexActivitySink) retryLocked() {
	if s.closed || (len(s.messages) == 0 && s.pending == nil) {
		return
	}
	if s.bc == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		bc, err := mcp.ConnectBridge(ctx, s.cfg)
		cancel()
		if err != nil {
			return
		}
		s.bc = bc
	}
	for len(s.messages) > 0 {
		if err := s.bc.Send(s.messages[0]); err != nil {
			_ = s.bc.Close()
			s.bc = nil
			return
		}
		s.messages = s.messages[1:]
	}
	if s.pending == nil {
		return
	}
	if err := s.bc.Send(*s.pending); err != nil {
		_ = s.bc.Close()
		s.bc = nil
		return
	}
	s.pending = nil
}

func (s *codexActivitySink) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.pending = nil
	s.messages = nil
	if s.bc != nil {
		_ = s.bc.Close()
		s.bc = nil
	}
}

func readRolloutMessages(path string, offset *int64, pending *[]byte) []protocol.Message {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil
	}
	if info.Size() < *offset {
		*offset = 0
		*pending = nil
	}
	if _, err := file.Seek(*offset, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(file)
	if err != nil || len(data) == 0 {
		return nil
	}
	*offset += int64(len(data))
	lines := append(*pending, data...)
	parts := bytes.Split(lines, []byte{'\n'})
	*pending = append((*pending)[:0], parts[len(parts)-1]...)
	messages := make([]protocol.Message, 0, 1)
	for _, line := range parts[:len(parts)-1] {
		var event struct {
			Type    string `json:"type"`
			Payload struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &event) != nil {
			continue
		}
		if event.Type == "response_item" && event.Payload.Type == "message" && event.Payload.Role == "assistant" {
			var text strings.Builder
			for _, part := range event.Payload.Content {
				if part.Type == "output_text" {
					text.WriteString(part.Text)
				}
			}
			if body := strings.TrimSpace(text.String()); body != "" {
				messages = append(messages, protocol.Message{Type: protocol.MsgAssistantMessage, Text: body})
			}
			continue
		}
		if event.Type != "event_msg" {
			continue
		}
		switch event.Payload.Type {
		case "task_started":
			messages = append(messages, protocol.Message{Type: protocol.MsgActivity, Activity: protocol.ActivityAgentStart})
		case "task_complete", "turn_aborted":
			messages = append(messages, protocol.Message{Type: protocol.MsgActivity, Activity: protocol.ActivityAgentSettled})
		}
	}
	return messages
}

func (p *Plugin) Close() error {
	p.mu.Lock()
	if p.stop != nil {
		p.stop()
		p.stop = nil
	}
	if p.activityStop != nil {
		p.activityStop()
		p.activityStop = nil
	}
	sink := p.activitySink
	p.activitySink = nil
	p.mu.Unlock()
	if sink != nil {
		sink.Close()
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
		ID     string          `json:"id"`
		CWD    string          `json:"cwd"`
		Source json.RawMessage `json:"source"`
	} `json:"payload"`
}

func (meta rolloutMeta) isSubagent() bool {
	var source map[string]json.RawMessage
	if json.Unmarshal(meta.Payload.Source, &source) != nil {
		return false
	}
	var subagent map[string]json.RawMessage
	return json.Unmarshal(source["subagent"], &subagent) == nil && subagent != nil
}

func findRolloutPath(home string, target state, sessionID string) string {
	if target.StartedAt.IsZero() {
		return ""
	}
	days := []time.Time{
		target.StartedAt.Local().AddDate(0, 0, -1), target.StartedAt.Local(), target.StartedAt.Local().AddDate(0, 0, 1),
		target.StartedAt.UTC().AddDate(0, 0, -1), target.StartedAt.UTC(), target.StartedAt.UTC().AddDate(0, 0, 1),
	}
	seen := make(map[string]bool)
	for _, date := range days {
		dir := filepath.Join(home, "sessions", date.Format("2006"), date.Format("01"), date.Format("02"))
		if seen[dir] {
			continue
		}
		seen[dir] = true
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), "rollout-") || !strings.HasSuffix(entry.Name(), ".jsonl") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			meta, err := readRolloutMeta(path)
			if err == nil && meta.Type == "session_meta" && !meta.isSubagent() && meta.Payload.ID == sessionID && sameCWD(meta.Payload.CWD, target.CWD) {
				return path
			}
		}
	}
	return ""
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
			if err != nil || meta.Type != "session_meta" || meta.isSubagent() || meta.Payload.ID == "" || !sameCWD(meta.Payload.CWD, target.CWD) {
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
