package opencode_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/plugins/opencode"
)

// TestPluginBinarySpeaksTheProtocol drives the shipped executable with the same
// client Duo Core uses. Calling the Handler directly cannot catch a disagreement
// about the wire format, and opencode's identity is exactly where such a
// disagreement would be invisible until a resume silently failed.
func TestPluginBinarySpeaksTheProtocol(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess test in short mode")
	}
	bin := t.TempDir() + "/duo-plugin-opencode"
	build := exec.Command("go", "build", "-o", bin, "github.com/atfa/duo/cmd/duo-plugin-opencode")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the opencode plugin: %v\n%s", err, out)
	}

	ctx := context.Background()
	c, err := driver.Start(ctx, opencode.Name, bin, nil, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Close()

	man, err := c.Describe(ctx)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if man.Name != opencode.Name || man.Protocol != driver.ProtocolVersion {
		t.Fatalf("manifest = %+v", man)
	}
	caps := man.Capabilities
	if caps.Resume != driver.ResumeServer || caps.Bridge != driver.BridgeAgent {
		t.Errorf("capabilities lost in transit: %+v", caps)
	}
	if caps.LiveSteering != true || caps.SelfReports {
		// opencode injects through promptAsync, but only attaches once its TUI has
		// a session. Both are load-bearing and a round trip must not lose either.
		t.Errorf("capabilities lost in transit: %+v", caps)
	}

	// A first run must send no session id, and must report an empty one, or the
	// rejected value gets persisted and replayed forever.
	first, err := c.Prepare(ctx, driver.LaunchRequest{Agent: "Austin", BaseCommand: opencode.Name})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if strings.Contains(first.Command, "--session") {
		t.Errorf("a first run was handed a session opencode never issued: %q", first.Command)
	}
	if first.SessionIdentity != "" {
		t.Errorf("session identity = %q, want empty on a first run", first.SessionIdentity)
	}

	// A learned id must round-trip through the blob and come back.
	resumed, err := c.Prepare(ctx, driver.LaunchRequest{
		Agent: "Austin", BaseCommand: opencode.Name,
		State: json.RawMessage(`{"sessionId":"ses_01ABC","sessionFile":"/tmp/duo-opencode-austin.session"}`),
	})
	if err != nil {
		t.Fatalf("prepare resume: %v", err)
	}
	if !strings.Contains(resumed.Command, "ses_01ABC") {
		t.Errorf("resume did not replay the identity: %q", resumed.Command)
	}
	if resumed.Env["DUO_OPENCODE_SESSION_FILE"] != "/tmp/duo-opencode-austin.session" {
		t.Errorf("the reported session file changed on resume: %v", resumed.Env)
	}

	if _, err := c.Thinking(ctx); err != nil {
		t.Fatalf("thinking: %v", err)
	}

	probe, err := c.Probe(ctx)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !probe.Available {
		if probe.Reason == "" {
			t.Error("an unavailable probe must say why")
		}
		return
	}
	if _, err := c.Models(ctx); err != nil {
		t.Fatalf("models: %v", err)
	}
}

// TestDefaultModelReadsOpencodeConfigInThePluginProcess proves the plugin owns its
// agent's configuration. Duo Core must not learn to read ~/.config/opencode, and a
// test that only exercised the Handler in-process could not show where the read
// happened.
func TestDefaultModelReadsOpencodeConfigInThePluginProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess test in short mode")
	}
	home := t.TempDir()
	configDir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"vendor/some-model"}`
	if err := os.WriteFile(filepath.Join(configDir, "opencode.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir() + "/duo-plugin-opencode"
	build := exec.Command("go", "build", "-o", bin, "github.com/atfa/duo/cmd/duo-plugin-opencode")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	ctx := context.Background()
	c, err := driver.Start(ctx, opencode.Name, bin, nil, []string{"HOME=" + home})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Close()

	man, err := c.Describe(ctx)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if man.DefaultModel != "vendor/some-model" {
		t.Errorf("defaultModel = %q, want the user's configured value", man.DefaultModel)
	}
}
