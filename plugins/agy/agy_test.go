package agy

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/driver"
)

// These tests moved here from internal/agent/commandline_test.go and
// internal/models/models_test.go unchanged in intent. They pin the exact agy
// command line and catalog shape Duo 0.9.0 produced, because each of them encodes
// a bug that already cost a real session.

// A substring test for "-c" also matches "--config". When it did, Duo silently
// stopped injecting --conversation and the agy agent lost its stable identity
// across restarts.
func TestAgyConversationInjectedDespiteUnrelatedFlags(t *testing.T) {
	prior, err := json.Marshal(state{ConversationID: "conv-1", LogFile: "/tmp/duo-test.log"})
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		"agy",
		"agy --config /etc/agy.json",
		"agy --continue",
		"agy --model google/gemini-3.8-flash-low",
	} {
		plan := prepare(t, driver.LaunchRequest{BaseCommand: command, State: prior})
		if !strings.Contains(plan.Command, `--conversation "$DUO_AGY_CONVERSATION_ID"`) {
			t.Errorf("command %q: conversation id was not injected: %s", command, plan.Command)
		}
	}
}

// An operator who already supplies a conversation flag keeps their choice.
func TestAgyRespectsExistingConversationFlag(t *testing.T) {
	prior, err := json.Marshal(state{ConversationID: "conv-1", LogFile: "/tmp/duo-test.log"})
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{`agy --conversation mine`, "agy -c mine"} {
		plan := prepare(t, driver.LaunchRequest{BaseCommand: command, State: prior})
		if strings.Contains(plan.Command, "$DUO_AGY_CONVERSATION_ID") {
			t.Errorf("command %q: Duo should not override the operator's conversation: %s", command, plan.Command)
		}
	}
}

// TestAgySendsNoConversationItHasNotSeen is the rule that keeps an agy session
// from forking. agy answers an id it does not know with "not found, ignoring
// --conversation flag" and opens a different conversation; persisting that
// rejected id would replay the rejection on every launch, so the agent would start
// fresh every time and look like it simply never resumed.
func TestAgySendsNoConversationItHasNotSeen(t *testing.T) {
	plan := prepare(t, driver.LaunchRequest{BaseCommand: "agy"})
	if strings.Contains(plan.Command, "$DUO_AGY_CONVERSATION_ID") {
		t.Errorf("a first launch was handed an identity agy has never issued: %s", plan.Command)
	}
	if plan.Env["DUO_AGY_CONVERSATION_ID"] != "" {
		t.Error("a first launch published an invented conversation id")
	}
	if plan.SessionIdentity != "" {
		t.Errorf("session identity = %q, want empty on a first run", plan.SessionIdentity)
	}
}

// The model id may itself contain a slash, so the provider prefix is stripped only
// when there is exactly one. Truncating at the first slash made the command line
// and .duo/config.json disagree about the same model.
func TestProviderPrefixStrippedConsistently(t *testing.T) {
	cases := map[string]string{
		"vendor/gemini-3.8-flash-high": "gemini-3.8-flash-high",
		"gemini-3.8-flash-high":        "gemini-3.8-flash-high",
		"":                             "",
	}
	for model, want := range cases {
		plan := prepare(t, driver.LaunchRequest{BaseCommand: "agy", Model: model})
		got := flagValue(t, plan.Command, "--model")
		if got != want {
			t.Errorf("model %q reached the command line as %q, want %q", model, got, want)
		}
	}
}

// TestAgyNormalisesAnOperatorsOwnPrefixedModel: with no model selected, a
// provider-prefixed --model on the operator's own command is still normalised,
// because agy would abort on it.
func TestAgyNormalisesAnOperatorsOwnPrefixedModel(t *testing.T) {
	plan := prepare(t, driver.LaunchRequest{BaseCommand: "agy --model vendor/gemini-3.8-flash-high"})
	if got := flagValue(t, plan.Command, "--model"); got != "gemini-3.8-flash-high" {
		t.Errorf("model = %q, want the bare id", got)
	}
}

// An agent runs unattended in its own worktree, so the trust prompt must not
// block it.
func TestAgyAlwaysRunsUnattended(t *testing.T) {
	plan := prepare(t, driver.LaunchRequest{BaseCommand: "agy"})
	if !strings.Contains(plan.Command, "--dangerously-skip-permissions") {
		t.Errorf("agy would stop on its interactive permission prompt: %s", plan.Command)
	}
	already := prepare(t, driver.LaunchRequest{BaseCommand: "agy --dangerously-skip-permissions"})
	if strings.Count(already.Command, "--dangerously-skip-permissions") != 1 {
		t.Errorf("flag duplicated: %s", already.Command)
	}
}

// The log file is where the Agent Adapter learns the conversation id, so a stale
// one from a previous run must be cleared and never reported as this run's.
func TestAgyClearsItsLogBetweenRuns(t *testing.T) {
	plan := prepare(t, driver.LaunchRequest{BaseCommand: "agy"})
	logFile := plan.Env["DUO_AGY_LOG_FILE"]
	if logFile == "" {
		t.Fatal("prepare published no log file for the adapter to read")
	}
	if len(plan.Cleanup) != 1 || plan.Cleanup[0] != logFile {
		t.Errorf("cleanup = %v, want the log file so a stale value is never read", plan.Cleanup)
	}
	if !strings.Contains(plan.Command, "--log-file") {
		t.Errorf("agy was not told to log where the adapter looks: %s", plan.Command)
	}
}

// The log file name is stable across runs so the adapter can find the same file,
// but distinct per agent so two agents never read each other's conversation.
func TestAgyLogFilesArePerAgent(t *testing.T) {
	austin := prepare(t, driver.LaunchRequest{Agent: "Austin", BaseCommand: "agy"})
	tony := prepare(t, driver.LaunchRequest{Agent: "Tony", BaseCommand: "agy"})
	if austin.Env["DUO_AGY_LOG_FILE"] == tony.Env["DUO_AGY_LOG_FILE"] {
		t.Error("both agents would watch the same log file")
	}
	if !strings.Contains(austin.Env["DUO_AGY_LOG_FILE"], "austin") {
		t.Errorf("log file %q does not name the agent", austin.Env["DUO_AGY_LOG_FILE"])
	}
}

func TestParseModels(t *testing.T) {
	// Shape copied from `agy models`: a spinner prefix, then one line per model
	// with a human description and no provider column.
	output := `⠋ Fetching available models...⠙ Fetching available models...gemini-3.8-flash-high     Gemini 3.8 Flash (High)
gemini-3.8-flash-medium   Gemini 3.8 Flash (Medium)
gemini-3.1-pro-low        Gemini 3.1 Pro (Low)
claude-sonnet-4-6         Claude Sonnet 4.6 (Thinking)
gpt-oss-120b-medium       GPT-OSS 120B (Medium)
custom-model-fast         Custom Fast Model`

	got, err := ParseModels(output)
	if err != nil {
		t.Fatal(err)
	}
	want := []driver.Model{
		{ID: "gemini-3.8-flash-high", Thinking: true, Images: true},
		{ID: "gemini-3.8-flash-medium", Thinking: true, Images: true},
		{ID: "gemini-3.1-pro-low", Thinking: false, Images: true},
		{ID: "claude-sonnet-4-6", Thinking: false, Images: true},
		{ID: "gpt-oss-120b-medium", Thinking: true, Images: true},
		{ID: "custom-model-fast", Thinking: false, Images: true},
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

// TestManifestDeclaresAgyBehaviour pins the capabilities Core branches on. Each one
// changes a delivery decision, and two of them exist to prevent silent data loss.
func TestManifestDeclaresAgyBehaviour(t *testing.T) {
	man, err := New(nil).Describe()
	if err != nil {
		t.Fatal(err)
	}
	if man.ModelReference != driver.ModelBare {
		t.Errorf("modelReference = %q; agy rejects a provider prefix", man.ModelReference)
	}
	caps := man.Capabilities
	if caps.Resume != driver.ResumeServer {
		t.Errorf("resume = %q; agy rejects an id it never issued", caps.Resume)
	}
	if caps.LiveModelSwitch || caps.LiveThinkingSwitch {
		t.Error("agy takes model and effort as launch flags, so neither is live")
	}
	if caps.Bridge != driver.BridgePlugin {
		t.Errorf("bridge = %q; the endpoint belongs to this plugin", caps.Bridge)
	}
	if caps.LiveSteering {
		t.Error("a plugin-owned endpoint cannot write to Core's PTY, so it cannot steer")
	}
	if caps.SelfReports {
		t.Error("the endpoint attaches only after agy reports a conversation, so Core must announce it")
	}
	if !caps.Activity || !caps.PTYFallback {
		t.Error("agy reports activity through this plugin and receives prompts at the PTY")
	}
}

// TestManifestIsRejectedIfAgyClaimedSteering keeps the impossible combination out
// of a manifest: it would look healthy and then drop every prompt.
func TestPrepareRejectsUnreadableState(t *testing.T) {
	if _, err := New(nil).Prepare(driver.LaunchRequest{BaseCommand: "agy", State: []byte("{not json")}); err == nil {
		t.Fatal("a corrupt resume blob must be reported, not treated as a first run")
	}
}

// TestWorkspaceTrustIsWrittenToAgySettings pins that this plugin edits agy's own
// configuration, which is why Core never does: a fourth driver will have its own
// equivalent and Core must not grow a switch for each.
func TestWorkspaceTrustIsWrittenToAgySettings(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GEMINI_APP_DATA_DIR", dir)
	settingsFile := filepath.Join(dir, "settings.json")

	// agy's own settings must survive being rewritten: an unrelated key is
	// dropped if the plugin replaces the file instead of merging into it.
	existing := `{"model":"test-model","trustedWorkspaces":["/existing/trusted/dir"]}`
	if err := os.WriteFile(settingsFile, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	worktree := filepath.Join(dir, "work", "austin")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureWorkspaceTrusted(worktree, "/existing/trusted/dir"); err != nil {
		t.Fatal(err)
	}

	read := func() map[string]any {
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

	settings := read()
	if settings["model"] != "test-model" {
		t.Errorf("agy settings were clobbered: %v", settings)
	}
	trusted, _ := settings["trustedWorkspaces"].([]any)
	if !containsString(trusted, "/existing/trusted/dir") {
		t.Errorf("the pre-existing entry was dropped: %v", trusted)
	}
	if !containsString(trusted, worktree) {
		t.Errorf("worktree not trusted: %v", trusted)
	}
	// agy compares the path it resolves, so the symlink-free spelling must be
	// trusted too or the prompt still appears.
	resolved, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != worktree && !containsString(trusted, resolved) {
		t.Errorf("the resolved worktree %q is not trusted: %v", resolved, trusted)
	}

	// Running again must add nothing.
	if err := EnsureWorkspaceTrusted(worktree); err != nil {
		t.Fatal(err)
	}
	after, _ := read()["trustedWorkspaces"].([]any)
	if len(after) != len(trusted) {
		t.Errorf("re-running changed the list: %d entries, want %d", len(after), len(trusted))
	}
}

func containsString(items []any, want string) bool {
	for _, item := range items {
		if s, ok := item.(string); ok && s == want {
			return true
		}
	}
	return false
}

// TestNoticeIsCheckedAtLaunchNotAtDescribe is the reason notices come from prepare:
// sqlite3 can be installed between the two, so a static string would be a claim
// Core cannot keep true.
func TestNoticeIsCheckedAtLaunchNotAtDescribe(t *testing.T) {
	t.Setenv("GEMINI_APP_DATA_DIR", t.TempDir())
	LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	t.Cleanup(func() { LookPath = exec.LookPath })

	plan := prepare(t, driver.LaunchRequest{BaseCommand: "agy"})
	if len(plan.Notices) != 1 || !strings.Contains(plan.Notices[0], "sqlite3") {
		t.Fatalf("notices = %v, want one about sqlite3", plan.Notices)
	}

	LookPath = func(string) (string, error) { return "/usr/bin/sqlite3", nil }
	plan = prepare(t, driver.LaunchRequest{BaseCommand: "agy"})
	if len(plan.Notices) != 0 {
		t.Fatalf("notices = %v, want none once sqlite3 is installed", plan.Notices)
	}
}

// prepare runs prepare with agy's configuration redirected to a temp directory, so
// no test touches a real agy install.
func prepare(t *testing.T, req driver.LaunchRequest) *driver.LaunchPlan {
	t.Helper()
	if dir := os.Getenv("GEMINI_APP_DATA_DIR"); dir == "" {
		t.Setenv("GEMINI_APP_DATA_DIR", t.TempDir())
	}
	plan, err := New(nil).Prepare(req)
	if err != nil {
		t.Fatalf("prepare %+v: %v", req, err)
	}
	return plan
}

// flagValue reads what a flag actually received after the shell has parsed the
// command. This is the only check that matters for quoting: a correctly quoted
// value and a corrupted one are indistinguishable until /bin/sh has run, and a
// value that expands a variable or a command substitution is a command injection
// into the agent's own command line.
func flagValue(t *testing.T, command, flag string) string {
	t.Helper()
	script := `set -- ` + command + `
prev=""
for a in "$@"; do
  if [ "$prev" = "` + flag + `" ]; then printf '%s' "$a"; exit 0; fi
  case "$a" in ` + flag + `=*) printf '%s' "${a#` + flag + `=}"; exit 0;; esac
  prev="$a"
done
`
	out, err := exec.Command("sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("the shell rejected %q: %v", command, err)
	}
	return string(out)
}

// TestResumeAcceptsTheSharedIdentitySpelling is the paired test for the session
// migration. Duo Core migrates a pre-protocol snapshot into {"sessionId": …}
// without knowing which agent it belongs to, because Core is forbidden from knowing
// that agy calls it a conversation. So this driver must accept the shared spelling
// or a resumed agy agent silently loses its conversation.
func TestResumeAcceptsTheSharedIdentitySpelling(t *testing.T) {
	plan := prepare(t, driver.LaunchRequest{
		BaseCommand: "agy",
		State:       []byte(`{"sessionId":"conv-from-v090"}`),
	})
	if !strings.Contains(plan.Command, `--conversation "$DUO_AGY_CONVERSATION_ID"`) {
		t.Errorf("a migrated identity was not replayed: %s", plan.Command)
	}
	if plan.Env["DUO_AGY_CONVERSATION_ID"] != "conv-from-v090" {
		t.Errorf("conversation = %q, want the migrated id", plan.Env["DUO_AGY_CONVERSATION_ID"])
	}
	if plan.SessionIdentity != "conv-from-v090" {
		t.Errorf("session identity = %q", plan.SessionIdentity)
	}
	// The log file is still the plugin's to choose, so a migrated blob must not
	// leave the observer without one.
	if plan.Env["DUO_AGY_LOG_FILE"] == "" {
		t.Error("no log file for the adapter to read")
	}
}

// TestOwnSpellingWinsOverTheSharedOne: a blob this plugin wrote carries its own
// field, and re-preparing it must not lose the conversation.
func TestOwnSpellingWinsOverTheSharedOne(t *testing.T) {
	plan := prepare(t, driver.LaunchRequest{
		BaseCommand: "agy",
		State:       []byte(`{"conversationId":"conv-own","sessionId":"conv-shared"}`),
	})
	if plan.Env["DUO_AGY_CONVERSATION_ID"] != "conv-own" {
		t.Errorf("conversation = %q, want the driver's own field", plan.Env["DUO_AGY_CONVERSATION_ID"])
	}
	// What prepare writes back must be in this driver's own spelling, so a later
	// resume is unambiguous.
	if !strings.Contains(string(plan.State), `"conversationId":"conv-own"`) {
		t.Errorf("state = %s, want conversationId", plan.State)
	}
	if strings.Contains(string(plan.State), "sessionId") {
		t.Errorf("state = %s, must not write the shared spelling", plan.State)
	}
}

// TestBridgeEraBlobKeepsItsLogFile covers the shape Duo's own bridge wrote during
// the transition: the identity is empty but the adapter still needs the log file it
// was told to use, or it re-reads the wrong file on the next launch.
func TestBridgeEraBlobKeepsItsLogFile(t *testing.T) {
	plan := prepare(t, driver.LaunchRequest{
		Agent:       "Austin",
		BaseCommand: "agy",
		State:       []byte(`{"logFile":"/tmp/duo-agy-austin.log"}`),
	})
	if plan.Env["DUO_AGY_LOG_FILE"] != "/tmp/duo-agy-austin.log" {
		t.Errorf("log file = %q, want the one the previous launch used", plan.Env["DUO_AGY_LOG_FILE"])
	}
	if strings.Contains(plan.Command, "$DUO_AGY_CONVERSATION_ID") {
		t.Errorf("an empty identity was sent to agy: %s", plan.Command)
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
	var h driver.Handler = New(nil)
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
