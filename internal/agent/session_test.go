package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	if !strings.Contains(cmd1, "gemini-3.8-flash-low") {
		t.Fatalf("commandLine() expected the model name, got: %s", cmd1)
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
	if !strings.Contains(cmd2, "gemini-3.8-flash-low") {
		t.Fatalf("commandLine() expected the model name, got: %s", cmd2)
	}

	// Case 3: SetModel updates command line even if initial command had --model
	s2.SetModel("claude-sonnet-4-6")
	cmd3 := s2.commandLine()
	if strings.Contains(cmd3, "gemini-3.8-flash-low") {
		t.Fatalf("commandLine() should replace old model, got: %s", cmd3)
	}
	if !strings.Contains(cmd3, "claude-sonnet-4-6") {
		t.Fatalf("commandLine() expected updated model, got: %s", cmd3)
	}
}

// The agy watcher writes cfg.AgyConversationID and the TUI writes the model from
// other goroutines, while composeSnapshot reads both back on the transport
// goroutines to persist the session. Those reads were unsynchronized, so the
// race detector flagged them. Run with -race to see this fail without the fix.
func TestSessionIdentityAndCommandAreSafeUnderConcurrentWrites(t *testing.T) {
	for _, tc := range []struct {
		name       string
		driverType string
	}{
		{"agy", "agy"},
		{"opencode", "opencode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSession(Config{Agent: protocol.Austin, DriverType: tc.driverType, Session: "s1"})
			// A model is always selected, so --model is always part of the
			// command and a missing one means the read lost the write.
			s.SetModel("provider/initial-model")

			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 2000; i++ {
					// What SetModel and the agy watcher do.
					s.SetModel("provider/model-name")
					s.SetEffort("high")
					s.mu.Lock()
					s.cfg.AgyConversationID = "11111111-2222-3333-4444-555555555555"
					s.mu.Unlock()
				}
			}()
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 2000; i++ {
					// What composeSnapshot and the startup diagnostics do. An
					// opencode identity is legitimately empty until the plugin
					// reports one, so only agy must always resolve.
					if id := s.SessionID(); id == "" && tc.driverType == "agy" {
						t.Error("SessionID returned an empty identity")
						return
					}
					if cmd := s.EffectiveCommand(); !strings.Contains(cmd, "--model") {
						t.Errorf("EffectiveCommand lost the model: %q", cmd)
						return
					}
				}
			}()
			wg.Wait()
		})
	}
}

// TestDriverIsClassifiedByExecutableNotByAFlagValue pins the failure that made a
// pi agent refuse to start. A model id persisted for opencode was appended to
// pi's command line, and the old substring check read the word "opencode" out
// of that flag value, so Duo built an *opencode* command for a pi process and
// added --auto, which pi rejects:
//
//	Error: Unknown option: --auto
//
// The agent then died before connecting and the only trace was "exit status 1".
func TestDriverIsClassifiedByExecutableNotByAFlagValue(t *testing.T) {
	// The command as config.go built it: pi, carrying a model id persisted for
	// opencode. --auto is not in the input; the old check classified this as
	// opencode and appended it, which is what the session log recorded.
	s := NewSession(Config{
		Agent:   protocol.Tony,
		Command: `pi --model opencode/space-bunny-free`,
		Model:   "opencode/space-bunny-free",
	})
	if s.isOpencode() {
		t.Error("a pi command carrying an opencode model id was classified as opencode")
	}
	if s.isAgy() {
		t.Error("a pi command was classified as agy")
	}
	if got := s.commandLine(); strings.Contains(got, "--auto") {
		t.Errorf("pi command line gained opencode's --auto: %s", got)
	}

	// The wrappers Duo ships must still be recognised, by executable name.
	for command, want := range map[string]string{
		"opencode":                 "opencode",
		"opencode --auto":          "opencode",
		"duo-opencode --session x": "opencode",
		"agy":                      "agy",
		"duo-agy":                  "agy",
		"pi":                       "",
		"pi --model opencode/x":    "",
	} {
		got := NewSession(Config{Agent: protocol.Tony, Command: command})
		switch want {
		case "opencode":
			if !got.isOpencode() {
				t.Errorf("%q should be opencode", command)
			}
		case "agy":
			if !got.isAgy() {
				t.Errorf("%q should be agy", command)
			}
		default:
			if got.isOpencode() || got.isAgy() {
				t.Errorf("%q should be pi, got opencode=%v agy=%v", command, got.isOpencode(), got.isAgy())
			}
		}
	}
}
