package opencode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/driver"
)

// These tests moved here from internal/agent/commandline_test.go and
// internal/models/models_test.go unchanged in intent. The one that matters most is
// the session id: opencode rejects an id it never issued, so getting the identity
// rule wrong costs a silent conversation fork on every launch.

func TestParseModels(t *testing.T) {
	// `opencode models` prints one plain provider/model reference per line.
	output := `anthropic/claude-sonnet-4-6
openai/gpt-5.3-codex
vendor/model/v2
anthropic/claude-sonnet-4-6
not-a-model-line`

	got, err := ParseModels(output)
	if err != nil {
		t.Fatal(err)
	}
	want := []driver.Model{
		{Provider: "anthropic", ID: "claude-sonnet-4-6"},
		{Provider: "openai", ID: "gpt-5.3-codex"},
		{Provider: "vendor", ID: "model/v2"},
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d models, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("model %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestPrepareSendsNoSessionItHasNotSeen(t *testing.T) {
	plan := prepare(t, driver.LaunchRequest{BaseCommand: Name})
	if strings.Contains(plan.Command, "--session") {
		t.Errorf("a first launch was handed a session id opencode never issued: %s", plan.Command)
	}
	if plan.Env["DUO_OPENCODE_SESSION_ID"] != "" {
		t.Error("a first launch published an invented session id")
	}
}

// TestPrepareIgnoresAPlaceholderIdentity: a value from an older Duo that is not an
// opencode id must be dropped rather than passed through. opencode answers "Session
// not found" and opens a different session, and because the rejected value is also
// what gets persisted, the agent would start fresh every launch and look like it
// simply never resumed.
func TestPrepareIgnoresAPlaceholderIdentity(t *testing.T) {
	for _, bogus := range []string{"20261003-185149-austin", "", "placeholder", "SES_upper"} {
		prior, err := json.Marshal(state{SessionID: bogus, SessionFile: "/tmp/duo-test.session"})
		if err != nil {
			t.Fatal(err)
		}
		plan := prepare(t, driver.LaunchRequest{BaseCommand: Name, State: prior})
		if strings.Contains(plan.Command, "--session") {
			t.Errorf("identity %q was passed to opencode: %s", bogus, plan.Command)
		}
		if plan.SessionIdentity != "" {
			t.Errorf("identity %q was reported as this agent's session", bogus)
		}
	}
}

func TestPrepareReplaysAKnownIdentity(t *testing.T) {
	prior, err := json.Marshal(state{SessionID: "ses_01ABC", SessionFile: "/tmp/duo-test.session"})
	if err != nil {
		t.Fatal(err)
	}
	plan := prepare(t, driver.LaunchRequest{BaseCommand: Name, State: prior})
	if !strings.Contains(plan.Command, "--session 'ses_01ABC'") {
		t.Errorf("resume did not replay the identity: %s", plan.Command)
	}
	if plan.Env["DUO_OPENCODE_SESSION_ID"] != "ses_01ABC" {
		t.Errorf("the identity did not reach the extension: %v", plan.Env)
	}
}

// An operator who already chose a session keeps their choice.
func TestPrepareRespectsAnOperatorsSessionFlag(t *testing.T) {
	prior, err := json.Marshal(state{SessionID: "ses_01ABC", SessionFile: "/tmp/duo-test.session"})
	if err != nil {
		t.Fatal(err)
	}
	plan := prepare(t, driver.LaunchRequest{BaseCommand: Name + " --session ses_MINE", State: prior})
	if strings.Contains(plan.Command, "ses_01ABC") {
		t.Errorf("Duo overrode the operator's session: %s", plan.Command)
	}
}

// Agents run unattended in their own worktree, so tool calls must not block on an
// interactive permission prompt.
func TestPrepareRunsUnattended(t *testing.T) {
	plan := prepare(t, driver.LaunchRequest{BaseCommand: Name})
	if !strings.Contains(plan.Command, "--auto") {
		t.Errorf("opencode would stop on a permission prompt: %s", plan.Command)
	}
	already := prepare(t, driver.LaunchRequest{BaseCommand: Name + " --auto"})
	if strings.Count(already.Command, "--auto") != 1 {
		t.Errorf("flag duplicated: %s", already.Command)
	}
}

// opencode spells reasoning effort --variant. Passing --thinking instead produced an
// "unknown flag" exit with the reason visible only in the session log.
func TestPrepareSpellsEffortAsVariant(t *testing.T) {
	plan := prepare(t, driver.LaunchRequest{BaseCommand: Name, Thinking: "high"})
	if !strings.Contains(plan.Command, "--variant 'high'") {
		t.Errorf("effort missing or misspelled: %s", plan.Command)
	}
	if strings.Contains(plan.Command, "--thinking") {
		t.Errorf("opencode does not accept --thinking: %s", plan.Command)
	}
}

func TestPrepareAppliesModelAndKeepsAQualifiedReference(t *testing.T) {
	plan := prepare(t, driver.LaunchRequest{BaseCommand: Name, Model: "anthropic/claude-sonnet-4-6"})
	if !strings.Contains(plan.Command, "--model 'anthropic/claude-sonnet-4-6'") {
		t.Errorf("model missing or stripped: %s", plan.Command)
	}
}

// The session file is where the extension reports the id opencode assigned, so it
// must be cleared between runs and must not be shared by the two agents.
func TestPrepareClearsAndSeparatesTheSessionFile(t *testing.T) {
	austin := prepare(t, driver.LaunchRequest{Agent: "Austin", BaseCommand: Name})
	tony := prepare(t, driver.LaunchRequest{Agent: "Tony", BaseCommand: Name})
	if austin.Env["DUO_OPENCODE_SESSION_FILE"] == tony.Env["DUO_OPENCODE_SESSION_FILE"] {
		t.Error("both agents would read the same reported session id")
	}
	for _, plan := range []*driver.LaunchPlan{austin, tony} {
		file := plan.Env["DUO_OPENCODE_SESSION_FILE"]
		if file == "" {
			t.Fatal("no session file published for the extension to write")
		}
		if len(plan.Cleanup) != 1 || plan.Cleanup[0] != file {
			t.Errorf("cleanup = %v, want the session file so a stale id is never replayed", plan.Cleanup)
		}
	}
}

// TestLearnSessionIDWaitsForTheExtension covers the only way this driver learns an
// identity: the extension writes the file once opencode has created its session.
func TestLearnSessionIDWaitsForTheExtension(t *testing.T) {
	file := filepath.Join(t.TempDir(), "reported.session")

	// A half-written or foreign value must not be accepted.
	if err := os.WriteFile(file, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	if got := LearnSessionID(ctx, file); got != "" {
		t.Fatalf("LearnSessionID accepted %q, want nothing", got)
	}

	watchInterval = 10 * time.Millisecond
	t.Cleanup(func() { watchInterval = 250 * time.Millisecond })
	if err := os.WriteFile(file, []byte("ses_01ABC\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	if got := LearnSessionID(ctx2, file); got != "ses_01ABC" {
		t.Fatalf("LearnSessionID = %q, want ses_01ABC", got)
	}

	// No file at all is not an error: the run works, it just cannot be resumed.
	if got := LearnSessionID(ctx2, ""); got != "" {
		t.Errorf("LearnSessionID with no file = %q", got)
	}
}

// TestDefaultModelPrefersTheUsersConfiguration pins the rule that keeps a foreign
// provider id out of the command line: opencode would abort with "Model not found",
// and it already resolves its own default.
func TestDefaultModelPrefersTheUsersConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Nothing configured: no model is injected at all.
	if got := DefaultModel(); got != "" {
		t.Errorf("DefaultModel() = %q, want empty so opencode resolves its own", got)
	}

	if err := os.WriteFile(filepath.Join(configDir, "opencode.json"), []byte(`{"model":"anthropic/claude-sonnet-4-6"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DefaultModel(); got != "anthropic/claude-sonnet-4-6" {
		t.Errorf("DefaultModel() = %q", got)
	}

	// opencode also accepts JSONC, and a comment must not fail the parse. Plain
	// JSON is checked first, so remove it to reach this path.
	if err := os.Remove(filepath.Join(configDir, "opencode.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "opencode.jsonc"), []byte(`{
  // the model I actually want
  "model": "openai/gpt-5.3-codex"
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DefaultModel(); got != "openai/gpt-5.3-codex" {
		t.Errorf("DefaultModel() = %q, want the JSONC value", got)
	}

	// A comment inside a string is not a comment.
	if err := os.WriteFile(filepath.Join(configDir, "opencode.jsonc"), []byte(`{"model":"a//b"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DefaultModel(); got != "a//b" {
		t.Errorf("DefaultModel() = %q, want a//b: a // inside a string is data", got)
	}
}

// TestManifestDeclaresOpencodeBehaviour pins the capabilities Core branches on.
// Two of them exist because opencode rejects what Pi accepts.
func TestManifestDeclaresOpencodeBehaviour(t *testing.T) {
	man, err := New().Describe()
	if err != nil {
		t.Fatal(err)
	}
	if man.ModelReference != driver.ModelQualified {
		t.Errorf("modelReference = %q; opencode only accepts provider/model", man.ModelReference)
	}
	caps := man.Capabilities
	if caps.Resume != driver.ResumeServer {
		t.Errorf("resume = %q; opencode mints session ids server-side", caps.Resume)
	}
	if caps.LiveModelSwitch || caps.LiveThinkingSwitch {
		t.Error("both are launch flags here, so neither is live")
	}
	if caps.Bridge != driver.BridgeAgent || !caps.LiveSteering {
		t.Error("the extension injects through promptAsync, so it can receive prompts mid-turn")
	}
	if caps.SelfReports {
		t.Error("opencode only creates a session on first input, so Core must announce the connection")
	}
}

func TestPrepareRejectsUnreadableState(t *testing.T) {
	if _, err := New().Prepare(driver.LaunchRequest{BaseCommand: Name, State: []byte("{not json")}); err == nil {
		t.Fatal("a corrupt resume blob must be reported, not treated as a first run")
	}
}

func prepare(t *testing.T, req driver.LaunchRequest) *driver.LaunchPlan {
	t.Helper()
	plan, err := New().Prepare(req)
	if err != nil {
		t.Fatalf("prepare %+v: %v", req, err)
	}
	return plan
}

// TestDeclaredCapabilitiesAreImplemented is a guard against the one mistake that
// makes a driver look healthy and behave as if it had no features at all: declaring
// a capability without implementing the method behind it. Over RPC the plugin
// answers `unsupported`, the picker comes up empty, and nothing anywhere reports an
// error — which is exactly how this shipped once.
//
// It is cheap, and it is the minimum the contract suite in M6 will generalise.
func TestDeclaredCapabilitiesAreImplemented(t *testing.T) {
	var h driver.Handler = New()
	man, err := h.Describe()
	if err != nil {
		t.Fatal(err)
	}
	if man.Capabilities.Models {
		if _, ok := h.(driver.ModelLister); !ok {
			t.Error("capabilities.models is declared but the models method is not implemented")
		}
	}
	if man.Capabilities.Thinking {
		if _, ok := h.(driver.ThinkingProvider); !ok {
			t.Error("capabilities.thinking is declared but the thinking method is not implemented")
		}
	}
}
