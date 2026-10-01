package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/version"
)

// Tool represents an MCP tool definition.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// DefaultTools returns the standard Duo state machine tools exposed via MCP.
func DefaultTools() []Tool {
	return []Tool{
		{
			Name:        "duo_status",
			Description: "Read authoritative session status: mode, phase, shared plan, verification, branches, and worktrees.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "duo_send",
			Description: "Send an important live message to the peer agent (for findings, questions, proposals).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{
						"type":        "string",
						"description": "The message text to send to the peer agent.",
					},
				},
				"required": []string{"message"},
			},
		},
		{
			Name:        "duo_set_status",
			Description: "In Goal mode, sign the current phase (ready: true) or revoke signature (ready: false). In Fast mode, Austin uses ready: true to request independent verification.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"ready": map[string]any{
						"type":        "boolean",
						"description": "Whether ready to advance the phase or request verification.",
					},
					"note": map[string]any{
						"type":        "string",
						"description": "Optional concise reason or completion note.",
					},
				},
				"required": []string{"ready"},
			},
		},
		{
			Name:        "duo_set_verification",
			Description: "Fast mode (Tony only): Report verification verdict (passed or issue_found) for Austin's exact commit.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"verdict": map[string]any{
						"type":        "string",
						"enum":        []string{"passed", "issue_found"},
						"description": "Verification verdict: 'passed' or 'issue_found'.",
					},
					"note": map[string]any{
						"type":        "string",
						"description": "Detailed review findings or concrete issue description.",
					},
				},
				"required": []string{"verdict"},
			},
		},
		{
			Name:        "duo_set_plan",
			Description: "Goal mode only: Create or replace the entire shared Plan. Increments plan version and resets signatures.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"plan": map[string]any{
						"type":        "string",
						"description": "Complete Markdown text of the shared plan.",
					},
				},
				"required": []string{"plan"},
			},
		},
		{
			Name:        "duo_escalate",
			Description: "Fast mode only: Dynamically escalate the session to Goal mode on the fly with a shared plan v1.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"reason": map[string]any{
						"type":        "string",
						"description": "Explanation why escalation to Goal mode is required.",
					},
				},
				"required": []string{"reason"},
			},
		},
	}
}

// Config specifies connection settings to Duo's transport server.
type Config struct {
	Agent     protocol.AgentID
	SessionID string
	Token     string
	Host      string
	Port      string
}

// LoadConfigFromEnv fills unset fields from standard DUO_* environment variables.
func (c *Config) LoadConfigFromEnv() {
	if c.Agent == "" {
		c.Agent = protocol.CanonicalAgent(os.Getenv("DUO_AGENT"))
	}
	if c.SessionID == "" {
		c.SessionID = os.Getenv("DUO_SESSION")
	}
	if c.Token == "" {
		c.Token = os.Getenv("DUO_TOKEN")
	}
	if c.Host == "" {
		c.Host = os.Getenv("DUO_HOST")
	}
	if c.Port == "" {
		c.Port = os.Getenv("DUO_PORT")
	}
}

// BridgeClient talks to Duo's internal transport.Server.
type BridgeClient struct {
	conn      net.Conn
	agent     protocol.AgentID
	sessionID string
	token     string

	mu      sync.Mutex
	reqSeq  int64
	pending map[string]chan protocol.Message
}

func ConnectBridge(ctx context.Context, cfg Config) (*BridgeClient, error) {
	if cfg.Host == "" || cfg.Port == "" {
		return nil, fmt.Errorf("missing DUO_HOST or DUO_PORT")
	}
	addr := net.JoinHostPort(cfg.Host, cfg.Port)
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connect to Duo (%s): %w", addr, err)
	}

	bc := &BridgeClient{
		conn:      conn,
		agent:     cfg.Agent,
		sessionID: cfg.SessionID,
		token:     cfg.Token,
		pending:   make(map[string]chan protocol.Message),
	}

	// Send Hello
	hello := protocol.Message{
		Version:    protocol.Version,
		Type:       protocol.MsgHello,
		ClientType: "mcp",
		Agent:      cfg.Agent,
		SessionID:  cfg.SessionID,
		Token:      cfg.Token,
		Timestamp:  time.Now().UnixMilli(),
	}
	if err := bc.sendRaw(hello); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("send hello: %w", err)
	}

	go bc.readLoop()
	return bc, nil
}

func (bc *BridgeClient) Close() error {
	return bc.conn.Close()
}

func (bc *BridgeClient) sendRaw(msg protocol.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = bc.conn.Write(data)
	return err
}

func (bc *BridgeClient) readLoop() {
	scanner := bufio.NewScanner(bc.conn)
	for scanner.Scan() {
		var msg protocol.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		if msg.RequestID != "" {
			bc.mu.Lock()
			ch, ok := bc.pending[msg.RequestID]
			if ok {
				delete(bc.pending, msg.RequestID)
			}
			bc.mu.Unlock()
			if ok && ch != nil {
				ch <- msg
			}
		}
	}
}

func (bc *BridgeClient) Call(ctx context.Context, msg protocol.Message) (string, bool, error) {
	bc.mu.Lock()
	bc.reqSeq++
	reqID := fmt.Sprintf("mcp-%d-%d", time.Now().UnixNano(), bc.reqSeq)
	ch := make(chan protocol.Message, 1)
	bc.pending[reqID] = ch
	bc.mu.Unlock()

	defer func() {
		bc.mu.Lock()
		delete(bc.pending, reqID)
		bc.mu.Unlock()
	}()

	msg.Version = protocol.Version
	msg.Agent = bc.agent
	msg.From = bc.agent
	msg.RequestID = reqID
	msg.Timestamp = time.Now().UnixMilli()

	if err := bc.sendRaw(msg); err != nil {
		return "", false, fmt.Errorf("send message: %w", err)
	}

	select {
	case <-ctx.Done():
		return "", false, ctx.Err()
	case <-time.After(30 * time.Second):
		return "", false, fmt.Errorf("timed out waiting for Duo response")
	case res := <-ch:
		text := res.Text
		if res.State != "" {
			if text != "" {
				text += "\n\n" + res.State
			} else {
				text = res.State
			}
		}
		return text, res.OK, nil
	}
}

// Server handles MCP JSON-RPC protocol over stdio.
type Server struct {
	client *BridgeClient
	tools  []Tool
}

func NewServer(client *BridgeClient) *Server {
	return &Server{
		client: client,
		tools:  DefaultTools(),
	}
}

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   any    `json:"error,omitempty"`
}

type callToolParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

func (s *Server) Serve(r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}

		resp := s.handleRequest(context.Background(), req)
		if resp == nil {
			continue // Notification, no response
		}
		data, err := json.Marshal(resp)
		if err != nil {
			continue
		}
		data = append(data, '\n')
		if _, err := w.Write(data); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (s *Server) handleRequest(ctx context.Context, req jsonRPCRequest) *jsonRPCResponse {
	// Notifications without ID do not expect responses
	if req.ID == nil && req.Method == "notifications/initialized" {
		return nil
	}

	switch req.Method {
	case "initialize":
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    "duo-mcp-server",
					"version": version.Version,
				},
			},
		}

	case "tools/list":
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"tools": s.tools,
			},
		}

	case "tools/call":
		var params callToolParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return &jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: map[string]any{
					"code":    -32602,
					"message": "Invalid params: " + err.Error(),
				},
			}
		}

		res := s.executeTool(ctx, params)
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  res,
		}

	default:
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: map[string]any{
				"code":    -32601,
				"message": "Method not found: " + req.Method,
			},
		}
	}
}

func (s *Server) executeTool(ctx context.Context, params callToolParams) toolResult {
	if s.client == nil {
		return toolResult{
			Content: []toolContent{{Type: "text", Text: "Duo bridge client is not connected"}},
			IsError: true,
		}
	}

	var msg protocol.Message
	switch params.Name {
	case "duo_status":
		msg = protocol.Message{Type: protocol.MsgGetStatus}

	case "duo_send":
		text, _ := params.Arguments["message"].(string)
		if text == "" {
			return toolResult{Content: []toolContent{{Type: "text", Text: "message argument is required"}}, IsError: true}
		}
		peer := protocol.PeerOf(s.client.agent)
		msg = protocol.Message{
			Type: protocol.MsgPeerMessage,
			To:   peer,
			Text: text,
		}

	case "duo_set_status":
		readyVal, ok := params.Arguments["ready"]
		if !ok {
			return toolResult{Content: []toolContent{{Type: "text", Text: "ready boolean argument is required"}}, IsError: true}
		}
		var ready bool
		switch v := readyVal.(type) {
		case bool:
			ready = v
		case string:
			ready, _ = strconv.ParseBool(v)
		}
		note, _ := params.Arguments["note"].(string)
		msg = protocol.Message{
			Type:  protocol.MsgSetStatus,
			Ready: &ready,
			Note:  note,
		}

	case "duo_set_verification":
		verdict, _ := params.Arguments["verdict"].(string)
		note, _ := params.Arguments["note"].(string)
		if verdict != "passed" && verdict != "issue_found" {
			return toolResult{Content: []toolContent{{Type: "text", Text: "verdict must be 'passed' or 'issue_found'"}}, IsError: true}
		}
		msg = protocol.Message{
			Type:         protocol.MsgSetVerification,
			Verification: verdict,
			Note:         note,
		}

	case "duo_set_plan":
		plan, _ := params.Arguments["plan"].(string)
		if plan == "" {
			return toolResult{Content: []toolContent{{Type: "text", Text: "plan text is required"}}, IsError: true}
		}
		msg = protocol.Message{
			Type: protocol.MsgSetPlan,
			Plan: plan,
		}

	case "duo_escalate":
		reason, _ := params.Arguments["reason"].(string)
		msg = protocol.Message{
			Type: protocol.MsgEscalate,
			Text: reason,
			Note: reason,
		}

	default:
		return toolResult{
			Content: []toolContent{{Type: "text", Text: "Unknown tool: " + params.Name}},
			IsError: true,
		}
	}

	text, ok, err := s.client.Call(ctx, msg)
	if err != nil {
		return toolResult{
			Content: []toolContent{{Type: "text", Text: "Error executing " + params.Name + ": " + err.Error()}},
			IsError: true,
		}
	}
	return toolResult{
		Content: []toolContent{{Type: "text", Text: text}},
		IsError: !ok,
	}
}
