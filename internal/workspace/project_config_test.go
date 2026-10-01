package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/atfa/duo/internal/protocol"
)

func TestEnsureGitIgnore(t *testing.T) {
	tempDir := t.TempDir()

	// 1. When .gitignore does not exist
	if err := EnsureGitIgnore(tempDir); err != nil {
		t.Fatalf("EnsureGitIgnore failed: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(tempDir, ".gitignore"))
	if err != nil {
		t.Fatalf("failed reading .gitignore: %v", err)
	}
	if string(content) != ".duo/\n" {
		t.Fatalf("expected .duo/\\n, got %q", string(content))
	}

	// 2. Call again, should not duplicate
	if err := EnsureGitIgnore(tempDir); err != nil {
		t.Fatalf("second EnsureGitIgnore failed: %v", err)
	}
	content2, err := os.ReadFile(filepath.Join(tempDir, ".gitignore"))
	if err != nil {
		t.Fatalf("failed reading .gitignore: %v", err)
	}
	if string(content2) != ".duo/\n" {
		t.Fatalf("expected no duplicates, got %q", string(content2))
	}

	// 3. Existing .gitignore with other content
	tempDir2 := t.TempDir()
	_ = os.WriteFile(filepath.Join(tempDir2, ".gitignore"), []byte("node_modules\n.DS_Store"), 0644)
	if err := EnsureGitIgnore(tempDir2); err != nil {
		t.Fatalf("EnsureGitIgnore failed on existing: %v", err)
	}
	content3, err := os.ReadFile(filepath.Join(tempDir2, ".gitignore"))
	if err != nil {
		t.Fatalf("failed reading .gitignore: %v", err)
	}
	expected := "node_modules\n.DS_Store\n.duo/\n"
	if string(content3) != expected {
		t.Fatalf("expected %q, got %q", expected, string(content3))
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

	// Check .gitignore exists
	giContent, err := os.ReadFile(filepath.Join(tempDir, ".gitignore"))
	if err != nil || string(giContent) != ".duo/\n" {
		t.Fatalf("expected .duo/ in .gitignore, got: %s", string(giContent))
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
	if parsed.Agents["austin"].Model != "gemini-3.8-flash-high" {
		t.Fatalf("expected austin model gemini-3.8-flash-high, got %s", parsed.Agents["austin"].Model)
	}
	if parsed.Agents["tony"].Driver != "agy" {
		t.Fatalf("expected tony driver agy, got %s", parsed.Agents["tony"].Driver)
	}

	// When per-agent drivers are not specified, top-level driver is kept
	tempDir2 := t.TempDir()
	if err := SaveProjectConfig(tempDir2, "agy", nil, nil); err != nil {
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
}
