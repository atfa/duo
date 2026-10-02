package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/atfa/duo/internal/protocol"
)

// AgyActivitySink receives normalized activity events from AgyWatcher.
type AgyActivitySink interface {
	OnActivity(agent protocol.AgentID, msg protocol.Message)
}

// FuncActivitySink implements AgyActivitySink using a function.
type FuncActivitySink func(agent protocol.AgentID, msg protocol.Message)

func (f FuncActivitySink) OnActivity(agent protocol.AgentID, msg protocol.Message) {
	if f != nil {
		f(agent, msg)
	}
}

// TranscriptStep represents one step entry in agy's transcript.jsonl.
type TranscriptStep struct {
	StepIndex int                  `json:"step_index"`
	Source    string               `json:"source"`
	Type      string               `json:"type"`   // USER_INPUT, PLANNER_RESPONSE, GENERIC, etc.
	Status    string               `json:"status"` // DONE, ERROR, etc.
	CreatedAt string               `json:"created_at"`
	Content   string               `json:"content,omitempty"`
	Thinking  string               `json:"thinking,omitempty"`
	ToolCalls []TranscriptToolCall `json:"tool_calls,omitempty"`
}

// TranscriptToolCall represents an invoked tool within a PLANNER_RESPONSE step.
type TranscriptToolCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

var convIDRegex = regexp.MustCompile(`(?:Created conversation |found conversation |conversation=|conversationID=")([a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12})`)

// ExtractConversationID extracts the Agy conversation UUID from log output.
func ExtractConversationID(r io.Reader) (string, error) {
	var lastID string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if m := convIDRegex.FindStringSubmatch(line); len(m) > 1 {
			lastID = m[1]
		}
	}
	return lastID, scanner.Err()
}

// SummarizeToolArgs formats tool arguments into a human-readable preview summary.
func SummarizeToolArgs(tool string, args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	switch tool {
	case "duo_send":
		if m, ok := args["message"].(string); ok {
			return truncateStr(m, 60)
		}
	case "duo_set_status":
		if r, ok := args["ready"]; ok {
			return fmt.Sprintf("ready=%v", r)
		}
	case "duo_set_verification":
		// The bridge extensions send "result"; "verdict" is the MCP spelling.
		if v, ok := args["result"].(string); ok {
			return v
		}
	case "duo_set_plan":
		return "updating shared plan"
	case "duo_escalate":
		if r, ok := args["reason"].(string); ok {
			return truncateStr(r, 60)
		}
	case "run_command":
		if s, ok := args["toolSummary"].(string); ok && s != "" {
			return strings.Trim(s, "\"")
		}
		if c, ok := args["CommandLine"].(string); ok && c != "" {
			return truncateStr(strings.Trim(c, "\""), 60)
		}
	case "view_file":
		if p, ok := args["AbsolutePath"].(string); ok {
			return filepath.Base(strings.Trim(p, "\""))
		}
	case "write_to_file", "replace_file_content":
		if p, ok := args["TargetFile"].(string); ok {
			return filepath.Base(strings.Trim(p, "\""))
		}
	case "search_web":
		if q, ok := args["query"].(string); ok {
			return truncateStr(strings.Trim(q, "\""), 50)
		}
	case "read_url_content":
		if u, ok := args["Url"].(string); ok {
			return truncateStr(strings.Trim(u, "\""), 50)
		}
	}

	// Sorted: Go randomizes map iteration, so an unsorted walk would pick a
	// different argument for the same tool call on every render.
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if s, ok := args[k].(string); ok && s != "" {
			return fmt.Sprintf("%s=%s", k, truncateStr(s, 40))
		}
	}
	return ""
}

func truncateStr(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	if len(s) > maxLen {
		return s[:maxLen-3] + "..."
	}
	return s
}

// AgyUsage holds token metrics extracted from agy conversation metadata.
type AgyUsage struct {
	PromptTokens     int
	CandidatesTokens int
	CachedTokens     int
	TotalInputTokens int
	Model            string
	ContextWindow    int
}

// ParseAgyGenMetadata parses the binary protobuf from gen_metadata to extract
// promptTokens, candidatesTokens, cachedTokens, and model name.
func ParseAgyGenMetadata(data []byte) (AgyUsage, bool) {
	var usage AgyUsage
	// Top level: find field 1 (length-delimited)
	cur := data
	var f1 []byte
	for len(cur) > 0 {
		fn, wt, val, rest, ok := parseProtoField(cur)
		if !ok {
			break
		}
		if fn == 1 && wt == 2 {
			f1 = val
			break
		}
		cur = rest
	}
	if len(f1) == 0 {
		return usage, false
	}

	// Inside f1: find field 17 (usage) and field 19 (model name)
	cur = f1
	var f17 []byte
	for len(cur) > 0 {
		fn, wt, val, rest, ok := parseProtoField(cur)
		if !ok {
			break
		}
		if fn == 17 && wt == 2 {
			f17 = val
		} else if fn == 19 && wt == 2 {
			usage.Model = string(val)
		}
		cur = rest
	}

	if len(f17) > 0 {
		// Inside f17: find field 2
		cur = f17
		var f2 []byte
		for len(cur) > 0 {
			fn, wt, val, rest, ok := parseProtoField(cur)
			if !ok {
				break
			}
			if fn == 2 && wt == 2 {
				f2 = val
				break
			}
			cur = rest
		}

		if len(f2) > 0 {
			// Inside f2: field 2 (prompt), field 3 (candidates), field 5 (cached)
			cur = f2
			for len(cur) > 0 {
				fn, wt, val, rest, ok := parseProtoField(cur)
				if !ok {
					break
				}
				if wt == 0 {
					v, _ := binary.Uvarint(val)
					switch fn {
					case 2:
						usage.PromptTokens = int(v)
					case 3:
						usage.CandidatesTokens = int(v)
					case 5:
						usage.CachedTokens = int(v)
					}
				}
				cur = rest
			}
		}
	}

	usage.TotalInputTokens = usage.PromptTokens + usage.CachedTokens
	usage.ContextWindow = resolveAgyContextWindow(usage.Model)
	return usage, usage.TotalInputTokens > 0 || usage.CandidatesTokens > 0
}

func parseProtoField(data []byte) (fieldNum int, wireType int, val []byte, rest []byte, ok bool) {
	if len(data) == 0 {
		return 0, 0, nil, nil, false
	}
	key, n := binary.Uvarint(data)
	if n <= 0 {
		return 0, 0, nil, nil, false
	}
	data = data[n:]
	fieldNum = int(key >> 3)
	wireType = int(key & 7)

	switch wireType {
	case 0: // varint
		_, vn := binary.Uvarint(data)
		if vn <= 0 {
			return 0, 0, nil, nil, false
		}
		val = data[:vn]
		rest = data[vn:]
		return fieldNum, wireType, val, rest, true
	case 1: // 64-bit
		if len(data) < 8 {
			return 0, 0, nil, nil, false
		}
		val = data[:8]
		rest = data[8:]
		return fieldNum, wireType, val, rest, true
	case 2: // length-delimited
		length, ln := binary.Uvarint(data)
		if ln <= 0 || int(length) < 0 || len(data[ln:]) < int(length) {
			return 0, 0, nil, nil, false
		}
		val = data[ln : ln+int(length)]
		rest = data[ln+int(length):]
		return fieldNum, wireType, val, rest, true
	case 5: // 32-bit
		if len(data) < 4 {
			return 0, 0, nil, nil, false
		}
		val = data[:4]
		rest = data[4:]
		return fieldNum, wireType, val, rest, true
	default:
		return 0, 0, nil, nil, false
	}
}

func resolveAgyContextWindow(model string) int {
	lower := strings.ToLower(model)
	switch {
	case strings.Contains(lower, "gemini-1.5-pro"), strings.Contains(lower, "gemini-2.5-pro"), strings.Contains(lower, "gemini-pro"):
		return 2_097_152
	case strings.Contains(lower, "gemini"):
		return 1_048_576
	case strings.Contains(lower, "claude"):
		return 200_000
	case strings.Contains(lower, "gpt"), strings.Contains(lower, "o1"), strings.Contains(lower, "o3"), strings.Contains(lower, "o4"), strings.Contains(lower, "deepseek"):
		return 128_000
	default:
		return 0
	}
}

func agyDBPathFromTranscript(transcriptPath string) string {
	if transcriptPath == "" {
		return ""
	}
	logsDir := filepath.Dir(transcriptPath)
	sysGenDir := filepath.Dir(logsDir)
	convDir := filepath.Dir(sysGenDir)
	convID := filepath.Base(convDir)
	brainDir := filepath.Dir(convDir)
	appDataDir := filepath.Dir(brainDir)
	return filepath.Join(appDataDir, "conversations", convID+".db")
}

// QueryLatestAgyUsage queries the latest generation metadata from the agy conversation database.
func QueryLatestAgyUsage(dbPath string) (AgyUsage, bool) {
	if dbPath == "" {
		return AgyUsage{}, false
	}
	if _, err := os.Stat(dbPath); err != nil {
		return AgyUsage{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sqlite3", "-readonly", dbPath, "SELECT hex(data) FROM gen_metadata ORDER BY idx DESC LIMIT 1;")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return AgyUsage{}, false
	}
	hexStr := strings.TrimSpace(stdout.String())
	if hexStr == "" {
		return AgyUsage{}, false
	}
	raw, err := hex.DecodeString(hexStr)
	if err != nil {
		return AgyUsage{}, false
	}
	return ParseAgyGenMetadata(raw)
}

// AgyWatcher observes agy's transcript.jsonl and generates normalized Duo activity messages.
type AgyWatcher struct {
	agent          protocol.AgentID
	transcriptPath string
	sink           AgyActivitySink

	mu       sync.Mutex
	running  bool
	stopCh   chan struct{}
	lastTool string
	lastStep int

	dbPath            string
	lastContextTokens int
	lastContextWindow int
	lastStepTime      time.Time
	lastSpeed         float64
}

// NewAgyWatcher creates a new transcript watcher for an agent.
func NewAgyWatcher(agent protocol.AgentID, transcriptPath string, sink AgyActivitySink) *AgyWatcher {
	dbPath := agyDBPathFromTranscript(transcriptPath)
	w := &AgyWatcher{
		agent:             agent,
		transcriptPath:    transcriptPath,
		sink:              sink,
		stopCh:            make(chan struct{}),
		lastStep:          -1,
		dbPath:            dbPath,
		lastStepTime:      time.Now(),
	}
	if usage, ok := QueryLatestAgyUsage(dbPath); ok {
		w.lastContextTokens = usage.TotalInputTokens
		if usage.ContextWindow > 0 {
			w.lastContextWindow = usage.ContextWindow
		}
	}
	return w
}

// SetUsage explicitly sets context usage (useful for testing).
func (w *AgyWatcher) SetUsage(tokens, window int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastContextTokens = tokens
	if window > 0 {
		w.lastContextWindow = window
	}
}

// SetDBPath explicitly sets the database path (useful for testing).
func (w *AgyWatcher) SetDBPath(dbPath string) {
	usage, ok := QueryLatestAgyUsage(dbPath)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.dbPath = dbPath
	if ok {
		w.lastContextTokens = usage.TotalInputTokens
		if usage.ContextWindow > 0 {
			w.lastContextWindow = usage.ContextWindow
		}
	}
}

// Start begins tailing transcript.jsonl asynchronously.
func (w *AgyWatcher) Start(ctx context.Context) {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return
	}
	w.running = true
	w.mu.Unlock()

	go w.tailLoop(ctx)
}

// Stop stops the watcher.
func (w *AgyWatcher) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.running {
		return
	}
	w.running = false
	close(w.stopCh)
}

func (w *AgyWatcher) tailLoop(ctx context.Context) {
	var file *os.File
	var err error

	// 1. Wait for transcript file to exist
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		default:
		}

		file, err = os.Open(w.transcriptPath)
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		default:
		}

		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			return
		}

		trimmed := strings.TrimSpace(string(line))
		if len(trimmed) == 0 {
			continue
		}

		w.ProcessLine([]byte(trimmed))
	}
}

// ProcessLine parses a single transcript step line and dispatches events.
func (w *AgyWatcher) ProcessLine(line []byte) {
	var step TranscriptStep
	if err := json.Unmarshal(line, &step); err != nil {
		return
	}

	w.mu.Lock()
	w.lastStep = step.StepIndex
	w.mu.Unlock()

	if w.sink == nil {
		return
	}

	now := time.Now()
	nowMilli := now.UnixMilli()

	switch step.Type {
	case "USER_INPUT":
		w.mu.Lock()
		w.lastStepTime = now
		w.lastSpeed = 0
		ctxToks := w.lastContextTokens
		ctxWin := w.lastContextWindow
		w.mu.Unlock()

		w.sink.OnActivity(w.agent, protocol.Message{
			Version:       protocol.Version,
			Type:          protocol.MsgActivity,
			Agent:         w.agent,
			Activity:      protocol.ActivityAgentStart,
			ContextTokens: ctxToks,
			ContextWindow: ctxWin,
			Timestamp:     nowMilli,
		})

	case "PLANNER_RESPONSE":
		w.mu.Lock()
		dbPath := w.dbPath
		w.mu.Unlock()

		usage, hasUsage := QueryLatestAgyUsage(dbPath)

		w.mu.Lock()
		elapsed := now.Sub(w.lastStepTime)
		w.lastStepTime = now

		candidates := 0
		if hasUsage {
			w.lastContextTokens = usage.TotalInputTokens
			if usage.ContextWindow > 0 {
				w.lastContextWindow = usage.ContextWindow
			}
			candidates = usage.CandidatesTokens
		}
		if candidates == 0 {
			charCount := len(step.Thinking) + len(step.Content)
			for _, tc := range step.ToolCalls {
				charCount += len(tc.Name)
				for k, v := range tc.Args {
					charCount += len(k) + len(fmt.Sprint(v))
				}
			}
			candidates = charCount / 4
		}
		if elapsed >= 200*time.Millisecond && candidates > 0 {
			w.lastSpeed = float64(candidates) / elapsed.Seconds()
		} else {
			w.lastSpeed = 0
		}
		ctxToks := w.lastContextTokens
		ctxWin := w.lastContextWindow
		speed := w.lastSpeed
		w.mu.Unlock()

		if step.Thinking != "" {
			w.sink.OnActivity(w.agent, protocol.Message{
				Version:         protocol.Version,
				Type:            protocol.MsgActivity,
				Agent:           w.agent,
				Activity:        protocol.ActivityStream,
				Detail:          truncateStr(step.Thinking, 60),
				ContextTokens:   ctxToks,
				ContextWindow:   ctxWin,
				TokensPerSecond: speed,
				Timestamp:       nowMilli,
			})
		}

		if len(step.ToolCalls) > 0 {
			for _, tc := range step.ToolCalls {
				detail := SummarizeToolArgs(tc.Name, tc.Args)
				w.mu.Lock()
				w.lastTool = tc.Name
				w.mu.Unlock()

				w.sink.OnActivity(w.agent, protocol.Message{
					Version:       protocol.Version,
					Type:          protocol.MsgActivity,
					Agent:         w.agent,
					Activity:      protocol.ActivityToolStart,
					Tool:          tc.Name,
					Detail:        detail,
					ContextTokens: ctxToks,
					ContextWindow: ctxWin,
					Timestamp:     nowMilli,
				})
			}
		} else {
			if step.Content != "" {
				w.sink.OnActivity(w.agent, protocol.Message{
					Version:   protocol.Version,
					Type:      protocol.MsgAssistantMessage,
					Agent:     w.agent,
					Text:      step.Content,
					Timestamp: nowMilli,
				})
				w.sink.OnActivity(w.agent, protocol.Message{
					Version:         protocol.Version,
					Type:            protocol.MsgActivity,
					Agent:           w.agent,
					Activity:        protocol.ActivityStream,
					Detail:          truncateStr(step.Content, 60),
					ContextTokens:   ctxToks,
					ContextWindow:   ctxWin,
					TokensPerSecond: speed,
					Timestamp:       nowMilli,
				})
			}
			w.mu.Lock()
			w.lastSpeed = 0
			w.mu.Unlock()
			w.sink.OnActivity(w.agent, protocol.Message{
				Version:       protocol.Version,
				Type:          protocol.MsgActivity,
				Agent:         w.agent,
				Activity:      protocol.ActivityAgentSettled,
				ContextTokens: ctxToks,
				ContextWindow: ctxWin,
				Timestamp:     nowMilli,
			})
		}

	case "GENERIC":
		w.mu.Lock()
		tool := w.lastTool
		w.lastStepTime = now
		w.lastSpeed = 0
		ctxToks := w.lastContextTokens
		ctxWin := w.lastContextWindow
		w.mu.Unlock()

		if step.Status == "ERROR" {
			w.sink.OnActivity(w.agent, protocol.Message{
				Version:       protocol.Version,
				Type:          protocol.MsgActivity,
				Agent:         w.agent,
				Activity:      protocol.ActivityToolError,
				Tool:          tool,
				Detail:        truncateStr(step.Content, 80),
				ContextTokens: ctxToks,
				ContextWindow: ctxWin,
				Timestamp:     nowMilli,
			})
		} else {
			w.sink.OnActivity(w.agent, protocol.Message{
				Version:       protocol.Version,
				Type:          protocol.MsgActivity,
				Agent:         w.agent,
				Activity:      protocol.ActivityToolEnd,
				Tool:          tool,
				ContextTokens: ctxToks,
				ContextWindow: ctxWin,
				Timestamp:     nowMilli,
			})
		}
	}
}
