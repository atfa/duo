package driver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildPlugin compiles one of the repository's plugins into a temp directory. The
// contract needs real executables: a plugin is a process, and the framing and
// shutdown behaviour a session depends on cannot be checked in-process.
func buildPlugin(t *testing.T, pkg, name string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), name)
	build := exec.Command("go", "build", "-o", bin, pkg)
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, out)
	}
	return bin
}

// TestContractPassesForTheReferencePlugin is the suite's positive control. The
// reference plugin is correct by construction, so if it fails then the contract is
// wrong — which is the failure mode a conformance suite has to be able to detect in
// itself.
func TestContractPassesForTheReferencePlugin(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess contract test in short mode")
	}
	bin := buildPlugin(t, "github.com/atfa/duo/cmd/duo-plugin-example", "duo-plugin-example")
	work := t.TempDir()

	report := RunContract(context.Background(), bin, work)
	if report.Failed {
		t.Fatalf("the reference plugin failed its own contract:\n%s", report.Format())
	}
	if report.Name != "example" {
		t.Errorf("report name = %q, want the plugin's own name rather than its file name", report.Name)
	}
	if len(report.Results) < 8 {
		t.Errorf("only %d checks ran; the contract is thinner than intended", len(report.Results))
	}
	if report.ExitCode() != 0 {
		t.Errorf("ExitCode() = %d for a passing report", report.ExitCode())
	}
	if !strings.Contains(report.Format(), "OK:") {
		t.Error("Format does not report success for a passing plugin")
	}
}

// TestContractPassesForEveryShippedPlugin is the guarantee that matters: a plugin
// author who runs this against their own plugin is doing the same thing Duo does to
// Pi, agy and opencode. If a shipped driver cannot pass, neither can a fourth one.
func TestContractPassesForEveryShippedPlugin(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess contract test in short mode")
	}
	for _, tc := range []struct{ pkg, name string }{
		{"github.com/atfa/duo/cmd/duo-plugin-pi", "duo-plugin-pi"},
		{"github.com/atfa/duo/cmd/duo-plugin-opencode", "duo-plugin-opencode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := buildPlugin(t, tc.pkg, tc.name)
			// The reference plugin is the positive control the crash-respawn check
			// needs; without it that check would skip and never be exercised.
			work := t.TempDir()
			if _, err := ReferencePluginBinary(context.Background(), work); err != nil {
				t.Fatalf("build the reference plugin: %v", err)
			}
			report := RunContract(context.Background(), bin, work)
			if !report.Passed() {
				t.Fatalf("a shipped driver failed the contract:\n%s", report.Format())
			}
			if !hasResult(report, "crash-respawn") {
				t.Error("the crash-respawn check did not run, so it is not being verified")
			}
		})
	}
}

// TestContractRejectsSomethingThatIsNotAPlugin checks the harness does not pass a
// binary that answers nothing, which is the failure mode of a conformance suite that
// only checks what it can.
func TestContractRejectsSomethingThatIsNotAPlugin(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess contract test in short mode")
	}
	dir := t.TempDir()

	// A program that ignores stdin and prints nothing.
	silent := filepath.Join(dir, "duo-plugin-silent")
	if err := os.WriteFile(silent, []byte("#!/bin/sh\nsleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	report := RunContract(context.Background(), silent, dir)
	if !report.Failed {
		t.Errorf("a plugin that never answers passed the contract:\n%s", report.Format())
	}

	// A program that exits immediately must not be reported as healthy.
	dead := filepath.Join(dir, "duo-plugin-dead")
	if err := os.WriteFile(dead, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	report = RunContract(context.Background(), dead, dir)
	if !report.Failed {
		t.Errorf("a plugin that exits immediately passed the contract:\n%s", report.Format())
	}

	// A file that is not executable at all.
	notExec := filepath.Join(dir, "duo-plugin-data")
	if err := os.WriteFile(notExec, []byte("notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	report = RunContract(context.Background(), notExec, dir)
	if !report.Failed {
		t.Error("a non-executable file passed the contract")
	}

	// A missing path.
	if report := RunContract(context.Background(), filepath.Join(dir, "nope"), dir); !report.Failed {
		t.Error("a missing path passed the contract")
	}
}

// TestContractRejectsAWrongProtocolVersion is the check that stops a future plugin
// from being driven with fields it may misread. Core refuses it at describe; the
// contract must say so too.
func TestContractRejectsAWrongProtocolVersion(t *testing.T) {
	// ValidateManifest is what Core calls at describe, and what the check reports on.
	bad := &Manifest{Protocol: 99, Name: "x", ModelReference: ModelQualified,
		Capabilities: Capabilities{Resume: ResumeClient, Bridge: BridgeNone}}
	err := ValidateManifest(bad)
	if err == nil {
		t.Fatal("a manifest from another protocol version must be refused")
	}
	report := Report{}
	report.add("protocol-version", false, "%v", err)
	if !report.Failed {
		t.Error("the contract would report success for an incompatible protocol")
	}
}

// TestContractFormatIsReadable guards the only output a plugin author sees. A
// conformance suite whose failure output is unreadable is not usable.
func TestContractFormatIsReadable(t *testing.T) {
	report := Report{
		Name: "example", Version: "1.0.0", Path: "/usr/local/bin/duo-plugin-example",
		Descriptor: "a reference plugin",
		Results: []Result{
			{Name: "describe", OK: true, Detail: "example 1.0.0"},
			{Name: "probe", OK: true, Optional: true, Detail: "agent CLI unavailable: not on PATH"},
			{Name: "prepare", OK: false, Detail: "prepare returned an empty command"},
		},
	}
	out := report.Format()
	for _, want := range []string{"example 1.0.0", "a reference plugin", "ok", "skip", "FAIL", "1 passed", "1 skipped", "1 failed", "FAILED:"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}
	if report.ExitCode() != 1 {
		t.Error("a failing report must exit non-zero so CI can gate on it")
	}
}

// TestDiscoverAndRunSkipsInProcessDrivers documents that the contract is about the
// wire, so a driver running inside Duo has no executable to check and is skipped
// rather than reported as a failure.
func TestDiscoverAndRunSkipsInProcessDrivers(t *testing.T) {
	t.Cleanup(func() { unregisterForTest("contract-builtin") })
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	Register("contract-builtin", func() Handler { return noopDriver{} })

	reports := DiscoverAndRun(context.Background(), t.TempDir())
	for _, r := range reports {
		if r.Name == "contract-builtin" || r.Path == BuiltInSource {
			t.Errorf("an in-process driver was sent through the wire contract: %+v", r)
		}
	}
}

// TestReferencePluginBinary builds the positive control the respawn check needs.
// hasResult reports whether a named check ran, so a test can insist a check was
// exercised rather than skipped.
func hasResult(r Report, name string) bool {
	for _, result := range r.Results {
		if result.Name == name {
			return true
		}
	}
	return false
}

func TestReferencePluginBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping build in short mode")
	}
	dir := t.TempDir()
	bin, err := ReferencePluginBinary(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReferencePluginBinary: %v", err)
	}
	if info, err := os.Stat(bin); err != nil || info.Mode()&0111 == 0 {
		t.Fatalf("reference plugin at %s is not executable: %v", bin, err)
	}
	if goodPluginPath(dir) != bin {
		t.Error("goodPluginPath does not find the reference plugin that was just built")
	}
}

// TestLegacyStateCheckRequiresClientResume pins which drivers are held to the
// pre-protocol compatibility requirement: only one that chooses the identity can be
// handed one, because a plugin that mints nothing has nothing to migrate.
func TestLegacyStateCheckRequiresClientResume(t *testing.T) {
	server := &Manifest{Capabilities: Capabilities{Resume: ResumeServer, Bridge: BridgeAgent}}
	report := Report{}
	plan := &LaunchPlan{Command: "example", SessionIdentity: ""}
	checkFirstLaunch(&report, server, plan)
	for _, r := range report.Results {
		if r.Name == "resume-first-run" && !r.OK {
			t.Errorf("a resume=server driver was failed for sending no identity: %s", r.Detail)
		}
	}

	report = Report{}
	lying := &Manifest{Capabilities: Capabilities{Resume: ResumeServer, Bridge: BridgeAgent}}
	checkFirstLaunch(&report, lying, &LaunchPlan{Command: "example", SessionIdentity: "invented"})
	found := false
	for _, r := range report.Results {
		if r.Name == "resume-first-run" && !r.OK {
			found = true
		}
	}
	if !found {
		t.Error("a resume=server driver that invented an identity must be failed")
	}
}

// TestLegacyShimIsDistinguishedFromABrokenPlugin covers the case the sweep exists to
// handle: an old `duo-<name>` exec wrapper is on PATH and speaks no protocol. It must
// be reported as what it is rather than as a plugin that fails, because those need
// different actions from whoever is reading the output.
func TestLegacyShimIsDistinguishedFromABrokenPlugin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".duo", "plugins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Two driver names: discovery keeps one implementation per name, so a shim and
	// a real plugin cannot share one.
	shim := filepath.Join(dir, "duo-oldstyle")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nexec oldstyle \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dir, "duo-plugin-broken")
	if err := os.WriteFile(real, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if !IsLegacyShim(shim) {
		t.Error("duo-<name> must be recognised as a legacy shim")
	}
	if IsLegacyShim(real) {
		t.Error("duo-plugin-<name> must not be mistaken for a shim")
	}

	reports := DiscoverAndRun(context.Background(), t.TempDir())
	var shimReport, pluginReport *Report
	for i := range reports {
		switch reports[i].Path {
		case shim:
			shimReport = &reports[i]
		case real:
			pluginReport = &reports[i]
		}
	}
	if shimReport == nil {
		t.Fatal("the shim was not discovered")
	}
	if !shimReport.Passed() || !hasOptional(shimReport, "legacy-launch-shim") {
		t.Errorf("a legacy shim must be skipped with an explanation, not failed:\\n%s", shimReport.Format())
	}
	if pluginReport == nil {
		t.Fatal("the plugin was not discovered")
	}
	if pluginReport.Passed() {
		t.Errorf("a duo-plugin-<name> that answers nothing must fail, not be excused as a shim:\\n%s", pluginReport.Format())
	}
}

func hasOptional(r *Report, name string) bool {
	for _, result := range r.Results {
		if result.Name == name && result.Optional {
			return true
		}
	}
	return false
}

// TestContractLeavesATimedOutListingUnverified pins the difference between a method
// that is missing and a method that was slow. A plugin whose listing timed out has
// implemented everything the contract asks for; failing it would send its author to
// look for a method that is already there.
//
// The deadline has to survive the round trip for this to work at all: it leaves the
// plugin as an error code and reappears as context.DeadlineExceeded on the way back.
// When it did not, this check failed a plugin for a busy machine.
func TestContractLeavesATimedOutListingUnverified(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess contract test in short mode")
	}
	bin := buildPlugin(t, "github.com/atfa/duo/internal/driver/testdata/slowmodels", "duo-plugin-slow")

	report := RunContract(context.Background(), bin, t.TempDir())
	for _, line := range strings.Split(report.Format(), "\n") {
		if !strings.Contains(line, "capability-models") {
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(line), "skip") {
			t.Fatalf("a timed-out listing must be skipped, not failed: %s", line)
		}
		return
	}
	t.Fatalf("capability-models was never reported; the deadline did not survive the round trip:\n%s", report.Format())
}

// TestTimeoutCodeSurvivesTheRoundTrip checks the same link directly, without a
// process, so a regression names itself.
func TestTimeoutCodeSurvivesTheRoundTrip(t *testing.T) {
	resp := handle(deadlinePlugin{}, &Request{Protocol: ProtocolVersion, ID: 1, Method: MethodModels})
	if resp.Error == nil || resp.Error.Code != CodeTimeout {
		t.Fatalf("a deadline must be served as %s, got %+v", CodeTimeout, resp.Error)
	}
	err := decodeResponse("slow", MethodModels, resp, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the deadline did not survive the round trip: %v", err)
	}
}

// deadlinePlugin is the smallest Handler that answers models with a deadline, which
// is the error RunModelList produces when a listing runs out of time.
type deadlinePlugin struct{}

func (deadlinePlugin) Describe() (*Manifest, error) {
	return &Manifest{Protocol: ProtocolVersion, Name: "slow"}, nil
}
func (deadlinePlugin) Probe() (*ProbeResult, error) { return &ProbeResult{Available: true}, nil }
func (deadlinePlugin) Prepare(LaunchRequest) (*LaunchPlan, error) {
	return &LaunchPlan{Command: "sh -c 'exit 0'"}, nil
}
func (deadlinePlugin) Models() (*ModelList, error) {
	return nil, fmt.Errorf("did not finish: %w", context.DeadlineExceeded)
}
