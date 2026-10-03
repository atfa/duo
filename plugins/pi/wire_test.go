package pi_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/plugins/pi"
)

// TestPluginBinarySpeaksTheProtocol drives the shipped executable through the same
// client Duo Core uses, which is the only test that proves both ends of the protocol
// agree. Everything else here calls the Handler directly and so cannot catch a
// disagreement about the wire format — the class of bug where a field is written on
// one side and never read on the other.
func TestPluginBinarySpeaksTheProtocol(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess test in short mode")
	}
	bin := t.TempDir() + "/duo-plugin-pi"
	build := exec.Command("go", "build", "-o", bin, "github.com/atfa/duo/cmd/duo-plugin-pi")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the pi plugin: %v\n%s", err, out)
	}

	ctx := context.Background()
	c, err := driver.Start(ctx, pi.Name, bin, nil, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Close()

	man, err := c.Describe(ctx)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if man.Name != pi.Name || man.Protocol != driver.ProtocolVersion {
		t.Fatalf("manifest = %+v", man)
	}
	// The manifest must survive the round trip through JSON, or a driver would
	// behave differently in-process and out of it.
	if man.Capabilities.Resume != driver.ResumeClient || man.Capabilities.Bridge != driver.BridgeAgent {
		t.Errorf("capabilities lost in transit: %+v", man.Capabilities)
	}
	if man.ModelReference != driver.ModelQualified {
		t.Errorf("modelReference = %q", man.ModelReference)
	}

	// prepare must replay a stored identity and publish it to the agent process.
	plan, err := c.Prepare(ctx, driver.LaunchRequest{
		Agent: "Austin", BaseCommand: "pi", Model: "anthropic/claude-sonnet-4-6",
		State: json.RawMessage(`{"sessionId":"abc"}`),
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if plan.Env["DUO_PI_SESSION_ID"] != "abc" {
		t.Errorf("the identity did not reach the agent process: %v", plan.Env)
	}
	if !strings.Contains(plan.Command, "--model") {
		t.Errorf("model missing from %q", plan.Command)
	}
	// An unset model must add no flag at all: "--model ''" is a flag whose argument
	// is the empty string, which pi rejects.
	bare, err := c.Prepare(ctx, driver.LaunchRequest{Agent: "Austin", BaseCommand: "pi"})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	for _, flag := range []string{"--model", "--thinking"} {
		if strings.Contains(bare.Command, flag) {
			t.Errorf("%s was added for an unset value: %q", flag, bare.Command)
		}
	}

	levels, err := c.Thinking(ctx)
	if err != nil {
		t.Fatalf("thinking: %v", err)
	}
	if len(levels.Levels) == 0 {
		t.Error("thinking returned no levels for a driver that declares the capability")
	}

	// The model catalog needs the real CLI. When pi is absent, probe must say so and
	// models must fail with that reason rather than hanging or reporting nothing.
	probe, err := c.Probe(ctx)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !probe.Available {
		if probe.Reason == "" {
			t.Error("an unavailable probe must say why")
		}
		if _, err := c.Models(ctx); err == nil {
			t.Error("listing models without the CLI must fail, not return an empty picker")
		}
		return
	}
	list, err := c.Models(ctx)
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	if len(list.Models) == 0 {
		t.Error("the catalog is empty on a machine where pi is installed")
	}
}
