package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestClientRoundTrip(t *testing.T) {
	ctx := context.Background()
	c := startStub(t, thinkingStub{})

	manifest, err := c.Describe(ctx)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if manifest.Name != "stub" || manifest.Protocol != ProtocolVersion {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
	if manifest.Capabilities.Resume != ResumeClient {
		t.Fatalf("resume = %q, want %q", manifest.Capabilities.Resume, ResumeClient)
	}

	probe, err := c.Probe(ctx)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !probe.Available || probe.AgentPath != "/bin/stub" {
		t.Fatalf("unexpected probe: %+v", probe)
	}

	plan, err := c.Prepare(ctx, LaunchRequest{BaseCommand: "stub", Model: "acme/one", State: json.RawMessage(`{"sessionId":"abc"}`)})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !strings.Contains(plan.Command, "--session-id abc") {
		t.Fatalf("prepare did not replay the stored identity: %q", plan.Command)
	}
	if plan.SessionIdentity != "abc" || string(plan.State) != `{"sessionId":"abc"}` {
		t.Fatalf("unexpected plan: %+v", plan)
	}

	list, err := c.Models(ctx)
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	if len(list.Models) != 1 || list.Models[0].ID != "one" {
		t.Fatalf("unexpected catalog: %+v", list)
	}

	levels, err := c.Thinking(ctx)
	if err != nil {
		t.Fatalf("thinking: %v", err)
	}
	if strings.Join(levels.Levels, ",") != "off,high" {
		t.Fatalf("unexpected levels: %+v", levels)
	}
}

// TestUnsupportedMethod checks that a method a plugin did not implement is
// reported as unsupported, not as a crash: Core relies on the distinction to
// know whether a capability check was wrong or the plugin is broken.
func TestUnsupportedMethod(t *testing.T) {
	c := startStub(t, echoHandler{})
	_, err := c.Thinking(context.Background())
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("thinking on a plugin without it: %v, want ErrUnsupported", err)
	}
}

// TestMalformedOutput checks that a plugin printing garbage on stdout does not
// take the session down: the call times out and reports why, and the plugin's
// own stderr carries the detail.
func TestMalformedOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	c := startRaw(t, peerSilent)
	err := c.Call(ctx, MethodDescribe, nil, &Manifest{})
	if err == nil {
		t.Fatal("expected a timeout for a silent plugin")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v, want a timeout", err)
	}
}

// TestPluginCrash checks that a plugin that dies mid-call is reported as a crash
// rather than as a timeout, so Core can say "the plugin exited" instead of
// leaving the operator waiting.
func TestPluginCrash(t *testing.T) {
	c := startRaw(t, peerDead)
	_, err := c.Describe(context.Background())
	if !errors.Is(err, ErrCrashed) {
		t.Fatalf("describe against a dead plugin: %v, want ErrCrashed", err)
	}
}

// TestPrepareWithoutCommand checks Core's own guard: a plan with no command is
// unusable, and Core refuses it at the boundary instead of launching an empty
// shell line.
func TestPrepareWithoutCommand(t *testing.T) {
	c := startStub(t, echoHandler{})
	_, err := c.Prepare(context.Background(), LaunchRequest{BaseCommand: "stub"})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	bad := startStub(t, emptyCommandHandler{})
	if _, err := bad.Prepare(context.Background(), LaunchRequest{}); err == nil {
		t.Fatal("prepare with an empty command must be refused")
	}
}

func TestValidateManifest(t *testing.T) {
	base := func() *Manifest {
		return &Manifest{
			Protocol:       ProtocolVersion,
			Name:           "x",
			ModelReference: ModelQualified,
			Capabilities:   Capabilities{Resume: ResumeClient, Bridge: BridgeAgent},
		}
	}
	if err := ValidateManifest(base()); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	cases := map[string]func(*Manifest){
		"wrong protocol":     func(m *Manifest) { m.Protocol = 99 },
		"no name":            func(m *Manifest) { m.Name = "" },
		"no resume":          func(m *Manifest) { m.Capabilities.Resume = "" },
		"unknown resume":     func(m *Manifest) { m.Capabilities.Resume = "magic" },
		"no bridge":          func(m *Manifest) { m.Capabilities.Bridge = "" },
		"unknown bridge":     func(m *Manifest) { m.Capabilities.Bridge = "carrier-pigeon" },
		"no model reference": func(m *Manifest) { m.ModelReference = "" },
	}
	for name, mutate := range cases {
		m := base()
		mutate(m)
		if err := ValidateManifest(m); err == nil {
			t.Errorf("%s: manifest accepted but must be rejected", name)
		}
	}
}

// TestServeRejectsProtocolMismatch checks a plugin refuses a request from a Core
// speaking another protocol version rather than answering with fields it may
// interpret differently.
func TestServeRejectsProtocolMismatch(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader(`{"protocol":2,"id":1,"method":"describe"}` + "\n")
	if err := Serve(in, &out, echoHandler{}); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var resp Response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", out.String(), err)
	}
	if resp.OK || resp.Error == nil || resp.Error.Code != CodeProtocol {
		t.Fatalf("expected a protocol_mismatch error, got %s", out.String())
	}
}

// TestServeRejectsMalformedRequest checks one bad line does not desynchronise
// the stream: the plugin answers it and keeps serving.
func TestServeRejectsMalformedRequest(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader("not json\n" + `{"protocol":1,"id":2,"method":"describe"}` + "\n")
	if err := Serve(in, &out, echoHandler{}); err != nil {
		t.Fatalf("serve: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want one reply per request, got %d: %q", len(lines), out.String())
	}
	if !strings.Contains(lines[0], CodeInvalid) {
		t.Fatalf("first reply should reject the malformed request: %q", lines[0])
	}
	if !strings.Contains(lines[1], `"ok":true`) {
		t.Fatalf("second reply should succeed: %q", lines[1])
	}
}

// --- test handlers -------------------------------------------------------

type echoHandler struct{}

func (echoHandler) Describe() (*Manifest, error) {
	return &Manifest{
		Protocol:       ProtocolVersion,
		Name:           "stub",
		Version:        "1.0.0",
		ModelReference: ModelQualified,
		Capabilities:   Capabilities{Resume: ResumeClient, Models: true, Bridge: BridgeAgent},
	}, nil
}

func (echoHandler) Probe() (*ProbeResult, error) {
	return &ProbeResult{Available: true, AgentPath: "/bin/stub"}, nil
}

func (echoHandler) Prepare(req LaunchRequest) (*LaunchPlan, error) {
	command := strings.TrimSpace(req.BaseCommand)
	// Replay a stored identity, which is what ResumeClient means: the plugin
	// owns the id and hands it back to the agent on the next launch.
	var prior struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(req.State, &prior)
	if prior.SessionID != "" {
		command += " --session-id " + prior.SessionID
	}
	if model := strings.TrimSpace(req.Model); model != "" && command != "" {
		command += " --model " + model
	}
	return &LaunchPlan{
		Command:         command,
		State:           req.State,
		SessionIdentity: prior.SessionID,
	}, nil
}

func (echoHandler) Models() (*ModelList, error) {
	return &ModelList{Models: []Model{{Provider: "acme", ID: "one"}}}, nil
}

// thinkingStub is echoHandler plus the optional thinking method, which is what a
// plugin declaring Capabilities.Thinking must implement.
type thinkingStub struct{ echoHandler }

func (thinkingStub) Thinking() (*ThinkingOptions, error) {
	return &ThinkingOptions{Levels: []string{"off", "high"}, Default: "off"}, nil
}

type emptyCommandHandler struct{}

func (emptyCommandHandler) Describe() (*Manifest, error) { return echoHandler{}.Describe() }
func (emptyCommandHandler) Probe() (*ProbeResult, error) { return echoHandler{}.Probe() }
func (emptyCommandHandler) Prepare(LaunchRequest) (*LaunchPlan, error) {
	return &LaunchPlan{}, nil
}

// --- plumbing ------------------------------------------------------------

// peerMode selects how a stub plugin behaves, so the tests can cover the
// failure paths that matter to Core: silence, death, and garbage.
type peerMode int

const (
	peerSilent peerMode = iota
	peerDead
)

// startRaw starts a client whose peer is a stub plugin of the given mode.
func startRaw(t *testing.T, mode peerMode) *Client {
	t.Helper()
	coreToPluginR, coreToPluginW := io.Pipe()
	pluginToCoreR, pluginToCoreW := io.Pipe()
	go func() {
		switch mode {
		case peerSilent:
			select {}
		case peerDead:
			_ = pluginToCoreW.Close()
		}
	}()
	// Drain the plugin's stdin the way a real Serve loop does, so Core's write
	// never blocks.
	go func() { _, _ = io.Copy(io.Discard, coreToPluginR) }()

	c := &Client{
		name:    "raw",
		stdin:   coreToPluginW,
		pending: make(map[int64]chan *Response),
		done:    make(chan struct{}),
	}
	go c.readLoop(pluginToCoreR)
	t.Cleanup(func() {
		_ = coreToPluginW.Close()
		_ = pluginToCoreW.Close()
	})
	return c
}

// startStub starts a client talking to an in-process Serve loop.
func startStub(t *testing.T, h Handler) *Client {
	t.Helper()
	coreToPluginR, coreToPluginW := io.Pipe()
	pluginToCoreR, pluginToCoreW := io.Pipe()
	go func() { _ = Serve(coreToPluginR, pluginToCoreW, h) }()
	c := &Client{
		name:    "stub",
		stdin:   coreToPluginW,
		pending: make(map[int64]chan *Response),
		done:    make(chan struct{}),
	}
	go c.readLoop(pluginToCoreR)
	t.Cleanup(func() {
		_ = coreToPluginW.Close()
		_ = pluginToCoreW.Close()
	})
	return c
}

// TestStartRealProcess drives the shipped reference plugin as a real
// subprocess, which is the only way to prove the framing, the pipes and the
// shutdown handshake work outside the test harness.
func TestStartRealProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess test in short mode")
	}
	bin := buildExamplePlugin(t)
	c, err := Start(context.Background(), "example", bin, nil, nil)
	if err != nil {
		t.Fatalf("start plugin: %v", err)
	}
	defer c.Close()

	ctx := context.Background()
	manifest, err := c.Describe(ctx)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if manifest.Name != "example" || manifest.Protocol != ProtocolVersion {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
	// The reference plugin drives no real agent, so probe must say so instead of
	// failing: an unavailable CLI is a reportable state, not an error.
	probe, err := c.Probe(ctx)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if probe.Available {
		t.Skip("example-agent is installed on this machine")
	}
	if probe.Reason == "" {
		t.Fatal("an unavailable probe must say why")
	}

	first, err := c.Prepare(ctx, LaunchRequest{BaseCommand: "example-agent"})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if strings.Contains(first.Command, "--session-id") {
		t.Fatalf("a first run must not be handed an identity the agent never saw: %q", first.Command)
	}
	var minted struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(first.State, &minted); err != nil || minted.SessionID == "" {
		t.Fatalf("prepare returned no usable resume state: %s", first.State)
	}
	second, err := c.Prepare(ctx, LaunchRequest{BaseCommand: "example-agent", State: first.State})
	if err != nil {
		t.Fatalf("prepare resume: %v", err)
	}
	if !strings.Contains(second.Command, minted.SessionID) {
		t.Fatalf("resume did not replay the stored identity: %q", second.Command)
	}

	// A driver declaring Models must answer the models call.
	list, err := c.Models(ctx)
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	if len(list.Models) == 0 {
		t.Fatal("models returned an empty catalog for a driver that declares the capability")
	}
}

// buildExamplePlugin compiles the reference plugin once per test run.
func buildExamplePlugin(t *testing.T) string {
	t.Helper()
	bin := t.TempDir() + "/duo-plugin-example"
	cmd := exec.Command("go", "build", "-o", bin, "github.com/atfa/duo/cmd/duo-plugin-example")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build reference plugin: %v\n%s", err, out)
	}
	return bin
}

// TestManifestRejectsUnservableLiveSteering pins the rule that made the bridge
// field an ownership enum rather than a transport name: only an endpoint inside
// the agent process can receive an injected prompt. A plugin-owned endpoint that
// claims live steering would look healthy in describe and then silently swallow
// every human prompt, harness nudge and steer, which is exactly the failure the
// agy driver would have hit.
func TestManifestRejectsUnservableLiveSteering(t *testing.T) {
	base := func() *Manifest {
		return &Manifest{
			Protocol:       ProtocolVersion,
			Name:           "x",
			ModelReference: ModelQualified,
			Capabilities: Capabilities{
				Resume:       ResumeClient,
				Bridge:       BridgePlugin,
				LiveSteering: true,
				PTYFallback:  true,
			},
		}
	}
	if err := ValidateManifest(base()); err == nil {
		t.Fatal("a plugin-owned bridge claiming liveSteering must be rejected")
	}
	ok := base()
	ok.Capabilities.LiveSteering = false
	if err := ValidateManifest(ok); err != nil {
		t.Fatalf("a plugin-owned bridge that does not claim live steering is valid: %v", err)
	}
	agent := base()
	agent.Capabilities.Bridge = BridgeAgent
	if err := ValidateManifest(agent); err != nil {
		t.Fatalf("an agent-owned bridge claiming live steering is valid: %v", err)
	}
}

// TestSupervisedRestartsACrashedPlugin is the crash policy: a plugin process that
// dies must cost a retry, not the session. The failure is reported once so the
// reason reaches the operator, and the next call succeeds against a fresh process.
func TestSupervisedRestartsACrashedPlugin(t *testing.T) {
	var spawned int
	open := func(context.Context, string, []string) (*Client, error) {
		spawned++
		return startRaw(t, peerDead), nil
	}
	var reported []error
	s := NewSupervised("flaky", SupervisedOptions{
		Path:   "/nonexistent/duo-plugin-flaky",
		Open:   open,
		Report: func(err error) { reported = append(reported, err) },
	})
	defer s.Close()

	// A first describe fails, the wrapper drops the dead process, and Describe
	// reports the crash.
	if _, err := s.Describe(context.Background()); err == nil {
		t.Fatal("describe against a plugin that never answers must fail")
	}
	if len(reported) == 0 {
		t.Fatal("a crashed plugin must be reported, not retried in silence")
	}

	// Now let the replacement process answer, and prove the wrapper reconnects.
	good := startStub(t, thinkingStub{})
	open = func(context.Context, string, []string) (*Client, error) {
		spawned++
		return good, nil
	}
	s.open = open
	man, err := s.Describe(context.Background())
	if err != nil {
		t.Fatalf("describe after a crash must respawn and succeed: %v", err)
	}
	if man.Name != "stub" {
		t.Fatalf("unexpected manifest %+v", man)
	}
	if spawned < 2 {
		t.Fatalf("spawned %d processes; a crash must force a new one", spawned)
	}
}

// TestBuiltinRejectsAnInvalidManifestOnce checks a built-in driver that describes
// itself badly fails at describe rather than at some later, confusing point.
func TestBuiltinRejectsAnInvalidManifest(t *testing.T) {
	b := NewBuiltin("broken", brokenDriver{})
	if _, err := b.Describe(context.Background()); err == nil {
		t.Fatal("a builtin with an invalid manifest must be refused")
	}
	// The failure must be repeatable: caching a broken result would hide it.
	if _, err := b.Describe(context.Background()); err == nil {
		t.Fatal("the refusal must be repeatable, not cached away")
	}
}

// TestBuiltinRefusesUndeclaredOptionalMethods checks a driver cannot answer a
// method its interface does not implement: Core must see ErrUnsupported so a
// capability check and the implementation cannot drift apart silently.
func TestBuiltinRefusesUndeclaredOptionalMethods(t *testing.T) {
	b := NewBuiltin("stub", echoHandler{})
	if _, err := b.Thinking(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("thinking on a builtin without it: %v, want ErrUnsupported", err)
	}
	if _, err := b.Models(context.Background()); err != nil {
		t.Fatalf("models on a builtin that implements it: %v", err)
	}
	b.Close()
}

type brokenDriver struct{ noopDriver }

func (brokenDriver) Describe() (*Manifest, error) {
	return &Manifest{Protocol: 99, Name: "broken"}, nil
}
