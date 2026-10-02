package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
		{name: "resume subcommand latest", args: []string{"resume"}, resume: true},
		{name: "resume subcommand with id", args: []string{"resume", "20260101-000000-abcdef01"}, resume: true, sessionID: "20260101-000000-abcdef01"},
		{name: "resume subcommand with repo and id", args: []string{"/tmp/repo", "resume", "abc"}, repo: "/tmp/repo", resume: true, sessionID: "abc"},
		{name: "resume subcommand followed by repo", args: []string{"resume", "/tmp/repo"}, repo: "/tmp/repo", resume: true},
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
	t.Setenv("DUO_DRIVER", "")

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
	t.Setenv("DUO_DRIVER", "")
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

func TestAgyDriverUsesAgyExecutableWithPiCommandConfigured(t *testing.T) {
	temp := t.TempDir()
	configJSON := `{
		"driver": "agy",
		"piCommand": "pi-custom",
		"agents": {
			"austin": {
				"model": "google/gemini-3.8-flash-high"
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(temp, ".duo.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DUO_REPO", temp)
	t.Setenv("DUO_DRIVER", "")
	t.Setenv("DUO_PI_COMMAND", "")

	cfg, err := loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	austinCmd := cfg.agentCommand(protocol.Austin)
	if !strings.HasPrefix(austinCmd, "agy ") {
		t.Fatalf("expected austinCmd to start with agy, got: %q", austinCmd)
	}
	if !strings.Contains(austinCmd, "--model gemini-3.8-flash-high") {
		t.Fatalf("expected sanitized model in austinCmd, got: %q", austinCmd)
	}
}

// The opencode driver must resolve to the opencode executable, not the pi
// command, and must keep the provider-qualified model id opencode expects.
func TestOpencodeDriverUsesOpencodeExecutable(t *testing.T) {
	temp := t.TempDir()
	configJSON := `{
		"driver": "opencode",
		"piCommand": "pi-custom",
		"agents": {
			"austin": { "model": "opencode/claude-sonnet-4-6" }
		}
	}`
	if err := os.WriteFile(filepath.Join(temp, ".duo.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DUO_REPO", temp)
	t.Setenv("DUO_DRIVER", "")
	t.Setenv("DUO_PI_COMMAND", "")

	cfg, err := loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.agentDriver(protocol.Austin); got != "opencode" {
		t.Fatalf("agentDriver(Austin) = %q, want opencode", got)
	}
	cmd := cfg.agentCommand(protocol.Austin)
	if !strings.HasPrefix(cmd, "opencode") {
		t.Fatalf("expected the opencode executable, got: %q", cmd)
	}
	if strings.Contains(cmd, "pi-custom") {
		t.Fatalf("opencode driver must not inherit the pi command: %q", cmd)
	}
	// opencode model ids are provider-qualified and must not be stripped.
	if !strings.Contains(cmd, "--model opencode/claude-sonnet-4-6") {
		t.Fatalf("expected the provider-qualified model, got: %q", cmd)
	}
	if strings.Contains(cfg.agentModel(protocol.Austin), "pi") {
		t.Fatalf("model for opencode should not fall back to pi: %q", cfg.agentModel(protocol.Austin))
	}
}

// Reasoning effort is --variant on opencode, not --thinking.
func TestOpencodeThinkingUsesVariantFlag(t *testing.T) {
	temp := t.TempDir()
	configJSON := `{
		"driver": "opencode",
		"agents": { "austin": { "thinking": "high" } }
	}`
	if err := os.WriteFile(filepath.Join(temp, ".duo.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DUO_REPO", temp)
	t.Setenv("DUO_DRIVER", "")

	cfg, err := loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	cmd := cfg.agentCommand(protocol.Austin)
	if !strings.Contains(cmd, "--variant high") {
		t.Fatalf("expected --variant high, got: %q", cmd)
	}
	if strings.Contains(cmd, "--thinking") {
		t.Fatalf("opencode does not accept --thinking: %q", cmd)
	}
}

func TestTestCommandConfigurationPrecedence(t *testing.T) {
	temp := t.TempDir()
	configJSON := `{"testCommand": "make test"}`
	if err := os.WriteFile(filepath.Join(temp, ".duo.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DUO_REPO", temp)
	t.Setenv("DUO_TEST_COMMAND", "")

	// 1. Config file
	cfg, err := loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.testCommand != "make test" {
		t.Fatalf("testCommand from file = %q, want 'make test'", cfg.testCommand)
	}

	// 2. Env var overrides config file
	t.Setenv("DUO_TEST_COMMAND", "go test ./...")
	cfg, err = loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.testCommand != "go test ./..." {
		t.Fatalf("testCommand from env = %q, want 'go test ./...'", cfg.testCommand)
	}

	// 3. CLI flag overrides both
	cfg, err = loadConfig([]string{"--test-cmd", "pytest"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.testCommand != "pytest" {
		t.Fatalf("testCommand from CLI = %q, want 'pytest'", cfg.testCommand)
	}

	// 4. CLI flag with equals syntax
	cfg, err = loadConfig([]string{"--test-cmd=cargo test"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.testCommand != "cargo test" {
		t.Fatalf("testCommand from CLI (=) = %q, want 'cargo test'", cfg.testCommand)
	}
}

func TestDriverConfigurationResolution(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("DUO_REPO", temp)
	t.Setenv("DUO_DRIVER", "")

	// 1. Defaults to pi
	cfg, err := loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := cfg.agentDriver(protocol.Austin); d != "pi" {
		t.Fatalf("austin default driver = %q, want pi", d)
	}
	if d := cfg.agentDriver(protocol.Tony); d != "pi" {
		t.Fatalf("tony default driver = %q, want pi", d)
	}

	// 2. Global --driver / --agent flag
	cfg, err = loadConfig([]string{"--driver", "agy"})
	if err != nil {
		t.Fatal(err)
	}
	if d := cfg.agentDriver(protocol.Austin); d != "agy" {
		t.Fatalf("austin driver with --driver agy = %q, want agy", d)
	}
	if d := cfg.agentDriver(protocol.Tony); d != "agy" {
		t.Fatalf("tony driver with --driver agy = %q, want agy", d)
	}

	// 3. Heterogeneous pairing via CLI flags
	cfg, err = loadConfig([]string{"--austin-driver", "agy", "--tony-driver", "pi"})
	if err != nil {
		t.Fatal(err)
	}
	if d := cfg.agentDriver(protocol.Austin); d != "agy" {
		t.Fatalf("austin driver = %q, want agy", d)
	}
	if d := cfg.agentDriver(protocol.Tony); d != "pi" {
		t.Fatalf("tony driver = %q, want pi", d)
	}

	// 4. Inferred driver when command is agy
	cfg, err = loadConfig([]string{"--austin-agent=agy"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd := cfg.agentCommand(protocol.Austin); cmd != "agy" {
		t.Fatalf("austin command for agy driver = %q, want agy", cmd)
	}
}

// The shipped duo-agy and duo-opencode bridges wrap their CLI, so an agent
// command is never spelled "agy" or "opencode". A wrapper must still be
// recognized as its driver instead of being recorded as pi, which handed an
// agy agent pi's default model.
func TestBridgeWrapperCommandsIdentifyTheDriver(t *testing.T) {
	load := func(t *testing.T, command string) config {
		t.Helper()
		temp := t.TempDir()
		configJSON := `{"piCommand": "pi-custom", "agents": {"austin": {"command": "` + command + `"}}}`
		if err := os.WriteFile(filepath.Join(temp, ".duo.json"), []byte(configJSON), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("DUO_REPO", temp)
		t.Setenv("DUO_DRIVER", "")
		t.Setenv("DUO_PI_COMMAND", "")
		cfg, err := loadConfig(nil)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	agyCfg := load(t, "duo-agy")
	if got := agyCfg.agentDriver(protocol.Austin); got != "agy" {
		t.Fatalf("austin driver for the duo-agy bridge = %q, want agy", got)
	}
	if got := agyCfg.agentModel(protocol.Austin); got != "gemini-3.8-flash-high" {
		t.Fatalf("austin model for the duo-agy bridge = %q, want the agy default", got)
	}
	if got := agyCfg.agentCommand(protocol.Austin); !strings.HasPrefix(got, "duo-agy") {
		t.Fatalf("austin command = %q, want the configured bridge", got)
	}

	if got := load(t, "duo-opencode").agentDriver(protocol.Austin); got != "opencode" {
		t.Fatalf("austin driver for the duo-opencode bridge = %q, want opencode", got)
	}
}

func TestDriverAndModelProjectConfigAndDefault(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("DUO_DRIVER", "")

	// 1. Default models when no config exists
	cfg, err := loadConfig([]string{"--driver", "agy", tempDir})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.agentModel(protocol.Austin); got != "gemini-3.8-flash-high" {
		t.Fatalf("austin agy default model = %q, want gemini-3.8-flash-high", got)
	}

	// 2. Project config with .duo/config.json
	duoDir := filepath.Join(tempDir, ".duo")
	if err := os.MkdirAll(duoDir, 0755); err != nil {
		t.Fatal(err)
	}
	cfgJSON := `{
		"driver": "agy",
		"agents": {
			"austin": {
				"model": "google/gemini-3.7-flash-high"
			},
			"tony": {
				"model": "google/gemini-3.8-flash-medium"
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(duoDir, "config.json"), []byte(cfgJSON), 0644); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadMergedConfigFile(tempDir)
	if err != nil {
		t.Fatalf("loadMergedConfigFile failed: %v", err)
	}
	if loaded.Driver != "agy" {
		t.Fatalf("loaded.Driver = %q, want agy", loaded.Driver)
	}
	if loaded.Agents["austin"].Model != "google/gemini-3.7-flash-high" {
		t.Fatalf("austin model = %q, want google/gemini-3.7-flash-high", loaded.Agents["austin"].Model)
	}

	// 3. loadConfig picks up the project config and sanitizes agy models (stripping google/ prefix)
	projCfg, err := loadConfig([]string{tempDir})
	if err != nil {
		t.Fatal(err)
	}
	if projCfg.agentDriver(protocol.Austin) != "agy" {
		t.Fatalf("austin driver = %q, want agy", projCfg.agentDriver(protocol.Austin))
	}
	if projCfg.agentModel(protocol.Austin) != "gemini-3.7-flash-high" {
		t.Fatalf("austin model = %q, want gemini-3.7-flash-high", projCfg.agentModel(protocol.Austin))
	}
	if !strings.Contains(projCfg.agentCommand(protocol.Austin), "--model gemini-3.7-flash-high") {
		t.Fatalf("austin command = %q, want --model gemini-3.7-flash-high", projCfg.agentCommand(protocol.Austin))
	}
}

// opencode only accepts provider/model. A bare id persisted by another driver
// must not be injected, or the agent exits at startup and never connects.
func TestOpencodeIgnoresBareModelFromAnotherDriver(t *testing.T) {
	temp := t.TempDir()
	configJSON := `{
		"driver": "opencode",
		"agents": { "austin": { "model": "gemini-3.8-flash-high" } }
	}`
	if err := os.WriteFile(filepath.Join(temp, ".duo.json"), []byte(configJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DUO_REPO", temp)
	t.Setenv("DUO_DRIVER", "")

	cfg, err := loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	cmd := cfg.agentCommand(protocol.Austin)
	if strings.Contains(cmd, "gemini-3.8-flash-high") {
		t.Fatalf("bare model leaked into the opencode command: %q", cmd)
	}
	if strings.Contains(cmd, "--model") {
		t.Fatalf("no valid model is configured, so none should be injected: %q", cmd)
	}
	if cfg.agentDriver(protocol.Austin) != "opencode" {
		t.Fatalf("driver = %q, want opencode", cfg.agentDriver(protocol.Austin))
	}
}

// TestModelPersistedForAnotherDriverIsDropped covers the launch that failed for a
// user who switched Tony to pi: .duo/config.json still held the opencode model
// Tony had been using, --tony-driver pi changed the driver, and the model rode
// along. pi rejects an unknown model and exits before connecting.
//
// The agent file records which driver the model was chosen for, which is what
// makes the model safe to carry over or not.
func TestModelPersistedForAnotherDriverIsDropped(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".duo"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"agents":{"tony":{"driver":"opencode","model":"opencode/space-bunny-free"}}}`
	if err := os.WriteFile(filepath.Join(root, ".duo", "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DUO_REPO", "")
	cfg, err := loadConfig([]string{"--tony-driver", "pi"})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.agentDriver(protocol.Tony); got != "pi" {
		t.Fatalf("Tony driver = %q, want pi", got)
	}
	if got := cfg.agentModel(protocol.Tony); got == "opencode/space-bunny-free" {
		t.Errorf("Tony kept the model persisted for opencode: %q", got)
	}
	// The model that reaches the command line is what pi would actually receive.
	if cmd := cfg.agentCommand(protocol.Tony); strings.Contains(cmd, "opencode/") {
		t.Errorf("pi command still carries the opencode model: %s", cmd)
	}
	// Austin is untouched: same driver, so his model must survive.
	if got := cfg.agentModel(protocol.Austin); got == "" {
		t.Error("Austin's model was dropped even though his driver did not change")
	}
}
