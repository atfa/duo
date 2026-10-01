package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
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
}

// NewAgyWatcher creates a new transcript watcher for an agent.
func NewAgyWatcher(agent protocol.AgentID, transcriptPath string, sink AgyActivitySink) *AgyWatcher {
	return &AgyWatcher{
		agent:          agent,
		transcriptPath: transcriptPath,
		sink:           sink,
		stopCh:         make(chan struct{}),
		lastStep:       -1,
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

	now := time.Now().UnixMilli()

	switch step.Type {
	case "USER_INPUT":
		w.sink.OnActivity(w.agent, protocol.Message{
			Version:   protocol.Version,
			Type:      protocol.MsgActivity,
			Agent:     w.agent,
			Activity:  protocol.ActivityAgentStart,
			Timestamp: now,
		})

	case "PLANNER_RESPONSE":
		if step.Thinking != "" {
			w.sink.OnActivity(w.agent, protocol.Message{
				Version:   protocol.Version,
				Type:      protocol.MsgActivity,
				Agent:     w.agent,
				Activity:  protocol.ActivityStream,
				Detail:    truncateStr(step.Thinking, 60),
				Timestamp: now,
			})
		}

		if len(step.ToolCalls) > 0 {
			for _, tc := range step.ToolCalls {
				detail := SummarizeToolArgs(tc.Name, tc.Args)
				w.mu.Lock()
				w.lastTool = tc.Name
				w.mu.Unlock()

				w.sink.OnActivity(w.agent, protocol.Message{
					Version:   protocol.Version,
					Type:      protocol.MsgActivity,
					Agent:     w.agent,
					Activity:  protocol.ActivityToolStart,
					Tool:      tc.Name,
					Detail:    detail,
					Timestamp: now,
				})
			}
		} else {
			if step.Content != "" {
				w.sink.OnActivity(w.agent, protocol.Message{
					Version:   protocol.Version,
					Type:      protocol.MsgAssistantMessage,
					Agent:     w.agent,
					Text:      step.Content,
					Timestamp: now,
				})
				w.sink.OnActivity(w.agent, protocol.Message{
					Version:   protocol.Version,
					Type:      protocol.MsgActivity,
					Agent:     w.agent,
					Activity:  protocol.ActivityStream,
					Detail:    truncateStr(step.Content, 60),
					Timestamp: now,
				})
			}
			w.sink.OnActivity(w.agent, protocol.Message{
				Version:   protocol.Version,
				Type:      protocol.MsgActivity,
				Agent:     w.agent,
				Activity:  protocol.ActivityAgentSettled,
				Timestamp: now,
			})
		}

	case "GENERIC":
		w.mu.Lock()
		tool := w.lastTool
		w.mu.Unlock()

		if step.Status == "ERROR" {
			w.sink.OnActivity(w.agent, protocol.Message{
				Version:   protocol.Version,
				Type:      protocol.MsgActivity,
				Agent:     w.agent,
				Activity:  protocol.ActivityToolError,
				Tool:      tool,
				Detail:    truncateStr(step.Content, 80),
				Timestamp: now,
			})
		} else {
			w.sink.OnActivity(w.agent, protocol.Message{
				Version:   protocol.Version,
				Type:      protocol.MsgActivity,
				Agent:     w.agent,
				Activity:  protocol.ActivityToolEnd,
				Tool:      tool,
				Timestamp: now,
			})
		}
	}
}
