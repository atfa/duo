package pi

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/driver"
)

// These tests moved here from internal/models/models_test.go unchanged in intent.
// They pin the exact `pi --list-models` output shape Duo 0.9.0 parsed, because a
// parser that silently returns fewer models produces an empty picker rather than
// an error.

func TestParseListModels(t *testing.T) {
	// Shape copied from `pi --list-models`: a header, aligned columns, provider
	// names and ids that contain slashes, and yes/no capability flags.
	output := `provider      model                                               context  max-out  thinking  images
cline         ~anthropic/claude-fable-latest                      1M       128K     yes       yes
cline         deepseek/deepseek-v4-flash-latest                  1.3M     943.7K   yes       no
workbuddy     deepseek-v4.1-flash                                 300K     64K      no        yes

Warning: errors loading models.json:
something went wrong`

	got, err := ParseListModels(output)
	if err != nil {
		t.Fatal(err)
	}
	want := []driver.Model{
		{Provider: "cline", ID: "~anthropic/claude-fable-latest", Thinking: true, Images: true},
		{Provider: "cline", ID: "deepseek/deepseek-v4-flash-latest", Thinking: true, Images: false},
		{Provider: "workbuddy", ID: "deepseek-v4.1-flash", Thinking: false, Images: true},
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

func TestParseIgnoresProse(t *testing.T) {
	got, err := ParseListModels("No models available\nrun pi auth first\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("parsed %#v, want none", got)
	}
}

// A model id may contain spaces; the id is everything between the provider and
// the last four columns, not just the second whitespace-separated token.
func TestParseKeepsSpacedModelID(t *testing.T) {
	output := `provider      model                        context  max-out  thinking  images
acme          gpt-5.3 codex preview         256K     64K      yes       no`

	got, err := ParseListModels(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("parsed %#v, want one model", got)
	}
	if got[0].Provider != "acme" || got[0].ID != "gpt-5.3 codex preview" {
		t.Fatalf("model = %#v, want acme/gpt-5.3 codex preview", got[0])
	}
	if !got[0].Thinking || got[0].Images {
		t.Fatalf("flags = %#v, want thinking yes images no", got[0])
	}
}

// TestDefaultModelPrefersPiSettings pins the precedence: the human's own Pi
// setting wins, because Duo must not override a choice the operator already made.
func TestDefaultModelPrefersPiSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".pi", "agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(home, ".pi", "agent", "settings.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(`{"defaultProvider":"workbuddy","defaultModel":"deepseek-v4.1-flash"}`)
	if got := DefaultModel(); got != "workbuddy/deepseek-v4.1-flash" {
		t.Fatalf("DefaultModel() = %q, want the configured provider/id", got)
	}

	// A model that already carries its provider must not be prefixed twice.
	write(`{"defaultProvider":"cline","defaultModel":"cline/~claude-fable"}`)
	if got := DefaultModel(); got != "cline/~claude-fable" {
		t.Fatalf("DefaultModel() = %q, want no double prefix", got)
	}

	// A model with no provider is used as written.
	write(`{"defaultModel":"some-model"}`)
	if got := DefaultModel(); got != "some-model" {
		t.Fatalf("DefaultModel() = %q, want the bare model", got)
	}

	// No usable settings falls back, so a fresh install still has a model.
	write(`{"defaultModel":"   "}`)
	if got := DefaultModel(); got != "anthropic/claude-sonnet-4-6" {
		t.Fatalf("DefaultModel() = %q, want the fallback", got)
	}
}

// TestManifestDeclaresPiBehaviour pins the capabilities Core branches on. These
// are not descriptions: each one changes how Core delivers a message.
func TestManifestDeclaresPiBehaviour(t *testing.T) {
	man, err := New().Describe()
	if err != nil {
		t.Fatal(err)
	}
	if man.Name != Name || man.ModelReference != driver.ModelQualified {
		t.Fatalf("manifest = %+v", man)
	}
	caps := man.Capabilities
	if caps.Resume != driver.ResumeClient {
		t.Errorf("resume = %q; Pi adopts an id it has never seen, so it must be client", caps.Resume)
	}
	if !caps.LiveModelSwitch || !caps.LiveThinkingSwitch {
		t.Error("Pi switches model and thinking over its bridge, so both must be live")
	}
	if !caps.LiveSteering || caps.Bridge != driver.BridgeAgent {
		t.Error("pi-extension connects from inside Pi, so it can receive an injected prompt")
	}
	if !caps.SelfReports {
		t.Error("pi-extension connects on session_start, so Core must not announce the connection itself")
	}
	if !caps.Models || !caps.Thinking {
		t.Error("Pi provides both a catalog and thinking levels")
	}
}

// TestPrepareAlwaysCarriesAnIdentity is the resume contract, and it is the whole
// reason pi is ResumeClient: Pi adopts an id it has never seen, so a first run
// gets a freshly minted one and every run after reuses it. A driver whose agent
// rejects an unknown id must declare ResumeServer and send nothing here instead.
func TestPrepareAlwaysCarriesAnIdentity(t *testing.T) {
	plan, err := New().Prepare(driver.LaunchRequest{BaseCommand: "pi"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Command, "--session-id") {
		t.Errorf("first launch carries no identity, so a resume could never reattach: %q", plan.Command)
	}
	if plan.Env["DUO_PI_SESSION_ID"] == "" {
		t.Error("prepare returned no identity to persist")
	}

	first := plan.Env["DUO_PI_SESSION_ID"]
	resumed, err := New().Prepare(driver.LaunchRequest{BaseCommand: "pi", State: plan.State})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resumed.Command, `"$DUO_PI_SESSION_ID"`) {
		t.Errorf("resumed launch does not replay the identity: %q", resumed.Command)
	}
	if resumed.Env["DUO_PI_SESSION_ID"] != first {
		t.Errorf("resume minted a new identity %q, want %q", resumed.Env["DUO_PI_SESSION_ID"], first)
	}
}

// TestPrepareRespectsAnOperatorsSessionFlag: an explicit operator choice is
// preserved rather than overridden.
func TestPrepareRespectsAnOperatorsSessionFlag(t *testing.T) {
	plan, err := New().Prepare(driver.LaunchRequest{BaseCommand: "pi --resume mine"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.Command, "--session-id") {
		t.Errorf("Duo injected --session-id over the operator's own --resume: %q", plan.Command)
	}
}

// TestPrepareAppliesModelAndThinking checks the launch flags reach the command and
// do not duplicate an operator's own.
func TestPrepareAppliesModelAndThinking(t *testing.T) {
	plan, err := New().Prepare(driver.LaunchRequest{
		BaseCommand: "pi",
		Model:       "anthropic/claude-sonnet-4-6",
		Thinking:    "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Command, "--model 'anthropic/claude-sonnet-4-6'") {
		t.Errorf("model missing from %q", plan.Command)
	}
	if !strings.Contains(plan.Command, "--thinking 'high'") {
		t.Errorf("thinking missing from %q", plan.Command)
	}

	kept, err := New().Prepare(driver.LaunchRequest{BaseCommand: "pi --model mine", Model: "theirs"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(kept.Command, "--model") != 1 || !strings.Contains(kept.Command, "mine") {
		t.Errorf("Duo overrode the operator's model: %q", kept.Command)
	}

	keptThinking, err := New().Prepare(driver.LaunchRequest{BaseCommand: "pi --thinking low", Thinking: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(keptThinking.Command, "--thinking") != 1 || !strings.Contains(keptThinking.Command, "low") {
		t.Errorf("Duo overrode the operator's thinking level: %q", keptThinking.Command)
	}
}

// TestPrepareRejectsUnreadableState: a corrupt blob must be reported, not
// silently treated as a first run, or a resume would quietly start a new
// conversation and look like it worked.
func TestPrepareRejectsUnreadableState(t *testing.T) {
	_, err := New().Prepare(driver.LaunchRequest{BaseCommand: "pi", State: []byte("{not json")})
	if err == nil {
		t.Fatal("a corrupt resume blob must be reported")
	}
}

func TestProbeReportsAMissingCLIWithoutFailing(t *testing.T) {
	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { lookPath = exec.LookPath })

	res, err := New().Probe()
	if err != nil {
		t.Fatalf("a missing CLI must be a reportable state, not an error: %v", err)
	}
	if res.Available || res.Reason == "" {
		t.Fatalf("probe = %+v, want unavailable with a reason", res)
	}

	lookPath = func(string) (string, error) { return "/usr/local/bin/pi", nil }
	res, err = New().Probe()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Available || res.AgentPath != "/usr/local/bin/pi" {
		t.Fatalf("probe = %+v, want available with a path", res)
	}
}

// TestIdentityIsPerAgentAndPerRun covers what Duo Core used to do and no longer
// does: mint one identity per agent and never hand the same one to both.
//
// Core no longer generates identities at all, because only the plugin knows
// whether its agent adopts one it has never seen. The obligation therefore moved
// here, and this is the test that holds it.
func TestIdentityIsPerAgentAndPerRun(t *testing.T) {
	austin, err := New().Prepare(driver.LaunchRequest{Agent: "Austin", BaseCommand: "pi"})
	if err != nil {
		t.Fatal(err)
	}
	tony, err := New().Prepare(driver.LaunchRequest{Agent: "Tony", BaseCommand: "pi"})
	if err != nil {
		t.Fatal(err)
	}
	a, b := austin.Env["DUO_PI_SESSION_ID"], tony.Env["DUO_PI_SESSION_ID"]
	if a == "" || b == "" {
		t.Fatalf("identities must not be empty: %q %q", a, b)
	}
	if a == b {
		t.Fatalf("both agents were given %q; they must never share a conversation", a)
	}

	// Re-preparing with the stored blob must reuse it rather than mint again,
	// because a restart that changed identity would lose the conversation.
	again, err := New().Prepare(driver.LaunchRequest{Agent: "Austin", BaseCommand: "pi", State: austin.State})
	if err != nil {
		t.Fatal(err)
	}
	if again.Env["DUO_PI_SESSION_ID"] != a {
		t.Errorf("a restart changed the identity from %q to %q", a, again.Env["DUO_PI_SESSION_ID"])
	}
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
