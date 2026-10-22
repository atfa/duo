package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/atfa/duo/internal/observer"
)

func TestSaveProjectObserverPreservesProjectAndAdvancedSettings(t *testing.T) {
	repo := t.TempDir()
	configPath := filepath.Join(repo, ".duo", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"language":"zh","custom":{"retain":true},"observer":{"enabled":true,"systemPrompt":"custom prompt","maxEvents":9,"future":"keep"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	settings := observer.Settings{Enabled: true, Endpoint: "http://127.0.0.1:4000/v1", Model: "exec", APIKeyEnv: "LLM_KEY", SystemPrompt: "custom prompt", MaxEvents: 9}
	if err := SaveProjectObserver(repo, settings); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Language string `json:"language"`
		Custom   struct {
			Retain bool `json:"retain"`
		} `json:"custom"`
		Observer map[string]json.RawMessage `json:"observer"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Language != "zh" || !decoded.Custom.Retain {
		t.Fatalf("unrelated settings lost: %s", data)
	}
	if string(decoded.Observer["endpoint"]) != `"http://127.0.0.1:4000/v1"` ||
		string(decoded.Observer["systemPrompt"]) != `"custom prompt"` ||
		string(decoded.Observer["maxEvents"]) != "9" || string(decoded.Observer["future"]) != `"keep"` {
		t.Fatalf("observer settings not preserved: %s", data)
	}
	if err := SaveProjectObserver(repo, observer.Settings{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	decoded.Observer = nil
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if string(decoded.Observer["enabled"]) != "false" || len(decoded.Observer) != 2 {
		t.Fatalf("unexpected section after default reset: %s", data)
	}
}

func TestSaveProjectObserverHandlesNullSection(t *testing.T) {
	repo := t.TempDir()
	configPath := filepath.Join(repo, ".duo", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"observer":null}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveProjectObserver(repo, observer.Settings{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	var observerFields map[string]json.RawMessage
	if err := json.Unmarshal(decoded["observer"], &observerFields); err != nil {
		t.Fatal(err)
	}
	if string(observerFields["enabled"]) != "false" {
		t.Fatalf("observer section = %s", decoded["observer"])
	}
}
