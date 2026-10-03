package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/transport"
)

func TestMCPServerInitializeAndToolsList(t *testing.T) {
	server := NewServer(nil)

	// 1. initialize request
	initReq := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}` + "\n"
	var out bytes.Buffer
	if err := server.Serve(strings.NewReader(initReq), &out); err != nil {
		t.Fatal(err)
	}

	var resp jsonRPCResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal init resp: %v, raw: %s", err, out.String())
	}
	if resp.JSONRPC != "2.0" {
		t.Fatalf("jsonrpc = %q, want 2.0", resp.JSONRPC)
	}
	resMap, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result is not map: %#v", resp.Result)
	}
	serverInfo, ok := resMap["serverInfo"].(map[string]any)
	if !ok || serverInfo["name"] != "duo-mcp-server" {
		t.Fatalf("serverInfo = %#v, want duo-mcp-server", serverInfo)
	}

	// 2. tools/list request
	toolsReq := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"
	out.Reset()
	if err := server.Serve(strings.NewReader(toolsReq), &out); err != nil {
		t.Fatal(err)
	}

	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal tools resp: %v", err)
	}
	toolsMap, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("tools result not map: %#v", resp.Result)
	}
	toolsList, ok := toolsMap["tools"].([]any)
	if !ok || len(toolsList) != 6 {
		t.Fatalf("tools count = %d, want 6", len(toolsList))
	}

	// Verify all 6 Duo tools are listed
	expectedTools := map[string]bool{
		"duo_status":           false,
		"duo_send":             false,
		"duo_set_status":       false,
		"duo_set_verification": false,
		"duo_set_plan":         false,
		"duo_escalate":         false,
	}
	for _, item := range toolsList {
		tm := item.(map[string]any)
		name := tm["name"].(string)
		if _, exists := expectedTools[name]; exists {
			expectedTools[name] = true
		}
	}
	for name, found := range expectedTools {
		if !found {
			t.Errorf("tool %s was not found in tools/list", name)
		}
	}

	// 3. Unknown method
	unknownReq := `{"jsonrpc":"2.0","id":3,"method":"unknown/method"}` + "\n"
	out.Reset()
	if err := server.Serve(strings.NewReader(unknownReq), &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil {
		t.Fatal("expected error for unknown method, got nil")
	}
}

func TestMCPToolCallValidation(t *testing.T) {
	server := NewServer(nil) // nil client tests validation without connection

	// 1. Missing required param for duo_send
	callReq := `{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"duo_send","arguments":{}}}` + "\n"
	var out bytes.Buffer
	if err := server.Serve(strings.NewReader(callReq), &out); err != nil {
		t.Fatal(err)
	}

	var resp jsonRPCResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	resMap, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result not map: %#v", resp.Result)
	}
	if isErr, _ := resMap["isError"].(bool); !isErr {
		t.Fatal("expected isError=true for missing message param")
	}

	// 2. Unknown tool name
	callReq2 := `{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"non_existent_tool","arguments":{}}}` + "\n"
	out.Reset()
	if err := server.Serve(strings.NewReader(callReq2), &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	resMap2 := resp.Result.(map[string]any)
	if isErr, _ := resMap2["isError"].(bool); !isErr {
		t.Fatal("expected isError=true for unknown tool")
	}
}

type mockTransportHandler struct {
	lastMsg protocol.Message
}

func (h *mockTransportHandler) OnConnect(_ context.Context, _ *transport.Client) {}
func (h *mockTransportHandler) OnDisconnect(_ *transport.Client)                 {}
func (h *mockTransportHandler) OnMessage(ctx context.Context, client *transport.Client, msg protocol.Message) {
	h.lastMsg = msg
	if msg.RequestID != "" {
		_ = client.Send(ctx, protocol.Message{
			Version:   protocol.Version,
			Type:      protocol.MsgResponse,
			RequestID: msg.RequestID,
			OK:        true,
			Text:      "received " + string(msg.Type),
			State:     "Phase: PLAN",
		})
	}
}

func TestMCPEndToEndWithBridge(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handler := &mockTransportHandler{}
	s := transport.NewServer("127.0.0.1:0", "test-session", "test-token")
	s.SetHandler(handler)

	go func() { _ = s.ListenAndServe(ctx) }()
	<-s.Ready()

	host, port, err := net.SplitHostPort(s.Addr())
	if err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Agent:     protocol.Austin,
		SessionID: "test-session",
		Token:     "test-token",
		Host:      host,
		Port:      port,
	}

	client, err := ConnectBridge(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to connect bridge: %v", err)
	}
	defer client.Close()

	mcpServer := NewServer(client)

	// Call duo_status
	callReq := `{"jsonrpc":"2.0","id":100,"method":"tools/call","params":{"name":"duo_status","arguments":{}}}` + "\n"
	var out bytes.Buffer
	if err := mcpServer.Serve(strings.NewReader(callReq), &out); err != nil {
		t.Fatal(err)
	}

	var resp jsonRPCResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal resp: %v, raw: %s", err, out.String())
	}
	if resp.Error != nil {
		t.Fatalf("unexpected jsonrpc error: %#v", resp.Error)
	}
	resMap, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result not map: %#v", resp.Result)
	}
	if isErr, _ := resMap["isError"].(bool); isErr {
		t.Fatalf("expected isError=false, got true: %#v", resMap)
	}
	contentList, ok := resMap["content"].([]any)
	if !ok || len(contentList) == 0 {
		t.Fatalf("content is empty: %#v", resMap)
	}
	item := contentList[0].(map[string]any)
	text := item["text"].(string)
	if !strings.Contains(text, "received get_status") || !strings.Contains(text, "Phase: PLAN") {
		t.Fatalf("unexpected content text: %s", text)
	}

	// Call duo_send
	callSend := `{"jsonrpc":"2.0","id":101,"method":"tools/call","params":{"name":"duo_send","arguments":{"message":"hello peer"}}}` + "\n"
	out.Reset()
	if err := mcpServer.Serve(strings.NewReader(callSend), &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if handler.lastMsg.Type != protocol.MsgPeerMessage || handler.lastMsg.Text != "hello peer" {
		t.Fatalf("expected MsgPeerMessage with 'hello peer', got: %#v", handler.lastMsg)
	}
}
