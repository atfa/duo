package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/atfa/duo/internal/mcp"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/transport"
)

func TestParseMCPServerArgs(t *testing.T) {
	args := []string{"-a=tony", "-s=sess-123", "-k=tok-456", "-H=127.0.0.1", "-p=8765"}

	parsed, err := parseMCPServerArgs(args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if parsed.cfg.Agent != protocol.Tony {
		t.Errorf("got agent %q, want tony", parsed.cfg.Agent)
	}
	if parsed.cfg.SessionID != "sess-123" {
		t.Errorf("got session %q, want sess-123", parsed.cfg.SessionID)
	}
	if parsed.cfg.Token != "tok-456" {
		t.Errorf("got token %q, want tok-456", parsed.cfg.Token)
	}
	if parsed.cfg.Host != "127.0.0.1" {
		t.Errorf("got host %q, want 127.0.0.1", parsed.cfg.Host)
	}
	if parsed.cfg.Port != "8765" {
		t.Errorf("got port %q, want 8765", parsed.cfg.Port)
	}
}

func TestExportMCPConfig(t *testing.T) {
	parsed, err := parseMCPServerArgs([]string{
		"config",
		"--agent", "austin",
		"--session", "s-1",
		"--token", "t-1",
		"--port", "9000",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.exportJSON {
		t.Fatal("expected exportJSON=true")
	}

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err = exportMCPConfig(parsed.cfg)
	w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("exportMCPConfig err: %v", err)
	}

	var buf bytes.Buffer
	io.Copy(&buf, r)

	var conf map[string]any
	if err := json.Unmarshal(buf.Bytes(), &conf); err != nil {
		t.Fatalf("invalid json output: %v, raw: %s", err, buf.String())
	}

	servers, ok := conf["mcpServers"].(map[string]any)
	if !ok {
		t.Fatal("mcpServers not found in json")
	}
	duoServer, ok := servers["duo"].(map[string]any)
	if !ok {
		t.Fatal("duo server config not found")
	}
	argsList, ok := duoServer["args"].([]any)
	if !ok || len(argsList) < 2 {
		t.Fatalf("args list invalid: %#v", argsList)
	}
	if argsList[0] != "mcp-server" {
		t.Errorf("expected first arg to be mcp-server, got %v", argsList[0])
	}
}

func TestMCPConfigIsACommand(t *testing.T) {
	parsed, err := parseMCPServerArgs([]string{"config", "-p=9000"})
	if err != nil || !parsed.exportJSON || parsed.cfg.Port != "9000" {
		t.Fatalf("config command parsed as %+v, %v", parsed, err)
	}
	for _, args := range [][]string{{"--export-config"}, {"-e"}, {"--export"}, {"--config"}} {
		if _, err := parseMCPServerArgs(args); err == nil {
			t.Errorf("removed output flag accepted: %v", args)
		}
	}
}

// mcpTestCore stands in for Duo Core: it records the exact message an MCP tool
// call produced and answers with a verdict the test supplies, so a test pins the
// wire fields and how a refusal travels back to the agent.
type mcpTestCore struct {
	mu     sync.Mutex
	last   protocol.Message
	answer func(protocol.Message) (bool, string)
}

func (h *mcpTestCore) OnConnect(context.Context, *transport.Client) {}
func (h *mcpTestCore) OnDisconnect(*transport.Client)               {}

func (h *mcpTestCore) OnMessage(ctx context.Context, client *transport.Client, message protocol.Message) {
	if message.Type == protocol.MsgHello {
		return
	}
	h.mu.Lock()
	h.last = message
	answer := h.answer
	h.mu.Unlock()

	ok, text := true, "ok"
	if answer != nil {
		ok, text = answer(message)
	}
	if message.RequestID != "" {
		_ = client.Send(ctx, protocol.Message{
			Version:   protocol.Version,
			Type:      protocol.MsgResponse,
			RequestID: message.RequestID,
			OK:        ok,
			Text:      text,
		})
	}
}

func (h *mcpTestCore) received() protocol.Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.last
}

// startMCPBridge wires the real MCP server over a real bridge connection to the
// recording core, which is the path an MCP-only driver such as codex takes.
func startMCPBridge(t *testing.T, answer func(protocol.Message) (bool, string)) (*mcp.Server, *mcpTestCore) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	core := &mcpTestCore{answer: answer}
	server := transport.NewServer("127.0.0.1:0", "test-session", "test-token")
	server.SetHandler(core)
	go func() { _ = server.ListenAndServe(ctx) }()
	<-server.Ready()

	host, port, err := net.SplitHostPort(server.Addr())
	if err != nil {
		t.Fatal(err)
	}
	client, err := mcp.ConnectBridge(ctx, mcp.Config{
		Agent:     protocol.Austin,
		SessionID: "test-session",
		Token:     "test-token",
		Host:      host,
		Port:      port,
	})
	if err != nil {
		t.Fatalf("connect bridge: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return mcp.NewServer(client), core
}

type mcpToolCall struct {
	Result struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c mcpToolCall) text() string {
	if len(c.Result.Content) == 0 {
		return ""
	}
	return c.Result.Content[0].Text
}

func callMCPTool(t *testing.T, server *mcp.Server, tool string, args map[string]any) mcpToolCall {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": tool, "arguments": args},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := server.Serve(strings.NewReader(string(payload)+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var call mcpToolCall
	if err := json.Unmarshal(out.Bytes(), &call); err != nil {
		t.Fatalf("unmarshal %s response: %v (%s)", tool, err, out.String())
	}
	if call.Error != nil {
		t.Fatalf("%s returned a jsonrpc error: %s", tool, call.Error.Message)
	}
	return call
}

// TestMCPSetStepsPublishesQueue is the regression for the STEP-mode deadlock: an
// MCP-only driver (codex declares MCP with no bridge) had no duo_set_steps, so
// PLAN could never publish the queue the mode requires. The call must reach Duo
// Core with the raw markdown in Steps and the append marker in Text, exactly as
// the pi bridge sends it, because the queue grammar lives in the Go runtime.
func TestMCPSetStepsPublishesQueue(t *testing.T) {
	const queue = "- [ ] [fast] add ModeStep\n- [ ] [goal] redesign the scheduler"
	server, core := startMCPBridge(t, func(protocol.Message) (bool, string) {
		return true, "Step queue set (2 step(s), cursor at 1)"
	})

	call := callMCPTool(t, server, "duo_set_steps", map[string]any{"steps": "  " + queue + "  ", "append": true})
	if call.Result.IsError {
		t.Fatalf("duo_set_steps failed: %s", call.text())
	}
	got := core.received()
	if got.Type != protocol.MsgSetSteps {
		t.Fatalf("message type = %q, want %q", got.Type, protocol.MsgSetSteps)
	}
	if got.Steps != queue {
		t.Errorf("Steps = %q, want the trimmed markdown queue %q", got.Steps, queue)
	}
	if got.Text != "append" {
		t.Errorf("Text = %q, want the append marker the coordinator reads", got.Text)
	}
	if !strings.Contains(call.text(), "Step queue set") {
		t.Errorf("the coordinator reply must reach the agent, got %q", call.text())
	}

	// Without append the marker stays empty, so the coordinator replaces the queue.
	callMCPTool(t, server, "duo_set_steps", map[string]any{"steps": queue})
	if got := core.received(); got.Text != "" {
		t.Errorf("Text = %q without append=true, want empty", got.Text)
	}

	// A blank queue is refused at the bridge: Duo Core never sees a queue it must reject.
	if blank := callMCPTool(t, server, "duo_set_steps", map[string]any{"steps": "   "}); !blank.Result.IsError {
		t.Error("blank steps must be an error")
	}
}

// TestMCPRestartRefusesDirtyWorktree pins the MCP wiring of duo_restart_duo: the
// reason must arrive as Note and install as Install, and a refusal must come back
// as an error the agent can read. The guard itself — a dirty worktree stops a
// restart so a killed agent loses nothing — is pinned by the coordinator's
// TestStepRestartRefusesDirtyWorktrees.
func TestMCPRestartRefusesDirtyWorktree(t *testing.T) {
	const refusal = "refusing to restart: the austin (/tmp/wt/austin) worktree has uncommitted changes; commit or discard them first so the queue resumes without losing work"
	server, core := startMCPBridge(t, func(protocol.Message) (bool, string) { return false, refusal })

	call := callMCPTool(t, server, "duo_restart_duo", map[string]any{
		"reason":  "  the step landed a change to Duo itself  ",
		"install": true,
	})
	if !call.Result.IsError {
		t.Fatalf("a refused restart must be an error, got %q", call.text())
	}
	if !strings.Contains(call.text(), "uncommitted changes") {
		t.Errorf("the coordinator refusal must reach the agent, got %q", call.text())
	}
	got := core.received()
	if got.Type != protocol.MsgRestartDuo {
		t.Fatalf("message type = %q, want %q", got.Type, protocol.MsgRestartDuo)
	}
	if got.Note != "the step landed a change to Duo itself" {
		t.Errorf("Note = %q, want the trimmed reason", got.Note)
	}
	if !got.Install {
		t.Error("Install = false, want the install flag passed through")
	}

	if blank := callMCPTool(t, server, "duo_restart_duo", map[string]any{"reason": "  "}); !blank.Result.IsError {
		t.Error("a restart without a reason must be an error")
	}
}

// TestMCPToolsListMatchesTheBridge keeps the MCP subset honest about the eight
// coordination tools the bridges document: a tool Duo Core can handle must be
// advertised, and every advertised tool must be executable.
func TestMCPToolsListMatchesTheBridge(t *testing.T) {
	server, _ := startMCPBridge(t, nil)

	out := runMCPRequest(t, server, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var listed struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		t.Fatal(err)
	}
	want := []string{"duo_send", "duo_status", "duo_set_status", "duo_set_verification", "duo_set_plan", "duo_set_steps", "duo_escalate", "duo_restart_duo"}
	if len(listed.Result.Tools) != len(want) {
		t.Fatalf("tools/list returned %d tools, want %d", len(listed.Result.Tools), len(want))
	}
	got := map[string]bool{}
	for _, tool := range listed.Result.Tools {
		got[tool.Name] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("tools/list is missing %s", name)
		}
	}
}

func runMCPRequest(t *testing.T, server *mcp.Server, request string) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := server.Serve(strings.NewReader(request+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
