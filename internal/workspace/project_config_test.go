package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/protocol"
)

func TestEnsureGitIgnoreUsesInfoExcludeAndLeavesGitignoreAlone(t *testing.T) {
	tempDir := t.TempDir()
	run(t, tempDir, "git", "init", "-q")

	if err := EnsureGitIgnore(tempDir); err != nil {
		t.Fatalf("EnsureGitIgnore failed: %v", err)
	}

	// The user's .gitignore must not be created or modified: it is tracked, so
	// writing to it would dirty the repository the user is working in.
	if _, err := os.Stat(filepath.Join(tempDir, ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf("expected no .gitignore to be created, stat err = %v", err)
	}

	exclude := filepath.Join(tempDir, ".git", "info", "exclude")
	content, err := os.ReadFile(exclude)
	if err != nil {
		t.Fatalf("expected .git/info/exclude to be written: %v", err)
	}
	if !strings.Contains(string(content), ".duo/") {
		t.Fatalf("expected .duo/ in info/exclude, got %q", string(content))
	}

	// Idempotent: a second call must not duplicate the entry.
	if err := EnsureGitIgnore(tempDir); err != nil {
		t.Fatalf("second EnsureGitIgnore failed: %v", err)
	}
	content2, _ := os.ReadFile(exclude)
	if got := strings.Count(string(content2), ".duo/"); got != 1 {
		t.Fatalf("expected exactly one .duo/ entry, got %d: %q", got, string(content2))
	}
}

// A user's own untracked .gitignore is real uncommitted work. It must read as
// dirty, otherwise delivery would fast-forward over it and silently discard it.
func TestUntrackedGitignoreCountsAsDirty(t *testing.T) {
	repo := t.TempDir()
	run(t, repo, "git", "init", "-q")
	run(t, repo, "git", "config", "user.email", "t@example.com")
	run(t, repo, "git", "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "git", "add", "-A")
	run(t, repo, "git", "commit", "-m", "init")
	if err := EnsureGitIgnore(repo); err != nil {
		t.Fatal(err)
	}
	// The user writes their own .gitignore and has not committed it.
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("node_modules\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	porcelain, err := gitOutput(context.Background(), repo, "status", "--porcelain")
	if err != nil {
		t.Fatalf("git status failed: %v", err)
	}
	if !strings.Contains(porcelain, ".gitignore") {
		t.Fatalf("porcelain should report the untracked .gitignore, got %q", porcelain)
	}
	if strings.TrimSpace(porcelain) == "" {
		t.Fatal("untracked user .gitignore must be treated as dirty, not ignored")
	}
}

func TestSaveProjectConfig(t *testing.T) {
	tempDir := t.TempDir()

	agentDrivers := map[protocol.AgentID]string{
		protocol.Austin: "agy",
		protocol.Tony:   "agy",
	}
	agentModels := map[protocol.AgentID]string{
		protocol.Austin: "google/gemini-3.8-flash-high",
		protocol.Tony:   "google/gemini-3.8-flash-high",
	}

	if err := SaveProjectConfig(tempDir, "agy", agentDrivers, agentModels); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}

	// Check .duo/config.json
	cfgData, err := os.ReadFile(filepath.Join(tempDir, ".duo", "config.json"))
	if err != nil {
		t.Fatalf("failed to read .duo/config.json: %v", err)
	}

	var parsed ProjectConfigFile
	if err := json.Unmarshal(cfgData, &parsed); err != nil {
		t.Fatalf("failed to parse json: %v", err)
	}

	if parsed.Driver != "" {
		t.Fatalf("expected top-level driver to be empty when per-agent drivers are set, got %s", parsed.Driver)
	}
	// The model is stored as it was chosen. This used to be rewritten here for one
	// driver by name, which meant saving a model required knowing which driver it
	// belonged to; folding a reference into the form a driver takes now happens when
	// that driver is resolved, from the format it declares.
	if parsed.Agents["austin"].Model != "google/gemini-3.8-flash-high" {
		t.Fatalf("expected austin model to be stored verbatim, got %s", parsed.Agents["austin"].Model)
	}
	if parsed.Agents["tony"].Driver != "agy" {
		t.Fatalf("expected tony driver agy, got %s", parsed.Agents["tony"].Driver)
	}

	// When per-agent drivers are not specified, the top-level driver is kept
	tempDir2 := t.TempDir()
	if err := SaveProjectConfig(tempDir2, "agy", nil, map[protocol.AgentID]string{protocol.Austin: "google/gemini-3.8-flash-high"}); err != nil {
		t.Fatalf("SaveProjectConfig failed: %v", err)
	}
	cfgData2, err := os.ReadFile(filepath.Join(tempDir2, ".duo", "config.json"))
	if err != nil {
		t.Fatalf("failed to read .duo/config.json: %v", err)
	}
	var parsed2 ProjectConfigFile
	if err := json.Unmarshal(cfgData2, &parsed2); err != nil {
		t.Fatalf("failed to parse json: %v", err)
	}
	if parsed2.Driver != "agy" {
		t.Fatalf("expected driver agy, got %s", parsed2.Driver)
	}
	if parsed2.Agents["austin"].Model != "google/gemini-3.8-flash-high" {
		t.Fatalf("expected austin model to be stored verbatim, got %s", parsed2.Agents["austin"].Model)
	}
}

// A model id belongs to the CLI it was chosen from. Switching drivers must drop
// it, otherwise one agent CLI's model id is handed to another and the agent dies
// at startup with "Model not found".
func TestSaveProjectConfigDropsModelWhenDriverChanges(t *testing.T) {
	dir := t.TempDir()
	// An earlier agy run persisted agy's bare model id.
	initial := `{"agents":{"austin":{"driver":"agy","model":"gemini-3.8-flash-high","command":"agy --legacy","thinking":"high","custom":"keep"}}}`
	if err := os.WriteFile(filepath.Join(dir, ".duo", "config.json"), []byte(initial), 0o644); err != nil {
		os.MkdirAll(filepath.Join(dir, ".duo"), 0o755)
		if err := os.WriteFile(filepath.Join(dir, ".duo", "config.json"), []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// The same stale id is still what config resolution produced for austin.
	if err := SaveProjectConfig(dir, "", map[protocol.AgentID]string{protocol.Austin: "opencode"},
		map[protocol.AgentID]string{protocol.Austin: "gemini-3.8-flash-high"}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".duo", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed ProjectConfigFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	austin := parsed.Agents["austin"]
	if austin.Driver != "opencode" {
		t.Fatalf("driver = %q, want opencode", austin.Driver)
	}
	if austin.Model != "" {
		t.Fatalf("model = %q, want it dropped: it belonged to the previous driver", austin.Model)
	}
	var raw struct {
		Agents map[string]map[string]json.RawMessage `json:"agents"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	fields := raw.Agents["austin"]
	for _, key := range []string{"command", "thinking"} {
		if _, ok := fields[key]; ok {
			t.Errorf("%s = %s, want previous-driver setting removed", key, fields[key])
		}
	}
	if string(fields["custom"]) != `"keep"` {
		t.Errorf("unknown field custom = %s, want it preserved", fields["custom"])
	}
}

// The same driver keeps its model: the drop must only happen on a real change.
func TestSaveProjectConfigKeepsModelWhenDriverUnchanged(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".duo"), 0o755); err != nil {
		t.Fatal(err)
	}
	initial := `{"agents":{"austin":{"driver":"opencode","command":"opencode --custom","thinking":"high"}}}`
	if err := os.WriteFile(filepath.Join(dir, ".duo", "config.json"), []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveProjectConfig(dir, "", map[protocol.AgentID]string{protocol.Austin: "opencode"},
		map[protocol.AgentID]string{protocol.Austin: "opencode/claude-sonnet-4-6"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".duo", "config.json"))
	var parsed ProjectConfigFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if got := parsed.Agents["austin"].Model; got != "opencode/claude-sonnet-4-6" {
		t.Fatalf("model = %q, want it kept", got)
	}
	var raw struct {
		Agents map[string]map[string]json.RawMessage `json:"agents"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	fields := raw.Agents["austin"]
	if string(fields["command"]) != `"opencode --custom"` || string(fields["thinking"]) != `"high"` {
		t.Errorf("same-driver command/thinking not preserved: %s", data)
	}
}

func TestSaveProjectConfigPreservesOtherProjectOptions(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".duo", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	initial := `{"mode":"goal","harness":{"enabled":false},"x-extension":{"value":7},"agents":{"austin":{"driver":"codex","model":"gpt-6-sol","command":"codex --profile cli-proxy","thinking":"high","x-agent-option":"keep"}}}`
	if err := os.WriteFile(configPath, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := SaveProjectConfig(dir, "", map[protocol.AgentID]string{protocol.Austin: "codex"}, map[protocol.AgentID]string{protocol.Austin: "gpt-5.6-terra"}); err != nil {
			t.Fatalf("SaveProjectConfig failed: %v", err)
		}
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"mode": `"goal"`, "harness": `{"enabled":false}`, "x-extension": `{"value":7}`,
	} {
		var compact bytes.Buffer
		if err := json.Compact(&compact, got[key]); err != nil || compact.String() != want {
			t.Errorf("%s = %s, want %s (compact error %v)", key, got[key], want, err)
		}
	}
	var agents map[string]map[string]json.RawMessage
	if err := json.Unmarshal(got["agents"], &agents); err != nil {
		t.Fatal(err)
	}
	austin := agents["austin"]
	for key, want := range map[string]string{
		"model": `"gpt-5.6-terra"`, "command": `"codex --profile cli-proxy"`,
		"thinking": `"high"`, "x-agent-option": `"keep"`,
	} {
		if string(austin[key]) != want {
			t.Errorf("Austin %s = %s, want %s", key, austin[key], want)
		}
	}
}

func TestSaveProjectConfigRejectsInvalidExistingConfigWithoutReplacingIt(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".duo", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	initial := []byte(`{"agents":`)
	if err := os.WriteFile(configPath, initial, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveProjectConfig(dir, "codex", nil, nil); err == nil {
		t.Fatal("SaveProjectConfig accepted malformed existing JSON")
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(initial) {
		t.Fatalf("malformed config was changed: %q", after)
	}
}

func TestSaveProjectConfigReturnsExistingConfigReadError(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".duo", "config.json")
	if err := os.MkdirAll(configPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SaveProjectConfig(dir, "codex", nil, nil); err == nil {
		t.Fatal("SaveProjectConfig ignored an error reading existing config")
	}
	if st, err := os.Stat(configPath); err != nil || !st.IsDir() {
		t.Fatalf("existing config path was replaced: stat=%v err=%v", st, err)
	}
}

func TestSaveProjectConfigHandlesNullAgents(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".duo", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"agents":null}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveProjectConfig(dir, "", map[protocol.AgentID]string{protocol.Austin: "codex"}, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var got ProjectConfigFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Agents["austin"].Driver != "codex" {
		t.Fatalf("Austin driver = %q, want codex", got.Agents["austin"].Driver)
	}
}

func TestSaveProjectLanguageWritesPreservesAndRemoves(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".duo", "config.json")
	initial := `{"driver":"codex","x-extension":{"value":7},"language":"en"}`
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveProjectLanguage(dir, "zh"); err != nil {
		t.Fatalf("SaveProjectLanguage failed: %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if string(got["language"]) != `"zh"` {
		t.Fatalf("language = %s, want \"zh\"", got["language"])
	}
	for key, want := range map[string]string{
		"driver": `"codex"`, "x-extension": `{"value":7}`,
	} {
		var compact bytes.Buffer
		if err := json.Compact(&compact, got[key]); err != nil || compact.String() != want {
			t.Errorf("%s not preserved (compact %s, err %v)", key, compact.String(), err)
		}
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("config must end with a newline: %q", data)
	}
	if _, err := os.Stat(configPath + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temp file left behind by the atomic write")
	}

	// An empty language removes the key; it must never persist "".
	if err := SaveProjectLanguage(dir, ""); err != nil {
		t.Fatalf("clearing language failed: %v", err)
	}
	data, _ = os.ReadFile(configPath)
	got = nil
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["language"]; ok {
		t.Fatalf("language key must be removed, got %s", data)
	}
}

func TestSaveProjectThinkingPreservesDriverModelAndOtherKeys(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".duo", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{
  "language": "zh",
  "agents": {
    "austin": {"driver": "opencode", "model": "opencode/gpt-5", "thinking": "low", "custom": "keep"},
    "tony": {"driver": "pi", "model": "anthropic/claude", "thinking": "medium"}
  }
}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := SaveProjectThinking(dir, protocol.Austin, "high"); err != nil {
		t.Fatalf("SaveProjectThinking: %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Language string `json:"language"`
		Agents   map[string]struct {
			Driver   string `json:"driver"`
			Model    string `json:"model"`
			Thinking string `json:"thinking"`
			Custom   string `json:"custom"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse config: %v\n%s", err, data)
	}
	austin := cfg.Agents["austin"]
	if austin.Driver != "opencode" || austin.Model != "opencode/gpt-5" || austin.Thinking != "high" || austin.Custom != "keep" {
		t.Fatalf("Austin entry was not preserved: %+v\n%s", austin, data)
	}
	if cfg.Agents["tony"].Thinking != "medium" || cfg.Language != "zh" {
		t.Fatalf("other entries changed: %+v language=%q\n%s", cfg.Agents, cfg.Language, data)
	}

	if err := SaveProjectThinking(dir, protocol.Austin, ""); err != nil {
		t.Fatalf("clear thinking: %v", err)
	}
	data, _ = os.ReadFile(configPath)
	if strings.Contains(string(data), `"thinking": "high"`) || strings.Contains(string(data), `"thinking":"high"`) {
		t.Fatalf("empty thinking did not remove the Austin key:\n%s", data)
	}
}

func TestSaveProjectLanguageMutualPreservationWithSaveProjectConfig(t *testing.T) {
	dir := t.TempDir()
	if err := SaveProjectLanguage(dir, "zh"); err != nil {
		t.Fatal(err)
	}
	if err := SaveProjectConfig(dir, "codex", map[protocol.AgentID]string{protocol.Austin: "codex"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := SaveProjectLanguage(dir, "en"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".duo", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if string(got["language"]) != `"en"` {
		t.Fatalf("language = %s, want \"en\"", got["language"])
	}
	var agents map[string]map[string]json.RawMessage
	if err := json.Unmarshal(got["agents"], &agents); err != nil {
		t.Fatal(err)
	}
	if string(agents["austin"]["driver"]) != `"codex"` {
		t.Fatalf("Austin driver lost by SaveProjectLanguage: %s", data)
	}
}

func TestSaveProjectLanguageRejectsMalformedExistingConfigWithoutReplacingIt(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".duo", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	initial := []byte(`{"language":`)
	if err := os.WriteFile(configPath, initial, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveProjectLanguage(dir, "zh"); err == nil {
		t.Fatal("SaveProjectLanguage accepted malformed existing JSON")
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(initial) {
		t.Fatalf("malformed config was changed: %q", after)
	}
}
