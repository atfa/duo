package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/protocol"
)

func TestSessionRunsInsidePTY(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s := NewSession(Config{
		Agent: protocol.Austin, Dir: t.TempDir(), Host: "127.0.0.1", Port: "1",
		Session: "session-1", Token: "secret",
		Command: `printf 'duo-pty-ok:%s:%s:%s\n' "$DUO_ACTIVE" "$DUO_SESSION" "$DUO_TOKEN"`,
	})
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.stopped:
	case <-ctx.Done():
		t.Fatal("session did not exit")
	}
	if s.Running() {
		t.Fatal("session still reported running after normal exit")
	}
	s.mu.RLock()
	got := append([]byte(nil), s.recent...)
	s.mu.RUnlock()
	if !bytes.Contains(got, []byte("duo-pty-ok:1:session-1:secret")) {
		t.Fatalf("PTY output missing marker: %q", strings.TrimSpace(string(got)))
	}
}

func TestEnsureAgyWorkspaceTrusted(t *testing.T) {
	tmpAppDir := t.TempDir()
	t.Setenv("GEMINI_APP_DATA_DIR", tmpAppDir)

	// Pre-populate settings.json with some existing custom settings
	initial := map[string]any{
		"model": "test-model",
		"trustedWorkspaces": []any{
			"/existing/trusted/dir",
		},
	}
	data, _ := json.Marshal(initial)
	settingsFile := filepath.Join(tmpAppDir, "settings.json")
	if err := os.WriteFile(settingsFile, data, 0644); err != nil {
		t.Fatal(err)
	}

	testPath1 := filepath.Join(t.TempDir(), "project-a")
	testPath2 := filepath.Join(t.TempDir(), "project-b")
	if err := os.MkdirAll(testPath1, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(testPath2, 0755); err != nil {
		t.Fatal(err)
	}

	// 1. Ensure first set of paths
	if err := EnsureAgyWorkspaceTrusted(testPath1, "/existing/trusted/dir"); err != nil {
		t.Fatalf("EnsureAgyWorkspaceTrusted failed: %v", err)
	}

	readSettings := func() map[string]any {
		raw, err := os.ReadFile(settingsFile)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	m1 := readSettings()
	if m1["model"] != "test-model" {
		t.Errorf("expected existing setting 'model' to be preserved, got %v", m1["model"])
	}

	trusted1, _ := m1["trustedWorkspaces"].([]any)
	hasPath1 := false
	hasExisting := false
	for _, item := range trusted1 {
		if item == testPath1 {
			hasPath1 = true
		}
		if item == "/existing/trusted/dir" {
			hasExisting = true
		}
	}
	if !hasPath1 || !hasExisting {
		t.Errorf("trustedWorkspaces missing expected path: %#v", trusted1)
	}

	// 2. Ensure second set with overlap, check no duplicates
	if err := EnsureAgyWorkspaceTrusted(testPath1, testPath2); err != nil {
		t.Fatalf("second EnsureAgyWorkspaceTrusted failed: %v", err)
	}

	m2 := readSettings()
	trusted2, _ := m2["trustedWorkspaces"].([]any)
	var countPath1 int
	hasPath2 := false
	for _, item := range trusted2 {
		if item == testPath1 {
			countPath1++
		}
		if item == testPath2 {
			hasPath2 = true
		}
	}
	if countPath1 != 1 {
		t.Errorf("expected testPath1 exactly once, got count %d: %#v", countPath1, trusted2)
	}
	if !hasPath2 {
		t.Errorf("expected testPath2 in trustedWorkspaces: %#v", trusted2)
	}
}

func TestAgyCommandLineModelSanitization(t *testing.T) {
	// Case 1: Model has provider prefix in Config.Model
	s1 := NewAgySession(Config{
		Agent:   protocol.Austin,
		Command: "agy",
		Model:   "google/gemini-3.8-flash-low",
	})
	cmd1 := s1.commandLine()
	if strings.Contains(cmd1, "google/") {
		t.Fatalf("commandLine() should not contain 'google/', got: %s", cmd1)
	}
	if !strings.Contains(cmd1, `--model "gemini-3.8-flash-low"`) {
		t.Fatalf("commandLine() expected --model \"gemini-3.8-flash-low\", got: %s", cmd1)
	}

	// Case 2: Command already has --model with provider prefix
	s2 := NewAgySession(Config{
		Agent:   protocol.Austin,
		Command: "agy --model google/gemini-3.8-flash-low",
	})
	cmd2 := s2.commandLine()
	if strings.Contains(cmd2, "google/") {
		t.Fatalf("commandLine() should sanitize provider from command, got: %s", cmd2)
	}
	if !strings.Contains(cmd2, `--model "gemini-3.8-flash-low"`) {
		t.Fatalf("commandLine() expected --model \"gemini-3.8-flash-low\", got: %s", cmd2)
	}

	// Case 3: SetModel updates command line even if initial command had --model
	s2.SetModel("claude-sonnet-4-6")
	cmd3 := s2.commandLine()
	if strings.Contains(cmd3, "gemini-3.8-flash-low") {
		t.Fatalf("commandLine() should replace old model, got: %s", cmd3)
	}
	if !strings.Contains(cmd3, `--model "claude-sonnet-4-6"`) {
		t.Fatalf("commandLine() expected updated model, got: %s", cmd3)
	}
}
