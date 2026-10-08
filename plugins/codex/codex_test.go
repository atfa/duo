package codex

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/driver"
)

func TestManifestDeclaresImplementedCapabilities(t *testing.T) {
	manifest, err := (&Plugin{}).Describe()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Name != Name || manifest.ModelReference != driver.ModelBare || manifest.Capabilities.Resume != driver.ResumeServer {
		t.Fatalf("unexpected identity contract: %#v", manifest)
	}
	if manifest.Capabilities.Bridge != driver.BridgeNone || !manifest.Capabilities.PTYFallback || !manifest.MCP || manifest.Activity {
		t.Fatalf("unexpected integration capabilities: %#v", manifest.Capabilities)
	}
}

func TestProjectTrustOverrideUsesGitRootForScopedWorkingDirectory(t *testing.T) {
	worktree := t.TempDir()
	if output, err := exec.Command("git", "-C", worktree, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	scope := filepath.Join(worktree, "packages", "web")
	if err := os.MkdirAll(scope, 0o700); err != nil {
		t.Fatal(err)
	}
	override := projectTrustOverride(driver.LaunchRequest{RepositoryRoot: "/source/repository", CWD: scope})
	worktreeRoot, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(override, strconv.Quote(worktreeRoot)+`={trust_level="trusted"}`) {
		t.Fatalf("override did not trust worktree root: %s", override)
	}
	if !strings.Contains(override, strconv.Quote("/source/repository")+`={trust_level="trusted"}`) {
		t.Fatalf("override omitted repository root: %s", override)
	}
}

func TestPreparePreservesCommandAndWiresMCPWithoutExposingToken(t *testing.T) {
	repositoryRoot := filepath.Join(t.TempDir(), "repo root")
	worktree := filepath.Join(t.TempDir(), "worktree root")
	plan := prepare(t, driver.LaunchRequest{
		Agent: "Austin", Session: "duo-session", Token: "secret-token",
		Host: "127.0.0.1", Port: "54321", BaseCommand: "codex --profile work",
		Model: "openai/gpt-5.4-codex", Thinking: "high", RepositoryRoot: repositoryRoot, CWD: worktree,
	})
	for _, want := range []string{
		"codex --profile work", "--model 'gpt-5.4-codex'", "model_reasoning_effort=high",
		`mcp_servers.duo.command="/usr/bin/duo"`, "mcp-server", "DUO_TOKEN", "--dangerously-bypass-approvals-and-sandbox",
		`projects={"` + repositoryRoot + `"={trust_level="trusted"},"` + worktree + `"={trust_level="trusted"}}`,
	} {
		if !strings.Contains(plan.Command, want) {
			t.Errorf("command missing %q: %s", want, plan.Command)
		}
	}
	if strings.Contains(plan.Command, "secret-token") {
		t.Fatal("bridge token leaked into command line")
	}
	if plan.Env["DUO_TOKEN"] != "secret-token" || plan.Env["DUO_SESSION"] != "duo-session" || plan.Env["DUO_AGENT"] != "austin" {
		t.Fatalf("MCP environment not wired: %#v", plan.Env)
	}
	if plan.Notices == nil || !strings.Contains(plan.Notices[0], "rollout files") {
		t.Fatalf("resume discovery limitation not disclosed: %#v", plan.Notices)
	}
}

func TestPrepareRespectsOperatorSafetyFlagsAndOmitsEmptyModel(t *testing.T) {
	plan := prepare(t, driver.LaunchRequest{BaseCommand: "codex --yolo --model custom", CWD: t.TempDir()})
	if strings.Count(plan.Command, "--yolo") != 1 || strings.Contains(plan.Command, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("duplicated or replaced explicit YOLO mode: %s", plan.Command)
	}
	if strings.Contains(plan.Command, "--model ''") || !strings.Contains(plan.Command, "--model custom") {
		t.Fatalf("empty model flag or operator model mishandled: %s", plan.Command)
	}
}

func TestPrepareAddsYOLOFlagOnce(t *testing.T) {
	plan := prepare(t, driver.LaunchRequest{BaseCommand: "codex --profile work", CWD: t.TempDir()})
	if strings.Count(plan.Command, "--dangerously-bypass-approvals-and-sandbox") != 1 {
		t.Fatalf("full bypass flag = %s", plan.Command)
	}
	plan = prepare(t, driver.LaunchRequest{BaseCommand: "codex --dangerously-bypass-approvals-and-sandbox", CWD: t.TempDir()})
	if strings.Count(plan.Command, "--dangerously-bypass-approvals-and-sandbox") != 1 {
		t.Fatalf("duplicated full bypass flag: %s", plan.Command)
	}
}

func TestPrepareRejectsConflictingSandboxOrApprovalFlags(t *testing.T) {
	for _, flags := range []string{
		"--sandbox workspace-write", "-s workspace-write", "--ask-for-approval on-request",
		"-a on-request", "--full-auto",
	} {
		_, err := prepareWithError(t, &Plugin{}, driver.LaunchRequest{BaseCommand: "codex " + flags, CWD: t.TempDir()})
		rpcErr, ok := err.(*driver.RPCError)
		if !ok || rpcErr.Code != driver.CodeInvalid {
			t.Errorf("BaseCommand %q error = %#v, want invalid option error", flags, err)
		}
	}
}

func TestPrepareRequiresRunnableDuoForMCP(t *testing.T) {
	old := duoCommand
	duoCommand = func() (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { duoCommand = old })
	if _, err := (&Plugin{}).Prepare(driver.LaunchRequest{CWD: t.TempDir()}); err == nil {
		t.Fatal("Prepare succeeded without a runnable Duo MCP server")
	}
}

func TestProbeRejectsBrokenCLIShim(t *testing.T) {
	oldLookPath := lookPath
	oldDuo := duoCommand
	t.Cleanup(func() { lookPath, duoCommand = oldLookPath, oldDuo })
	duoCommand = func() (string, error) { return "/usr/bin/duo", nil }
	path := filepath.Join(t.TempDir(), "codex")
	lookPath = func(string) (string, error) { return path, nil }

	if err := os.WriteFile(path, []byte("#!/bin/sh\necho missing-runtime >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := (&Plugin{}).Probe()
	if err != nil || result.Available || !strings.Contains(result.Reason, "missing-runtime") {
		t.Fatalf("Probe() = %#v, %v", result, err)
	}

	if err := os.WriteFile(path, []byte("#!/bin/sh\necho 'codex 1.2.3'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	result, err = (&Plugin{}).Probe()
	if err != nil || !result.Available || result.AgentPath != path {
		t.Fatalf("Probe() = %#v, %v", result, err)
	}
}

func TestPrepareRejectsBrokenDefaultCLI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho \"Cannot find module @openai/codex-darwin-arm64\" >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	oldLookPath := lookPath
	lookPath = func(string) (string, error) { return path, nil }
	t.Cleanup(func() { lookPath = oldLookPath })

	_, err := prepareWithError(t, &Plugin{}, driver.LaunchRequest{BaseCommand: Name, CWD: t.TempDir()})
	rpcErr, ok := err.(*driver.RPCError)
	if !ok || rpcErr.Code != driver.CodeUnavailable || !strings.Contains(rpcErr.Message, "codex-darwin-arm64") {
		t.Fatalf("Prepare error = %#v, want concise unavailable CLI error", err)
	}
}

func TestPrepareSkipsGlobalPreflightForCustomCommand(t *testing.T) {
	calls := 0
	oldLookPath := lookPath
	lookPath = func(string) (string, error) {
		calls++
		return "", os.ErrNotExist
	}
	t.Cleanup(func() { lookPath = oldLookPath })

	plan := prepare(t, driver.LaunchRequest{BaseCommand: "/worktree/bin/codex-wrapper --profile local", CWD: t.TempDir()})
	if calls != 0 {
		t.Fatalf("custom command triggered global codex lookup %d times", calls)
	}
	if !strings.HasPrefix(plan.Command, "/worktree/bin/codex-wrapper --profile local") {
		t.Fatalf("custom command was not preserved: %s", plan.Command)
	}
}

func TestDiscoverFreshRolloutAndResumeAfterRestart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	oldInterval := watchInterval
	watchInterval = 5 * time.Millisecond
	t.Cleanup(func() { watchInterval = oldInterval })
	cwd := t.TempDir()
	p := &Plugin{}
	plan := prepareWith(t, p, driver.LaunchRequest{CWD: cwd})
	var prior state
	if err := json.Unmarshal(plan.State, &prior); err != nil {
		t.Fatal(err)
	}
	id := "01abc-real-codex-id"
	writeRollout(t, home, prior.StartedAt, id, cwd)
	waitForSessionID(t, p, id)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}

	stateBlob, err := p.State()
	if err != nil {
		t.Fatal(err)
	}
	restarted := &Plugin{}
	resume := prepareWith(t, restarted, driver.LaunchRequest{CWD: cwd, State: stateBlob})
	if !strings.Contains(resume.Command, "resume '"+id+"'") {
		t.Fatalf("resume command did not use Codex-issued ID: %s", resume.Command)
	}
	if resume.SessionIdentity != id {
		t.Fatalf("resume identity = %q, want %q", resume.SessionIdentity, id)
	}
}

func TestDiscoveryRejectsOldAndAmbiguousRollouts(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	start := time.Now()
	writeRollout(t, home, start.Add(-time.Minute), "old-id", cwd)
	writeRollout(t, home, start.Add(time.Second), "new-a", cwd)
	writeRollout(t, home, start.Add(2*time.Second), "new-b", cwd)
	if ids := findNewSessionIDs(home, state{CWD: cwd, StartedAt: start}); len(ids) != 2 {
		t.Fatalf("fresh matching candidates = %v, want ambiguous pair", ids)
	}
	if ids := findNewSessionIDs(home, state{CWD: filepath.Join(cwd, "elsewhere"), StartedAt: start}); len(ids) != 0 {
		t.Fatalf("different cwd matched: %v", ids)
	}
}

func TestWatcherDoesNotChooseAnAmbiguousSession(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", home)
	oldInterval, oldSettle := watchInterval, settleInterval
	watchInterval, settleInterval = 5*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { watchInterval, settleInterval = oldInterval, oldSettle })
	p := &Plugin{}
	plan := prepareWith(t, p, driver.LaunchRequest{CWD: cwd})
	var target state
	if err := json.Unmarshal(plan.State, &target); err != nil {
		t.Fatal(err)
	}
	writeRollout(t, home, target.StartedAt.Add(time.Second), "candidate-a", cwd)
	writeRollout(t, home, target.StartedAt.Add(2*time.Second), "candidate-b", cwd)
	time.Sleep(150 * time.Millisecond)
	data, err := p.State()
	if err != nil {
		t.Fatal(err)
	}
	var got state
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.SessionID != "" {
		t.Fatalf("ambiguous rollout selected %q", got.SessionID)
	}
	_ = p.Close()
}

func TestThinkingOptions(t *testing.T) {
	options, err := (&Plugin{}).Thinking()
	if err != nil || options.Default != "medium" || len(options.Levels) == 0 {
		t.Fatalf("Thinking() = %#v, %v", options, err)
	}
}

func prepare(t *testing.T, req driver.LaunchRequest) *driver.LaunchPlan {
	t.Helper()
	return prepareWith(t, &Plugin{}, req)
}

func prepareWith(t *testing.T, p *Plugin, req driver.LaunchRequest) *driver.LaunchPlan {
	t.Helper()
	plan, err := prepareWithError(t, p, req)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func prepareWithError(t *testing.T, p *Plugin, req driver.LaunchRequest) (*driver.LaunchPlan, error) {
	t.Helper()
	t.Cleanup(func() { _ = p.Close() })
	old := duoCommand
	duoCommand = func() (string, error) { return "/usr/bin/duo", nil }
	t.Cleanup(func() { duoCommand = old })
	return p.Prepare(req)
}

func writeRollout(t *testing.T, home string, timestamp time.Time, id, cwd string) {
	t.Helper()
	dir := filepath.Join(home, "sessions", timestamp.Local().Format("2006"), timestamp.Local().Format("01"), timestamp.Local().Format("02"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{
		"type": "session_meta", "timestamp": timestamp.Format(time.RFC3339Nano),
		"payload": map[string]string{"id": id, "cwd": cwd},
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-test-"+id+".jsonl"), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitForSessionID(t *testing.T, p *Plugin, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := p.State()
		if err != nil {
			t.Fatal(err)
		}
		var got state
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if got.SessionID == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("driver did not learn session id %q", want)
}
