package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/agent/agenttest"
	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/project"
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
	// The command is the operator's override and nothing else. The model and the
	// effort travel beside it and are spelled onto the command line by the plugin,
	// so a value can no longer be baked in twice.
	if cmd := cfg.agentCommand(protocol.Austin); cmd != "pi-custom" {
		t.Fatalf("austin command = %q, want the override alone", cmd)
	}
	if cmd := cfg.agentCommand(protocol.Tony); cmd != "pi-custom" {
		t.Fatalf("tony command = %q, want the override alone", cmd)
	}
	if m := cfg.agentModel(protocol.Austin); m != "anthropic/claude-3-7-sonnet" {
		t.Fatalf("austin model = %q", m)
	}
	if e := cfg.agentThinking(protocol.Austin); e != "high" {
		t.Fatalf("austin effort = %q", e)
	}
	if m := cfg.agentModel(protocol.Tony); m != "openai/o3-mini" {
		t.Fatalf("tony model = %q", m)
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

	// Core passes the command through untouched, so a flag the operator already
	// wrote cannot be duplicated and the effort cannot be appended behind it. The
	// plugin owns the command line and is the only thing that can add a flag.
	if got := cfg.agentCommand(protocol.Austin); got != "pi --model custom-model" {
		t.Fatalf("austin command = %q, want the operator's verbatim", got)
	}
	if got := cfg.agentThinking(protocol.Austin); got != "high" {
		t.Fatalf("the effort should be recorded, not appended: %q", got)
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
	// piCommand is the command for the default driver. A driver named explicitly is
	// not the default one, so it must not inherit pi-custom — and with no override
	// there is nothing here at all, because the driver's own manifest names its
	// binary.
	if cmd := cfg.agentCommand(protocol.Austin); cmd != "" {
		t.Fatalf("an explicit driver must not inherit piCommand, got %q", cmd)
	}
	// The reference is recorded exactly as the operator wrote it. Whether agy wants
	// it bare is answered by its manifest, not guessed here.
	if m := cfg.agentModel(protocol.Austin); m != "google/gemini-3.8-flash-high" {
		t.Fatalf("model = %q, want the operator's reference unaltered", m)
	}
	command, model, _ := cfg.resolveDriver(protocol.Austin, agyManifest())
	if command != "agy" {
		t.Fatalf("command = %q, want agy from the manifest", command)
	}
	if model != "gemini-3.8-flash-high" {
		t.Fatalf("model = %q, want the provider folded off for a bare driver", model)
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
	if cmd := cfg.agentCommand(protocol.Austin); cmd != "" {
		t.Fatalf("opencode driver must not inherit the pi command, got %q", cmd)
	}
	if m := cfg.agentModel(protocol.Austin); m != "opencode/claude-sonnet-4-6" {
		t.Fatalf("model = %q, want the operator's reference unaltered", m)
	}
	// opencode declares qualified references, so nothing is folded off and the
	// binary comes from its manifest rather than from a name comparison.
	command, model, _ := cfg.resolveDriver(protocol.Austin, opencodeManifest())
	if command != "opencode" {
		t.Fatalf("command = %q, want opencode from the manifest", command)
	}
	if model != "opencode/claude-sonnet-4-6" {
		t.Fatalf("model = %q, want the provider kept for a qualified driver", model)
	}
}

// Reasoning effort is recorded as a value and the driver puts it on its own command
// line, so Core cannot spell the flag and gets opencode's --variant right by not
// guessing --thinking.
func TestOpencodeThinkingIsRecordedNotAppended(t *testing.T) {
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
	if got := cfg.agentThinking(protocol.Austin); got != "high" {
		t.Fatalf("recorded effort = %q, want high", got)
	}
	command, _, effort := cfg.resolveDriver(protocol.Austin, opencodeManifest())
	if effort != "high" {
		t.Fatalf("effort did not survive resolution: %q", effort)
	}
	// No flag of any spelling is added here; the plugin owns that.
	if strings.Contains(command, "--variant") || strings.Contains(command, "--thinking") {
		t.Fatalf("Core must not spell the effort flag: %q", command)
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

	// 4. The binary for a driver comes from that driver's own manifest
	cfg, err = loadConfig([]string{"--austin-agent=agy"})
	if err != nil {
		t.Fatal(err)
	}
	command, model, _ := cfg.resolveDriver(protocol.Austin, agyManifest())
	if command != "agy" {
		t.Fatalf("command = %q, want agy from the manifest", command)
	}
	if model != "gemini-3.8-flash-high" {
		t.Fatalf("model = %q, want the default agy declares", model)
	}
}

// A command is a command. It used to be inspected for a driver name, so pointing
// piCommand at the duo-agy bridge silently switched the driver — which is how an agy
// agent ended up recorded as pi and handed pi's default model. The driver is now
// whatever was asked for by name, and the command is handed over untouched for the
// driver to interpret.
func TestCommandDoesNotReidentifyTheDriver(t *testing.T) {
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
	if got := agyCfg.agentDriver(protocol.Austin); got != "pi" {
		t.Fatalf("driver = %q: a command must not change which driver is selected", got)
	}
	if got := agyCfg.agentCommand(protocol.Austin); got != "duo-agy" {
		t.Fatalf("command = %q, want the configured bridge untouched", got)
	}

	// Naming the driver is how an operator selects one, and then the bridge is
	// accepted as an override for that driver rather than as a way to detect it.
	named := loadConfigFor(t, `{"driver": "agy", "agents": {"austin": {"command": "duo-agy"}}}`)
	if got := named.agentDriver(protocol.Austin); got != "agy" {
		t.Fatalf("driver = %q, want agy", got)
	}
	if _, model, _ := named.resolveDriver(protocol.Austin, agyManifest()); model != "gemini-3.8-flash-high" {
		t.Fatalf("model = %q, want agy's own default", model)
	}
}

func TestDriverAndModelProjectConfigAndDefault(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("DUO_DRIVER", "")

	// 1. Nothing is chosen for the operator: an empty model means the driver decides,
	// and what it decides is whatever its manifest declares.
	cfg, err := loadConfig([]string{"--driver", "agy", tempDir})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.agentModel(protocol.Austin); got != "" {
		t.Fatalf("austin model = %q, want none chosen by Core", got)
	}
	if _, model, _ := cfg.resolveDriver(protocol.Austin, agyManifest()); model != "gemini-3.8-flash-high" {
		t.Fatalf("resolved model = %q, want the default agy declares", model)
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

	// 3. loadConfig picks up the project config. The reference is recorded exactly as
	// it was written, and the google/ prefix comes off when the driver that receives
	// it declares that it takes a bare id — not because Core knew agy by name.
	projCfg, err := loadConfig([]string{tempDir})
	if err != nil {
		t.Fatal(err)
	}
	if projCfg.agentDriver(protocol.Austin) != "agy" {
		t.Fatalf("austin driver = %q, want agy", projCfg.agentDriver(protocol.Austin))
	}
	if got := projCfg.agentModel(protocol.Austin); got != "google/gemini-3.7-flash-high" {
		t.Fatalf("austin model = %q, want it recorded as written", got)
	}
	if _, model, _ := projCfg.resolveDriver(protocol.Austin, agyManifest()); model != "gemini-3.7-flash-high" {
		t.Fatalf("resolved model = %q, want the prefix folded off for a bare driver", model)
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
// clearDuoEnv removes every DUO_* variable for the duration of a test.
//
// Duo exports these into each agent's environment, so the same test resolves a
// different configuration depending on whether it runs from a shell or from
// inside a Duo session. DUO_DRIVER in particular overrides an agent's configured
// driver, which is how this test came to fail for one of two agents reviewing
// the same commit and pass for the other. Nothing here uses LookupEnv, so an
// empty value is indistinguishable from an absent one.
func clearDuoEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if name, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(name, "DUO_") {
			t.Setenv(name, "")
		}
	}
}

// TestModelPersistedForAnotherDriverIsDropped covers the rule that a model
// recorded for one driver is meaningless to another. Switching Tony to pi left
// him dead at startup because his opencode model id rode along and pi rejects it.
//
// Both directions are exercised with the same mechanism, so the test does not
// depend on what any driver happens to default to: Austin's model is recorded for
// the driver that is still selected and must survive, Tony's is recorded for the
// driver being switched away from and must go.
func TestModelPersistedForAnotherDriverIsDropped(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".duo"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"agents":{` +
		`"austin":{"driver":"pi","model":"keep-this-model"},` +
		`"tony":{"driver":"opencode","model":"opencode/space-bunny-free"}}}`
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
	clearDuoEnv(t)

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

	// Austin is not collateral damage: his model was recorded for the driver
	// still in use, so it must be carried through untouched. Asserting on his
	// exact model rather than on it being non-empty is deliberate — an
	// unconfigured opencode is supposed to resolve to no model at all, so
	// "non-empty" would be a claim about driver defaults, not about this rule.
	if got := cfg.agentModel(protocol.Austin); got != "keep-this-model" {
		t.Errorf("Austin model = %q, want the one persisted for his own driver", got)
	}
	if command, model, _ := cfg.resolveDriver(protocol.Austin, agyManifest()); model != "keep-this-model" || command == "" {
		t.Errorf("Austin's own model did not survive resolution: model %q, command %q", model, command)
	}
}

// TestComposeSnapshotCarriesDriverStateVerbatim is the durability guarantee: what a
// plugin stored must come back out of the snapshot byte for byte, for any driver.
//
// Core cannot round-trip this itself — it does not know the blob's shape — so the
// test asserts equality rather than meaning. A driver that stores a conversation id
// and a driver that stores nothing at all are both correct here. The blob lives in
// DriverStates only: PiSessions is the bare downgrade mirror a v0.9.0 binary reads.
func TestComposeSnapshotCarriesDriverStateVerbatim(t *testing.T) {
	agyBlob := `{"conversationId":"conv-1","logFile":"/tmp/duo-agy-austin.log"}`
	piBlob := `{"sessionId":"0f2b7c1e-1111-4222-8333-444455556666"}`

	r := &runtime{
		cfg:       config{agentDrivers: map[protocol.AgentID]string{protocol.Austin: "agy", protocol.Tony: "pi"}},
		repoID:    "repo-1",
		sessionID: "session-1",
		createdAt: time.Unix(0, 0).UTC(),
		state:     project.NewStateFor(project.ModeGoal),
		set:       workspace.Set{},
		driverState: map[protocol.AgentID]sessionstore.DriverState{
			protocol.Austin: {Driver: "agy", State: json.RawMessage(agyBlob)},
			protocol.Tony:   {Driver: "pi", State: json.RawMessage(piBlob)},
		},
		piSessions: map[protocol.AgentID]string{
			protocol.Austin: "conv-1",
			protocol.Tony:   "0f2b7c1e-1111-4222-8333-444455556666",
		},
	}

	snap := r.composeSnapshot(nil)
	if got := snap.DriverStates[protocol.Austin]; got.Driver != "agy" || string(got.State) != agyBlob {
		t.Errorf("Austin state = %+v, want driver agy and the blob verbatim", got)
	}
	if got := snap.DriverStates[protocol.Tony]; got.Driver != "pi" || string(got.State) != piBlob {
		t.Errorf("Tony state = %+v, want driver pi and the blob verbatim", got)
	}
	// The mirror stays bare: a `{`-prefixed value here would be read by a v0.9.0
	// binary as an identity string and resume nothing.
	if got := snap.PiSessions[protocol.Austin]; got != "conv-1" {
		t.Errorf("Austin mirror = %q, want the bare identity", got)
	}
	if got := snap.PiSessions[protocol.Tony]; got != "0f2b7c1e-1111-4222-8333-444455556666" {
		t.Errorf("Tony mirror = %q, want the bare identity", got)
	}
	if snap.AgentDrivers[protocol.Austin] != "agy" || snap.AgentDrivers[protocol.Tony] != "pi" {
		t.Errorf("drivers = %v, want each agent's plugin recorded", snap.AgentDrivers)
	}
}

// TestComposeSnapshotPrefersTheLiveDriverState covers a restart: the state a plugin
// returned for the run that just happened must win over what was loaded, or a
// driver that learned its identity would keep replaying the previous one. The same
// run refills the downgrade mirror from the identity the driver reports.
func TestComposeSnapshotPrefersTheLiveDriverState(t *testing.T) {
	mgr := agent.NewManager()
	stub := agenttest.New("stub").With(func(spec *agenttest.Spec) {
		spec.Command = "sh -c 'exit 0'"
		spec.State = []byte(`{"sessionId":"learned"}`)
	})
	s, err := agent.NewSession(context.Background(), agent.Config{
		Agent:       protocol.Austin,
		Dir:         t.TempDir(),
		Plugin:      stub.Caller(),
		PluginState: []byte(`{"sessionId":""}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	mgr.Add(s)

	r := &runtime{
		cfg:       config{agentDrivers: map[protocol.AgentID]string{protocol.Austin: "stub"}},
		repoID:    "repo-1",
		sessionID: "session-1",
		createdAt: time.Unix(0, 0).UTC(),
		state:     project.NewStateFor(project.ModeGoal),
		set:       workspace.Set{},
		agents:    mgr,
		driverState: map[protocol.AgentID]sessionstore.DriverState{
			protocol.Austin: {Driver: "stub", State: json.RawMessage(`{"sessionId":""}`)},
		},
		piSessions: map[protocol.AgentID]string{},
	}
	snap := r.composeSnapshot(nil)
	if got := snap.DriverStates[protocol.Austin]; string(got.State) != `{"sessionId":"learned"}` || got.Driver != "stub" {
		t.Errorf("snapshot state = %+v, want what the driver learned this run", got)
	}
	// The stub reports "test-session" as its identity; the mirror must carry that
	// bare value, never the blob.
	if got := snap.PiSessions[protocol.Austin]; got != "test-session" {
		t.Errorf("mirror = %q, want the driver's reported identity", got)
	}
}

// TestDriverSeedsKeepsBlobsAndBareIdentitiesApart pins the resume wiring: the
// authoritative blobs come from DriverStates, and only bare values enter the
// downgrade mirror. An M1-era blob sitting in PiSessions must not become a
// "v0.9.0 identity" — it is skipped until the driver reports one live.
func TestDriverSeedsKeepsBlobsAndBareIdentitiesApart(t *testing.T) {
	snap := sessionstore.Snapshot{
		DriverStates: map[protocol.AgentID]sessionstore.DriverState{
			protocol.Austin: {Driver: "agy", State: json.RawMessage(`{"conversationId":"conv-1"}`)},
			protocol.Tony:   {Driver: "pi", State: json.RawMessage(`{"sessionId":"pi-id"}`)},
		},
		PiSessions: map[protocol.AgentID]string{
			protocol.Austin: "conv-1",
			protocol.Tony:   `{"sessionId":"blob-era"}`,
		},
	}

	driverState, piSessions := driverSeeds(snap)

	if got := driverState[protocol.Austin]; got.Driver != "agy" || string(got.State) != `{"conversationId":"conv-1"}` {
		t.Errorf("Austin blob = %+v, want the stored driver state", got)
	}
	if got := driverState[protocol.Tony]; got.Driver != "pi" || string(got.State) != `{"sessionId":"pi-id"}` {
		t.Errorf("Tony blob = %+v, want the stored driver state", got)
	}
	if got := piSessions[protocol.Austin]; got != "conv-1" {
		t.Errorf("Austin mirror = %q, want the bare identity", got)
	}
	if _, ok := piSessions[protocol.Tony]; ok {
		t.Errorf("Tony mirror must skip the blob: %q", piSessions[protocol.Tony])
	}
}

// manifestFor builds the manifest a driver would have declared, so the resolution
// below can be tested without launching one.
func manifestFor(name, cli, reference, defaultModel string) *driver.Manifest {
	return &driver.Manifest{
		Protocol:       driver.ProtocolVersion,
		Name:           name,
		Agent:          driver.AgentInfo{CLI: cli, DefaultCommand: cli},
		DefaultModel:   defaultModel,
		ModelReference: reference,
	}
}

func agyManifest() *driver.Manifest {
	return manifestFor("agy", "agy", driver.ModelBare, "gemini-3.8-flash-high")
}

func opencodeManifest() *driver.Manifest {
	return manifestFor("opencode", "opencode", driver.ModelQualified, "")
}

// TestResolveDriverAnswersFromTheManifest is the M3 contract in one place: the binary,
// the model and the default all come from the driver, and Core's own name for it
// changes nothing.
func TestResolveDriverAnswersFromTheManifest(t *testing.T) {
	cases := []struct {
		name        string
		cfg         config
		agent       protocol.AgentID
		manifest    *driver.Manifest
		wantCommand string
		wantModel   string
	}{
		{
			name:        "no override falls back to the manifest's binary",
			cfg:         config{},
			agent:       protocol.Austin,
			manifest:    agyManifest(),
			wantCommand: "agy",
			wantModel:   "gemini-3.8-flash-high",
		},
		{
			name:        "an operator's command still wins",
			cfg:         config{agentCommands: map[protocol.AgentID]string{protocol.Austin: "my-wrapper"}},
			agent:       protocol.Austin,
			manifest:    agyManifest(),
			wantCommand: "my-wrapper",
			wantModel:   "gemini-3.8-flash-high",
		},
		{
			name:        "a bare driver folds the provider off a persisted reference",
			cfg:         config{agentModels: map[protocol.AgentID]string{protocol.Austin: "google/gemini-3.8-flash-high"}},
			agent:       protocol.Austin,
			manifest:    agyManifest(),
			wantCommand: "agy",
			wantModel:   "gemini-3.8-flash-high",
		},
		{
			name:        "a qualified driver keeps the provider",
			cfg:         config{agentModels: map[protocol.AgentID]string{protocol.Austin: "opencode/claude-sonnet-4-6"}},
			agent:       protocol.Austin,
			manifest:    opencodeManifest(),
			wantCommand: "opencode",
			wantModel:   "opencode/claude-sonnet-4-6",
		},
		{
			name:        "a driver that declares no default is handed none",
			cfg:         config{},
			agent:       protocol.Tony,
			manifest:    opencodeManifest(),
			wantCommand: "opencode",
			wantModel:   "",
		},
		{
			name:        "an unresolved driver leaves the operator's values alone",
			cfg:         config{agentModels: map[protocol.AgentID]string{protocol.Austin: "some/model"}},
			agent:       protocol.Austin,
			manifest:    nil,
			wantCommand: "",
			wantModel:   "some/model",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			command, model, _ := tc.cfg.resolveDriver(tc.agent, tc.manifest)
			if command != tc.wantCommand {
				t.Errorf("command = %q, want %q", command, tc.wantCommand)
			}
			if model != tc.wantModel {
				t.Errorf("model = %q, want %q", model, tc.wantModel)
			}
		})
	}
}

// TestResolveDriverPassesTheEffortThrough keeps the effort a separate value: which
// flag carries it is the driver's, so Core only remembers what was asked for.
func TestResolveDriverPassesTheEffortThrough(t *testing.T) {
	cfg := config{agentThinkings: map[protocol.AgentID]string{protocol.Austin: "high"}}
	if _, _, effort := cfg.resolveDriver(protocol.Austin, opencodeManifest()); effort != "high" {
		t.Fatalf("effort = %q, want high", effort)
	}
}

// loadConfigFor writes a config file and loads it, for the cases that only need the
// JSON.
func loadConfigFor(t *testing.T, configJSON string) config {
	t.Helper()
	temp := t.TempDir()
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
