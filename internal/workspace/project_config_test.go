package workspace

import (
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
	initial := `{"agents":{"austin":{"driver":"agy","model":"gemini-3.8-flash-high"}}}`
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
}

// The same driver keeps its model: the drop must only happen on a real change.
func TestSaveProjectConfigKeepsModelWhenDriverUnchanged(t *testing.T) {
	dir := t.TempDir()
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
}
