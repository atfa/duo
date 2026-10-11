package codex

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/mcp"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/transport"
)

func TestManifestDeclaresImplementedCapabilities(t *testing.T) {
	manifest, err := (&Plugin{}).Describe()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Name != Name || manifest.ModelReference != driver.ModelBare || manifest.Capabilities.Resume != driver.ResumeServer {
		t.Fatalf("unexpected identity contract: %#v", manifest)
	}
	if manifest.Capabilities.Bridge != driver.BridgeNone || !manifest.Capabilities.PTYFallback || !manifest.MCP || !manifest.Activity {
		t.Fatalf("unexpected integration capabilities: %#v", manifest.Capabilities)
	}
	if !manifest.Capabilities.Models {
		t.Fatal("Codex model catalog capability is not declared")
	}
}

func TestModelsReadsVisiblePagesFromAppServer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex")
	server := `#!/bin/sh
while IFS= read -r request; do
  case "$request" in
    *'"method":"initialize"'*) printf '%s\n' '{"id":1,"result":{}}' ;;
    *'"method":"model/list"'*'"cursor"'*) printf '%s\n' '{"id":3,"result":{"data":[{"model":"model-two","displayName":"Second","inputModalities":["text"]}],"nextCursor":null}}' ;;
    *'"method":"model/list"'*) printf '%s\n' '{"id":2,"result":{"data":[{"model":"model-one","displayName":"First","inputModalities":["text","image"],"supportedReasoningEfforts":[{"reasoningEffort":"high"}]}],"nextCursor":"next"}}' ;;
  esac
done
`
	if err := os.WriteFile(path, []byte(server), 0o700); err != nil {
		t.Fatal(err)
	}
	oldLookPath := lookPath
	lookPath = func(string) (string, error) { return path, nil }
	t.Cleanup(func() { lookPath = oldLookPath })

	list, err := (&Plugin{}).Models()
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Models) != 2 || list.Models[0].ID != "model-one" || list.Models[1].ID != "model-two" {
		t.Fatalf("catalog = %#v", list.Models)
	}
	if list.Models[0].Provider != "openai" || list.Models[0].DisplayName != "First" || !list.Models[0].Images || !list.Models[0].Thinking {
		t.Fatalf("first model metadata = %#v", list.Models[0])
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
	if strings.Count(plan.Command, "--no-daemon") != 1 {
		t.Fatalf("Codex shared daemon was not disabled exactly once: %s", plan.Command)
	}
	if strings.Contains(plan.Command, "--model ''") || !strings.Contains(plan.Command, "--model custom") {
		t.Fatalf("empty model flag or operator model mishandled: %s", plan.Command)
	}
}

func TestPrepareDisablesSharedDaemonForFreshAndResumedCommands(t *testing.T) {
	cwd := t.TempDir()
	prior, err := json.Marshal(state{SessionID: "prior-session", CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"codex", "codex --no-daemon", "codex resume existing-session"} {
		plan := prepare(t, driver.LaunchRequest{BaseCommand: base, CWD: cwd, State: prior, ResumeWake: true})
		if strings.Count(plan.Command, "--no-daemon") != 1 {
			t.Errorf("BaseCommand %q produced %q; want --no-daemon exactly once", base, plan.Command)
		}
	}
}

func TestPreparePromptsCodexToContinueOnlyWhenAddingResume(t *testing.T) {
	cwd := t.TempDir()
	stateBlob, err := json.Marshal(state{SessionID: "prior-session", CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	plan := prepare(t, driver.LaunchRequest{BaseCommand: "codex", CWD: cwd, State: stateBlob, ResumeWake: true})
	quotedPrompt := driver.ShellQuote(resumePrompt)
	if !strings.Contains(plan.Command, "resume 'prior-session' "+quotedPrompt) {
		t.Fatalf("resume command missing quoted continuation prompt: %s", plan.Command)
	}
	if !strings.Contains(quotedPrompt, `\''`) {
		t.Fatalf("test prompt no longer exercises shell apostrophe quoting: %s", quotedPrompt)
	}

	withoutSession, err := json.Marshal(state{CWD: cwd, StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	plan = prepare(t, driver.LaunchRequest{BaseCommand: "codex --profile work", CWD: cwd, State: withoutSession, ResumeWake: true})
	if !strings.HasPrefix(plan.Command, "codex --profile work ") || strings.Contains(plan.Command, " resume ") || !strings.Contains(plan.Command, "This Duo session was resumed, but no prior Codex conversation could be recovered.") {
		t.Fatalf("resume without Codex session ID did not get a truthful startup prompt: %s", plan.Command)
	}
	if !strings.HasSuffix(plan.Command, driver.ShellQuote("This Duo session was resumed, but no prior Codex conversation could be recovered. Do not claim to have that conversation. First inspect duo_status, then continue the current phase and work in this worktree without restarting completed work.")) {
		t.Fatalf("startup prompt was not appended after Codex options: %s", plan.Command)
	}

	restarted := prepare(t, driver.LaunchRequest{BaseCommand: "codex", CWD: cwd, State: stateBlob})
	if !strings.Contains(restarted.Command, "resume 'prior-session'") || strings.Contains(restarted.Command, resumePrompt) {
		t.Fatalf("same-session restart did not preserve Codex conversation without waking it: %s", restarted.Command)
	}
	restartedWithoutSession := prepare(t, driver.LaunchRequest{BaseCommand: "codex", CWD: cwd, State: withoutSession})
	if strings.Contains(restartedWithoutSession.Command, resumePrompt) || strings.Contains(restartedWithoutSession.Command, "This Duo session was resumed") || strings.Contains(restartedWithoutSession.Command, " resume ") {
		t.Fatalf("same-session restart without Codex ID was given an unsolicited prompt: %s", restartedWithoutSession.Command)
	}

	fresh := prepare(t, driver.LaunchRequest{BaseCommand: "codex", CWD: cwd})
	if strings.Contains(fresh.Command, resumePrompt) || strings.Contains(fresh.Command, " resume ") {
		t.Fatalf("fresh command unexpectedly contains resume prompt or subcommand: %s", fresh.Command)
	}

	alreadyResuming := prepare(t, driver.LaunchRequest{BaseCommand: "codex resume other-session", CWD: cwd, State: stateBlob})
	if strings.Contains(alreadyResuming.Command, resumePrompt) || strings.Count(alreadyResuming.Command, " resume ") != 1 {
		t.Fatalf("existing resume command was given a duplicate prompt or subcommand: %s", alreadyResuming.Command)
	}
	alreadyResumingWithoutSession := prepare(t, driver.LaunchRequest{BaseCommand: "codex resume other-session", CWD: cwd, State: withoutSession})
	if strings.Contains(alreadyResumingWithoutSession.Command, "This Duo session was resumed") || strings.Count(alreadyResumingWithoutSession.Command, " resume ") != 1 {
		t.Fatalf("explicit resume command was given a duplicate startup prompt: %s", alreadyResumingWithoutSession.Command)
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
	resume := prepareWith(t, restarted, driver.LaunchRequest{CWD: cwd, State: stateBlob, ResumeWake: true})
	if !strings.Contains(resume.Command, "resume '"+id+"'") {
		t.Fatalf("resume command did not use Codex-issued ID: %s", resume.Command)
	}
	if resume.SessionIdentity != id {
		t.Fatalf("resume identity = %q, want %q", resume.SessionIdentity, id)
	}
}

func TestPrepareRecoversUniqueRolloutWhenCheckpointPredatesMetadata(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", home)
	startedAt := time.Now()
	const id = "01abc-late-rollout-id"
	writeRollout(t, home, startedAt.Add(-8*time.Minute), id, cwd)
	prior, err := json.Marshal(state{CWD: cwd, StartedAt: startedAt})
	if err != nil {
		t.Fatal(err)
	}
	plan := prepareWith(t, &Plugin{}, driver.LaunchRequest{CWD: cwd, State: prior, ResumeWake: true})
	if plan.SessionIdentity != id || !strings.Contains(plan.Command, "resume '"+id+"'") {
		t.Fatalf("unique prior rollout not recovered: identity=%q command=%s", plan.SessionIdentity, plan.Command)
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

func TestDiscoveryIgnoresCodexSubagentRollout(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	start := time.Now()
	writeRollout(t, home, start.Add(time.Second), "root-session", cwd)
	writeRolloutMetadata(t, home, start.Add(2*time.Second), "subagent-session", cwd, map[string]any{
		"subagent": map[string]any{"thread_spawn": map[string]string{"parent_thread_id": "root-session"}},
	})
	if ids := findNewSessionIDs(home, state{CWD: cwd, StartedAt: start}); len(ids) != 1 || ids[0] != "root-session" {
		t.Fatalf("root session discovery included a subagent: %v", ids)
	}
	if path := findRolloutPath(home, state{CWD: cwd, StartedAt: start}, "subagent-session"); path != "" {
		t.Fatalf("subagent rollout was treated as a root session: %s", path)
	}
}

func TestRolloutTailReportsAssistantTextOnceAndTurnTransitions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-test.jsonl")
	line := `{"type":"event_msg","payload":{"type":"task_started"}}`
	if err := os.WriteFile(path, []byte(line[:len(line)-3]), 0o600); err != nil {
		t.Fatal(err)
	}
	var offset int64
	var pending []byte
	if got := readRolloutMessages(path, &offset, &pending); len(got) != 0 {
		t.Fatalf("incomplete event produced activity: %v", got)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(append([]byte(line[len(line)-3:]), '\n')); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if got := readRolloutMessages(path, &offset, &pending); len(got) != 1 || got[0].Activity != protocol.ActivityAgentStart {
		t.Fatalf("task_started event = %v", got)
	}
	entries := []byte(strings.Join([]string{
		`{"type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Austin summary"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"task"}]}}`,
		`{"type":"response_item","payload":{"type":"reasoning","summary":[]}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"duo_status","arguments":"{}"}}`,
		`{"type":"event_msg","payload":{"type":"AgentMessage","message":"Austin summary"}}`,
		`{"type":"event_msg","payload":{"type":"task_complete"}}`,
	}, "\n") + "\n")
	if f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(entries); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	got := readRolloutMessages(path, &offset, &pending)
	if len(got) != 2 || got[0].Type != protocol.MsgAssistantMessage || got[0].Text != "Austin summary" || got[1].Activity != protocol.ActivityAgentSettled {
		t.Fatalf("assistant/settled messages = %#v", got)
	}

	history := []byte(`{"type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"old summary"}]}}` + "\n")
	if err := os.WriteFile(path, history, 0o600); err != nil {
		t.Fatal(err)
	}
	offset, pending = int64(len(history)), nil
	if got := readRolloutMessages(path, &offset, &pending); len(got) != 0 {
		t.Fatalf("historical assistant message replayed: %#v", got)
	}
}

func TestActivitySinkRetriesLatestTransitionAfterBridgeFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &activityCapture{messages: make(chan protocol.Message, 4)}
	s := transport.NewServer("127.0.0.1:0", "session-1", "secret")
	s.SetHandler(h)
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServe(ctx) }()
	<-s.Ready()

	sink := &codexActivitySink{cfg: mcp.Config{
		Agent: protocol.Austin, SessionID: "session-1", Token: "secret",
		Host: "127.0.0.1", Port: "1", ClientType: "observer",
	}}
	defer sink.Close()
	sink.OnActivity(protocol.Message{Type: protocol.MsgActivity, Activity: protocol.ActivityAgentStart})
	sink.OnAssistantMessage(protocol.Message{Type: protocol.MsgAssistantMessage, Text: "first summary paragraph"})
	sink.OnAssistantMessage(protocol.Message{Type: protocol.MsgAssistantMessage, Text: "second summary paragraph"})
	sink.OnActivity(protocol.Message{Type: protocol.MsgActivity, Activity: protocol.ActivityAgentSettled})
	sink.mu.Lock()
	pending := sink.pending
	sink.mu.Unlock()
	if pending == nil || pending.Activity != protocol.ActivityAgentSettled {
		t.Fatalf("pending transition = %#v, want latest settled transition", pending)
	}

	_, port, err := net.SplitHostPort(s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	sink.cfg.Port = port
	sink.mu.Unlock()
	sink.Retry()
	for _, want := range []protocol.Message{
		{Type: protocol.MsgAssistantMessage, Text: "first summary paragraph"},
		{Type: protocol.MsgAssistantMessage, Text: "second summary paragraph"},
		{Type: protocol.MsgActivity, Activity: protocol.ActivityAgentSettled},
	} {
		select {
		case got := <-h.messages:
			if got.Type != want.Type || got.Text != want.Text || got.Activity != want.Activity {
				t.Fatalf("retried message = %#v, want %#v", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("pending message was not retried: %#v", want)
		}
	}
	cancel()
	_ = <-done
}

func TestWatcherReportsActivityFromNewRollout(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", home)
	oldInterval, oldSettle := watchInterval, settleInterval
	watchInterval, settleInterval = 5*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { watchInterval, settleInterval = oldInterval, oldSettle })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &activityCapture{messages: make(chan protocol.Message, 2)}
	s := transport.NewServer("127.0.0.1:0", "session-1", "secret")
	s.SetHandler(h)
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServe(ctx) }()
	<-s.Ready()
	_, port, err := net.SplitHostPort(s.Addr())
	if err != nil {
		t.Fatal(err)
	}

	target := state{CWD: cwd, StartedAt: time.Now()}
	p := &Plugin{current: target}
	t.Cleanup(func() { _ = p.Close() })
	p.watch(target)
	p.watchActivity(target, mcp.Config{
		Agent: protocol.Austin, SessionID: "session-1", Token: "secret",
		Host: "127.0.0.1", Port: port, ClientType: "observer",
	})

	const id = "01abc-new-rollout"
	writeRollout(t, home, target.StartedAt.Add(time.Second), id, cwd)
	path := findRolloutPath(home, target, id)
	if path == "" {
		t.Fatal("new rollout was not discoverable")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"event_msg","payload":{"type":"task_started"}}` + "\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()

	select {
	case got := <-h.messages:
		if got.Agent != protocol.Austin || got.Activity != protocol.ActivityAgentStart {
			t.Fatalf("rollout activity = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("Codex task_started was not reported to Duo")
	}
	f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Austin summary"}]}}` + "\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()
	select {
	case got := <-h.messages:
		if got.Type != protocol.MsgAssistantMessage || got.Agent != protocol.Austin || got.Text != "Austin summary" {
			t.Fatalf("rollout assistant message = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("Codex assistant message was not reported to Duo")
	}
	cancel()
	_ = <-done
}

type activityCapture struct{ messages chan protocol.Message }

func (*activityCapture) OnConnect(context.Context, *transport.Client) {}
func (*activityCapture) OnDisconnect(*transport.Client)               {}
func (h *activityCapture) OnMessage(_ context.Context, _ *transport.Client, msg protocol.Message) {
	h.messages <- msg
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
	writeRolloutMetadata(t, home, timestamp, id, cwd, nil)
}

func writeRolloutMetadata(t *testing.T, home string, timestamp time.Time, id, cwd string, source any) {
	t.Helper()
	dir := filepath.Join(home, "sessions", timestamp.Local().Format("2006"), timestamp.Local().Format("01"), timestamp.Local().Format("02"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"id": id, "cwd": cwd, "source": "cli"}
	if source != nil {
		payload["source"] = source
	}
	meta := map[string]any{
		"type": "session_meta", "timestamp": timestamp.Format(time.RFC3339Nano),
		"payload": payload,
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
