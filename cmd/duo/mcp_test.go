package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/atfa/duo/internal/protocol"
)

func TestParseMCPServerArgs(t *testing.T) {
	args := []string{
		"--agent", "tony",
		"--session", "sess-123",
		"--token", "tok-456",
		"--host", "127.0.0.1",
		"--port", "8765",
	}

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
		"--agent", "austin",
		"--session", "s-1",
		"--token", "t-1",
		"--port", "9000",
		"--export-config",
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
