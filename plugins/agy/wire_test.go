package agy_test

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/plugins/agy"
)

// TestPluginBinarySpeaksTheProtocol drives the shipped executable through the
// same client Duo Core uses, which is the only test that proves both ends of the
// protocol agree. Everything else in this package calls the Handler directly and
// so cannot catch a disagreement about the wire format — the class of bug where a
// field is written on one side and never read on the other.
func TestPluginBinarySpeaksTheProtocol(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess test in short mode")
	}
	// Prepare writes agy's own trusted-workspaces settings; redirect them so no
	// test touches a real agy install.
	t.Setenv("GEMINI_APP_DATA_DIR", t.TempDir())

	bin := t.TempDir() + "/duo-plugin-agy"
	build := exec.Command("go", "build", "-o", bin, "github.com/atfa/duo/cmd/duo-plugin-agy")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the agy plugin: %v\n%s", err, out)
	}

	ctx := context.Background()
	c, err := driver.Start(ctx, agy.Name, bin, nil, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Close()

	man, err := c.Describe(ctx)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if man.Name != agy.Name || man.Protocol != driver.ProtocolVersion {
		t.Fatalf("manifest = %+v", man)
	}
	// The manifest must survive the round trip through JSON, or a driver would
	// behave differently in-process and out of it. These three are the ones a
	// drift would cost the most: the bridge ownership decides who may connect,
	// liveSteering decides where prompts are delivered, resume decides whether a
	// conversation is replayed or forked.
	caps := man.Capabilities
	if caps.Bridge != driver.BridgePlugin || caps.LiveSteering || caps.Resume != driver.ResumeServer {
		t.Errorf("capabilities lost in transit: %+v", caps)
	}
	if man.ModelReference != driver.ModelBare {
		t.Errorf("modelReference = %q", man.ModelReference)
	}

	// prepare must accept the shared identity spelling Core migrates into every
	// snapshot and hand it back as a replayed conversation.
	plan, err := c.Prepare(ctx, driver.LaunchRequest{
		Agent: "Austin", BaseCommand: "agy", Model: "vendor-1/model-x",
		State: json.RawMessage(`{"sessionId":"abc"}`),
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if plan.Env["DUO_AGY_CONVERSATION_ID"] != "abc" || plan.SessionIdentity != "abc" {
		t.Errorf("the identity did not reach the plan: env=%v identity=%q", plan.Env, plan.SessionIdentity)
	}
	// agy rejects a provider prefix; the bare id must be what ships.
	if !strings.Contains(plan.Command, "model-x") || strings.Contains(plan.Command, "vendor-1/") {
		t.Errorf("model not normalised to a bare id: %q", plan.Command)
	}
	// The state written back must round-trip into the next prepare, or the
	// second launch forks the conversation.
	next, err := c.Prepare(ctx, driver.LaunchRequest{Agent: "Austin", BaseCommand: "agy", State: plan.State})
	if err != nil {
		t.Fatalf("prepare with the state just written: %v", err)
	}
	if next.SessionIdentity != "abc" {
		t.Errorf("identity after one resume = %q, want %q", next.SessionIdentity, "abc")
	}

	// A first run has no conversation, and a placeholder must never be sent:
	// agy answers an identity it has never seen by opening a different one, so
	// a fabricated flag buys a misleading warning on every launch.
	bare, err := c.Prepare(ctx, driver.LaunchRequest{Agent: "Austin", BaseCommand: "agy"})
	if err != nil {
		t.Fatalf("prepare first run: %v", err)
	}
	if strings.Contains(bare.Command, "--conversation") {
		t.Errorf("first run added --conversation: %q", bare.Command)
	}
	if bare.SessionIdentity != "" {
		t.Errorf("first run reported identity %q, want none", bare.SessionIdentity)
	}

	// A first run's own state is a log file with no conversation in it — the blob
	// the Agent Adapter keeps reading the id from. It must prepare again without
	// error and without inventing a conversation.
	bridgeEra, err := c.Prepare(ctx, driver.LaunchRequest{
		Agent: "Austin", BaseCommand: "agy",
		State: json.RawMessage(`{"logFile":"/tmp/duo-agy-austin.log"}`),
	})
	if err != nil {
		t.Fatalf("prepare with a bridge-era blob: %v", err)
	}
	if strings.Contains(bridgeEra.Command, "--conversation") {
		t.Errorf("bridge-era blob added --conversation: %q", bridgeEra.Command)
	}
	if bridgeEra.Env["DUO_AGY_LOG_FILE"] != "/tmp/duo-agy-austin.log" {
		t.Errorf("log file = %q, want the one the blob carried", bridgeEra.Env["DUO_AGY_LOG_FILE"])
	}

	levels, err := c.Thinking(ctx)
	if err != nil {
		t.Fatalf("thinking: %v", err)
	}
	if len(levels.Levels) == 0 {
		t.Error("thinking returned no levels for a driver that declares the capability")
	}

	// The model catalog needs the real CLI. When agy is absent, probe must say so
	// and models must fail with that reason rather than hanging or reporting nothing.
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
		// `agy models` fetches over the network on top of the RPC round trip, so
		// the call can outlive the client's fixed deadline on a loaded machine.
		// That is an environmental delay, not a protocol failure: the parse path
		// is pinned by the unit tests, and anything else (a crash, a malformed
		// answer, a real error) must still fail here.
		if errors.Is(err, context.DeadlineExceeded) {
			t.Logf("agy models too slow on this machine, parser covered by unit tests: %v", err)
			return
		}
		t.Fatalf("models: %v", err)
	}
	if len(list.Models) == 0 {
		t.Error("the catalog is empty on a machine where agy is installed")
	}
}
