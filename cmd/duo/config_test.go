package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

func TestSessionID(t *testing.T) {
	t.Setenv("DUO_SESSION", "")
	first, err := loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	a, b := first.session, second.session
	if a == b || !regexp.MustCompile(`^\d{8}-\d{6}-[0-9a-f]{8}$`).MatchString(a) {
		t.Fatalf("session IDs %q %q", a, b)
	}
	t.Setenv("DUO_SESSION", "abc")
	got, err := loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.session != "abc" {
		t.Fatalf("explicit session = %q", got.session)
	}
}

func TestLoadConfigLaunchTargetPrecedence(t *testing.T) {
	root := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(root, "env")
	cli := filepath.Join(root, "cli")
	t.Setenv("DUO_REPO", env)
	cfg, err := loadConfig([]string{cli})
	if err != nil || cfg.launchDir != cli {
		t.Fatalf("CLI launch target = %q, %v", cfg.launchDir, err)
	}
	cfg, err = loadConfig(nil)
	if err != nil || cfg.launchDir != env {
		t.Fatalf("environment launch target = %q, %v", cfg.launchDir, err)
	}
	t.Setenv("DUO_REPO", "")
	cfg, err = loadConfig(nil)
	if err != nil || !workspace.SamePath(cfg.launchDir, root) {
		t.Fatalf("cwd launch target = %q, %v", cfg.launchDir, err)
	}
}

func TestSetFromLegacySnapshotUsesRootScope(t *testing.T) {
	set := setFromSnapshot(sessionstore.Snapshot{Repository: "/tmp/repo"})
	if set.ScopePath != "." {
		t.Fatalf("legacy scope = %q, want .", set.ScopePath)
	}
}

func TestParseResumeArgs(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		repo      string
		resume    bool
		sessionID string
	}{
		{name: "plain", args: nil, resume: false},
		{name: "repo only", args: []string{"/tmp/repo"}, repo: "/tmp/repo"},
		{name: "resume latest", args: []string{"--resume"}, resume: true},
		{name: "resume latest with repo", args: []string{"/tmp/repo", "--resume"}, repo: "/tmp/repo", resume: true},
		{name: "resume id", args: []string{"--resume", "20260101-000000-abcdef01"}, resume: true, sessionID: "20260101-000000-abcdef01"},
		{name: "resume equals", args: []string{"--resume=abc"}, resume: true, sessionID: "abc"},
		{name: "short resume", args: []string{"-r", "abc"}, resume: true, sessionID: "abc"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseArgs(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if got.repository != tc.repo || got.resume != tc.resume || got.sessionID != tc.sessionID {
				t.Fatalf("parseArgs(%v) = %+v, want repo=%q resume=%t sessionID=%q",
					tc.args, got, tc.repo, tc.resume, tc.sessionID)
			}
		})
	}
}

func TestParseResumeArgsRejectsUnknownFlag(t *testing.T) {
	if _, err := parseArgs([]string{"--nope"}); err == nil {
		t.Fatal("expected an error for an unknown flag")
	}
}

func TestResumeAddsHarnessGrace(t *testing.T) {
	fresh, err := loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.harness.RecoveryGrace != 0 {
		t.Fatalf("fresh session should not have a recovery grace, got %s", fresh.harness.RecoveryGrace)
	}

	resumed, err := loadConfig([]string{"--resume"})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.harness.RecoveryGrace < 30*time.Second || resumed.harness.RecoveryGrace > 60*time.Second {
		t.Fatalf("resume grace %s is outside the required 30-60s window", resumed.harness.RecoveryGrace)
	}
}

func TestPiSessionIDsAreStableAndDistinct(t *testing.T) {
	first, err := piSessionIDs(nil)
	if err != nil {
		t.Fatal(err)
	}
	austin, tony := first[protocol.Austin], first[protocol.Tony]
	if austin == "" || tony == "" {
		t.Fatalf("generated ids must not be empty: %+v", first)
	}
	if austin == tony {
		t.Fatalf("Austin and Tony must never share a Pi session id: %q", austin)
	}

	reused, err := piSessionIDs(first)
	if err != nil {
		t.Fatal(err)
	}
	if reused[protocol.Austin] != austin || reused[protocol.Tony] != tony {
		t.Fatalf("persisted ids must be reused: got %+v want %+v", reused, first)
	}

	// A duplicated pair must be repaired rather than silently shared.
	repaired, err := piSessionIDs(map[protocol.AgentID]string{
		protocol.Austin: "same",
		protocol.Tony:   "same",
	})
	if err != nil {
		t.Fatal(err)
	}
	if repaired[protocol.Austin] == repaired[protocol.Tony] {
		t.Fatalf("duplicate ids were not repaired: %+v", repaired)
	}

	// A missing side is generated without disturbing the other.
	partial, err := piSessionIDs(map[protocol.AgentID]string{protocol.Austin: "keep-me"})
	if err != nil {
		t.Fatal(err)
	}
	if partial[protocol.Austin] != "keep-me" || partial[protocol.Tony] == "" || partial[protocol.Tony] == "keep-me" {
		t.Fatalf("partial ids handled incorrectly: %+v", partial)
	}
}

func TestLoadConfigFileAndPrecedence(t *testing.T) {
	temp := t.TempDir()
	duoDir := filepath.Join(temp, ".duo")
	if err := os.MkdirAll(duoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	configJSON := `{
		"mode": "goal",
		"piCommand": "pi-custom",
		"harness": {
			"idleSeconds": 42,
			"stallSeconds": 420
		},
		"agents": {
			"austin": {
				"model": "anthropic/claude-3-7-sonnet",
				"thinking": "high"
			},
			"tony": {
				"model": "openai/o3-mini"
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(duoDir, "config.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DUO_REPO", temp)
	t.Setenv("DUO_MODE", "")
	t.Setenv("DUO_PI_COMMAND", "")

	// 1. From config file
	cfg, err := loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.mode != "goal" || cfg.modeSource != "config" {
		t.Fatalf("mode = %q (source %q), want goal (config)", cfg.mode, cfg.modeSource)
	}
	if cfg.piCommand != "pi-custom" {
		t.Fatalf("piCommand = %q, want pi-custom", cfg.piCommand)
	}
	if cfg.harness.IdleThreshold != 42*time.Second {
		t.Fatalf("idle threshold = %v, want 42s", cfg.harness.IdleThreshold)
	}
	austinCmd := cfg.agentCommand(protocol.Austin)
	if austinCmd != "pi-custom --model anthropic/claude-3-7-sonnet --thinking high" {
		t.Fatalf("austin command = %q", austinCmd)
	}
	tonyCmd := cfg.agentCommand(protocol.Tony)
	if tonyCmd != "pi-custom --model openai/o3-mini" {
		t.Fatalf("tony command = %q", tonyCmd)
	}

	// 2. Env var overrides config file
	t.Setenv("DUO_MODE", "fast")
	cfg, err = loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.mode != "fast" || cfg.modeSource != "env" {
		t.Fatalf("mode = %q (source %q), want fast (env)", cfg.mode, cfg.modeSource)
	}

	// 3. CLI flag overrides env var and config file
	cfg, err = loadConfig([]string{"--mode", "goal"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.mode != "goal" || cfg.modeSource != "cli" {
		t.Fatalf("mode = %q (source %q), want goal (cli)", cfg.mode, cfg.modeSource)
	}
}

func TestAgentCommandDoesNotDuplicateFlags(t *testing.T) {
	temp := t.TempDir()
	configJSON := `{
		"agents": {
			"austin": {
				"model": "claude-3-7-sonnet",
				"thinking": "high"
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(temp, ".duo.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DUO_REPO", temp)
	t.Setenv("DUO_PI_COMMAND", "pi --model custom-model")
	cfg, err := loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}

	// Austin should NOT append --model because DUO_PI_COMMAND already has --model, but SHOULD append --thinking
	austinCmd := cfg.agentCommand(protocol.Austin)
	if austinCmd != "pi --model custom-model --thinking high" {
		t.Fatalf("austin command = %q", austinCmd)
	}
}
