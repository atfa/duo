package guiipc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/models"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

// eventTime is a fixed instant so RFC3339 formatting is asserted, not sampled.
var eventTime = time.Date(2026, 10, 16, 10, 37, 37, 0, time.UTC)

// keys reports the agent keys of a wire view, for failure messages that show what
// was actually sent instead of only what was missing.
func keys(m map[string]Agent) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// testView is a view with every field the wire projection reads set to a value a
// test can assert on, so a dropped field fails here rather than in the GUI.
func testView() session.View {
	return session.View{
		Version:   "v0.11.0",
		SessionID: "20261016-103737-393c1e00",
		Language:  "zh",
		Project: project.Snapshot{
			Started:      true,
			Mode:         project.ModeStep,
			Phase:        project.PhaseExecute,
			Plan:         "the shared plan",
			PlanVersion:  3,
			Ready:        map[protocol.AgentID]bool{protocol.Austin: true},
			Notes:        map[protocol.AgentID]string{protocol.Austin: "reviewed"},
			Evidence:     map[protocol.AgentID]string{protocol.Austin: "go test ./internal/core"},
			Step:         2,
			Verification: project.Verification{Status: project.VerificationPassed, Head: "abc123", Note: "all checks passed", At: &eventTime},
			Steps: project.Steps{
				{Title: "one", Done: true, Head: "step1", Verification: project.Verification{Status: project.VerificationPassed, Head: "step1", Note: "verified"}, DeliveryHead: "landed1", DeliveredAt: "2026-10-16T10:00:00Z"},
				{Title: "two"}, {Title: "three"},
			},
		},
		Worktrees: workspace.Set{
			Repository: "/home/user/duo",
			BaseBranch: "main",
			BaseCommit: "d434ddbfc6",
			Austin:     workspace.Worktree{Path: "/tmp/duo/austin", Branch: "duo/austin", Head: "aaa"},
			Tony:       workspace.Worktree{Path: "/tmp/duo/tony", Branch: "duo/tony", Head: "bbb"},
		},
		Delivery: sessionstore.Delivery{Status: sessionstore.DeliveryPending},
		Notices:  []string{"driver reported a diagnostic"},
		Agents: map[protocol.AgentID]session.AgentView{
			protocol.Austin: {
				Present:      true,
				Connected:    true,
				Process:      agent.ProcessRunning,
				Driver:       "pi",
				Model:        "claude-sonnet",
				Thinking:     "high",
				Capabilities: driver.Capabilities{LiveModelSwitch: true},
				Runtime: harness.AgentRuntime{
					Busy:           true,
					ProviderActive: true,
					ToolDepth:      1,
					Tool:           "edit",
					ToolDetail:     "internal/guiipc/protocol.go",
					TurnStarted:    eventTime,
					ToolStarted:    eventTime.Add(-time.Second),
					ContextTokens:  123, ContextWindow: 456, TokensPerSecond: 7.5, Tools: 2,
					Recent: []harness.ToolNote{{Name: "read", Detail: "a.go", OK: true, Duration: 1500 * time.Millisecond}},
				},
			},
			protocol.Tony: {Present: true, Process: agent.ProcessStopping},
		},
	}
}

func TestFromViewProjectsEveryFrozenField(t *testing.T) {
	got := fromView(testView(), "status line")

	if got.Version != "v0.11.0" || got.Session != "20261016-103737-393c1e00" {
		t.Errorf("version/session = %q/%q", got.Version, got.Session)
	}
	if got.Mode != "step" || got.Phase != "EXECUTE" {
		t.Errorf("mode/phase = %q/%q; want step/EXECUTE", got.Mode, got.Phase)
	}
	if !got.Started {
		t.Error("started = false; want true")
	}
	if got.Language != "zh" || got.Status != "status line" {
		t.Errorf("language/status = %q/%q", got.Language, got.Status)
	}
	if got.Repository != "/home/user/duo" || got.BaseBranch != "main" || got.BaseCommit != "d434ddbfc6" {
		t.Errorf("repository/branch/commit = %q/%q/%q", got.Repository, got.BaseBranch, got.BaseCommit)
	}
	if got.Plan != "the shared plan" || got.PlanVersion != 3 {
		t.Errorf("plan/plan_version = %q/%d", got.Plan, got.PlanVersion)
	}
	if got.StepsTotal != 3 || got.StepCursor != 2 {
		t.Errorf("steps = %d/%d; want 3/2", got.StepsTotal, got.StepCursor)
	}
	if got.Delivery != sessionstore.DeliveryPending {
		t.Errorf("delivery = %q; want %q", got.Delivery, sessionstore.DeliveryPending)
	}
	if len(got.Notices) != 1 || got.Notices[0] != "driver reported a diagnostic" {
		t.Errorf("notices = %v", got.Notices)
	}
	if !got.Ready.Austin || got.Ready.Tony {
		t.Errorf("ready = %+v; want austin only", got.Ready)
	}
	if got.Verification.Status != "passed" || got.Verification.Head != "abc123" || got.Verification.Note != "all checks passed" || got.Verification.At != eventTime.Format(time.RFC3339Nano) {
		t.Errorf("verification = %+v", got.Verification)
	}
	if len(got.StepEvidence) != 3 || got.StepEvidence[0].Index != 1 || !got.StepEvidence[0].Done || got.StepEvidence[0].Verification.Status != "passed" || got.StepEvidence[0].DeliveryHead != "landed1" || got.StepEvidence[0].DeliveredAt != "2026-10-16T10:00:00Z" {
		t.Errorf("step evidence = %+v", got.StepEvidence)
	}

	austin, ok := got.Agents["austin"]
	if !ok {
		t.Fatalf("agents has no austin key; got %v", keys(got.Agents))
	}
	if !austin.Present || !austin.Connected || austin.Process != "running" {
		t.Errorf("austin presence = %+v", austin)
	}
	if austin.Driver != "pi" || austin.Model != "claude-sonnet" || austin.Thinking != "high" {
		t.Errorf("austin driver/model/thinking = %q/%q/%q", austin.Driver, austin.Model, austin.Thinking)
	}
	if !austin.LiveModelSwitch {
		t.Error("live_model_switch = false; want the session driver's capability")
	}
	if austin.WorktreePath != "/tmp/duo/austin" || austin.WorktreeBranch != "duo/austin" || austin.WorktreeHead != "aaa" {
		t.Errorf("austin worktree = %q/%q/%q", austin.WorktreePath, austin.WorktreeBranch, austin.WorktreeHead)
	}
	if austin.ReadyNote != "reviewed" || austin.ReadyEvidence != "go test ./internal/core" {
		t.Errorf("readiness evidence = %q/%q", austin.ReadyNote, austin.ReadyEvidence)
	}
	if !austin.Busy || austin.Activity != "internal/guiipc/protocol.go" {
		t.Errorf("austin busy/activity = %v/%q", austin.Busy, austin.Activity)
	}
	if austin.Work.Tool != "edit" || austin.Work.ToolDetail != "internal/guiipc/protocol.go" || !austin.Work.ProviderActive || austin.Work.ToolDepth != 1 || austin.Work.TurnStarted != eventTime.Format(time.RFC3339Nano) || austin.Work.ContextTokens != 123 || austin.Work.ContextWindow != 456 || austin.Work.TokensPerSecond != 7.5 || austin.Work.Tools != 2 || len(austin.Work.Recent) != 1 || austin.Work.Recent[0].DurationMS != 1500 {
		t.Errorf("structured work preview = %+v", austin.Work)
	}
	if _, ok := got.Agents["Austin"]; ok {
		t.Error("agents uses a title-case key; the wire spelling must be lower case")
	}
	if tony, ok := got.Agents["tony"]; !ok || tony.Process != "stopping" || tony.Present != true {
		t.Errorf("tony = %+v, present=%v", tony, ok)
	}
}

func TestWorkPreviewProjectsElapsedMillisecondsFromSnapshotTime(t *testing.T) {
	now := eventTime.Add(2500 * time.Millisecond)
	rt := harness.AgentRuntime{
		TurnStarted: eventTime,
		ToolStarted: eventTime.Add(time.Second),
	}

	got := workPreviewAt(rt, now)
	if got.TurnElapsedMS != 2500 || got.ToolElapsedMS != 1500 {
		t.Fatalf("elapsed = turn %d ms, tool %d ms; want 2500/1500", got.TurnElapsedMS, got.ToolElapsedMS)
	}
	if got.TurnStarted != eventTime.Format(time.RFC3339Nano) || got.ToolStarted != eventTime.Add(time.Second).Format(time.RFC3339Nano) {
		t.Fatalf("absolute starts = %q/%q", got.TurnStarted, got.ToolStarted)
	}

	zero := workPreviewAt(harness.AgentRuntime{}, now)
	if zero.TurnElapsedMS != 0 || zero.ToolElapsedMS != 0 {
		t.Fatalf("unset start elapsed = %d/%d; want zero", zero.TurnElapsedMS, zero.ToolElapsedMS)
	}
	future := workPreviewAt(harness.AgentRuntime{TurnStarted: now.Add(time.Second)}, now)
	if future.TurnElapsedMS != 0 {
		t.Fatalf("future turn start elapsed = %d; want zero", future.TurnElapsedMS)
	}
}

func TestReadDiffstatIncludesTrackedChangesAndUntrackedFiles(t *testing.T) {
	repo := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "-q")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test")
	tracked := filepath.Join(repo, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("old\nkeep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "tracked.txt")
	runGit("commit", "-qm", "base")
	base := runGit("rev-parse", "HEAD")
	if err := os.WriteFile(tracked, []byte("new\nkeep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("untracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := readDiffstat(context.Background(), repo, base)
	if got.State != "ready" || got.Files != 1 || got.Insertions != 1 || got.Deletions != 1 || got.Untracked != 1 {
		t.Fatalf("diffstat = %+v; want one changed file (+1/-1) and one untracked file", got)
	}
}

func TestReadDiffstatDoesNotRunRepositoryFsmonitor(t *testing.T) {
	repo := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "-q")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "tracked.txt")
	runGit("commit", "-qm", "base")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	marker := filepath.Join(repo, "fsmonitor-ran")
	hook := filepath.Join(repo, "fsmonitor")
	script := "#!/bin/sh\ntouch '" + marker + "'\nprintf 'token\\n'\n"
	if err := os.WriteFile(hook, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runGit("config", "core.fsmonitor", hook)

	got := readDiffstat(context.Background(), repo, runGit("rev-parse", "HEAD"))
	if got.State != "ready" || got.Files != 1 {
		t.Fatalf("diffstat = %+v; want ready with one tracked file", got)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("repository fsmonitor command ran during diffstat: stat error = %v", err)
	}
}

func TestReadDiffstatReportsGitFailureAndProjectionWithoutWorktree(t *testing.T) {
	if got := readDiffstat(context.Background(), filepath.Join(t.TempDir(), "missing"), "HEAD"); got.State != "error" || got.Error == "" {
		t.Fatalf("missing worktree diffstat = %+v; want error", got)
	}
	view := fromView(session.View{}, "")
	if got := view.Agents["austin"].Diffstat; got.State != "" {
		t.Fatalf("pure projection diffstat = %+v; host should own Git reads", got)
	}
	got := NewHost(&fakeSession{}).projectView(context.Background(), session.View{}, "")
	for _, id := range []string{"austin", "tony"} {
		if state := got.Agents[id].Diffstat.State; state != "unavailable" {
			t.Errorf("%s missing worktree state = %q; want unavailable", id, state)
		}
	}
}

func TestFromModelsKeepsDriverReferences(t *testing.T) {
	got := fromModels([]models.Model{{Provider: "real-provider", ID: "real/model", Thinking: true, Images: true, Bare: true}})
	if len(got) != 1 || got[0].Reference != "real/model" || got[0].Provider != "real-provider" || !got[0].Thinking || !got[0].Images {
		t.Fatalf("catalog projection = %+v", got)
	}
}

// TestFromViewAlwaysCarriesBothAgents pins the invariant that the window never
// has to distinguish "agent missing" from "field missing": both keys always
// exist, because a zero AgentView already reads as not-present.
func TestFromViewAlwaysCarriesBothAgents(t *testing.T) {
	got := fromView(session.View{}, "")
	if len(got.Agents) != 2 {
		t.Fatalf("agents = %v; want both keys", keys(got.Agents))
	}
	for _, id := range []string{"austin", "tony"} {
		if _, ok := got.Agents[id]; !ok {
			t.Errorf("agents has no %q key", id)
		}
	}
	if got.Ready.Austin || got.Ready.Tony {
		t.Errorf("zero ready = %+v; want both unset", got.Ready)
	}
	if got.Notices == nil {
		t.Error("zero notices = nil; want an empty slice so JSON encodes []")
	}
}

// TestFromViewCopiesNotices keeps the projection a value. The terminal interface
// reads the same session, so a frontend that mutated a slice it was handed must not
// be able to change what the next reader sees.
func TestFromViewCopiesNotices(t *testing.T) {
	view := testView()
	got := fromView(view, "")
	if len(got.Notices) == 0 {
		t.Fatal("the fixture has no notices, so this check proves nothing")
	}
	got.Notices[0] = "mutated"
	if view.Notices[0] == "mutated" {
		t.Error("fromView aliased the session's notices slice")
	}
}

func TestActivityLinePicksOneBoundedLine(t *testing.T) {
	cases := []struct {
		name string
		rt   harness.AgentRuntime
		want string
	}{
		{name: "tool detail wins", rt: harness.AgentRuntime{Tool: "edit", ToolDetail: "src/app.rs"}, want: "src/app.rs"},
		{name: "tool name next", rt: harness.AgentRuntime{Tool: "bash"}, want: "bash"},
		{name: "stream tail next", rt: harness.AgentRuntime{StreamTail: "writing the\nparser"}, want: "writing the parser"},
		{name: "error last", rt: harness.AgentRuntime{LastError: "boom"}, want: "boom"},
		{name: "none", rt: harness.AgentRuntime{}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := activityLine(tc.rt); got != tc.want {
				t.Errorf("activityLine = %q; want %q", got, tc.want)
			}
		})
	}

	long := harness.AgentRuntime{StreamTail: strings.Repeat("界", activityMaxRunes+50)}
	got := activityLine(long)
	if n := len([]rune(got)); n != activityMaxRunes {
		t.Errorf("truncated length = %d runes; want %d", n, activityMaxRunes)
	}
	if !strings.HasPrefix(got, "界") {
		t.Errorf("truncation split a multi-byte character: %q", got)
	}
}

func TestWorkPreviewBoundsTextWithoutSplittingUnicode(t *testing.T) {
	original := strings.Repeat("界", 1300)
	got := workPreview(harness.AgentRuntime{StreamTail: original})
	if n := len([]rune(got.StreamTail)); n != 1200 {
		t.Fatalf("stream tail length = %d runes; want 1200", n)
	}
	if !strings.HasPrefix(got.StreamTail, "界") || original != strings.Repeat("界", 1300) {
		t.Fatal("preview clipping split text or changed source data")
	}
}

func TestFromEventUsesTheWireAgentSpelling(t *testing.T) {
	got := fromEvent(events.Event{
		Time:  eventTime,
		Kind:  events.KindPeer,
		Agent: protocol.Austin,
		Peer:  protocol.Tony,
		Text:  "hello",
	})
	if got.Kind != "peer" || got.Agent != "austin" || got.Peer != "tony" || got.Text != "hello" {
		t.Errorf("fromEvent = %+v", got)
	}
	if got.Time != eventTime.Format(time.RFC3339Nano) {
		t.Errorf("time = %q; want %q", got.Time, eventTime.Format(time.RFC3339Nano))
	}
}

func TestFromHistoryDerivesKindAndKeepsLabel(t *testing.T) {
	cases := []struct {
		name string
		in   sessionstore.TUIEntry
		want string
	}{
		{name: "ordinary line", in: sessionstore.TUIEntry{Pane: "Austin"}, want: "assistant"},
		{name: "warning is a verdict", in: sessionstore.TUIEntry{Pane: "Tony", Warning: true}, want: "verdict"},
		{name: "error wins", in: sessionstore.TUIEntry{Pane: "Tony", Error: true, Warning: true}, want: "error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.in.Time = eventTime
			tc.in.Text = "body"
			tc.in.Label = "Austin → Tony"
			got := fromHistory(tc.in)
			if got.Kind != tc.want {
				t.Errorf("kind = %q; want %q", got.Kind, tc.want)
			}
			if got.Agent != "tony" && tc.in.Pane == "Tony" {
				t.Errorf("agent = %q; want the lower case wire spelling", got.Agent)
			}
			if got.Label != "Austin → Tony" {
				t.Errorf("label = %q; replay must keep the direction header", got.Label)
			}
			if got.Peer != "" {
				t.Errorf("peer = %q; history has no peer", got.Peer)
			}
		})
	}
}

func TestDecodeCommandRoundTripsEachKind(t *testing.T) {
	cases := []struct {
		line string
		want session.Command
		quit bool
	}{
		{line: `{"type":"command","kind":"submit_task","text":"do it"}`, want: session.SubmitTask{Text: "do it"}},
		{line: `{"type":"command","kind":"escalate_to_goal"}`, want: session.EscalateToGoal{}},
		{line: `{"type":"command","kind":"restart_agent","agent":"austin"}`, want: session.RestartAgent{Agent: protocol.Austin}},
		{line: `{"type":"command","kind":"restart_agent","agent":"Tony"}`, want: session.RestartAgent{Agent: protocol.Tony}},
		{line: `{"type":"command","kind":"set_language","language":"en"}`, want: session.SetLanguage{Language: "en"}},
		{line: `{"type":"command","kind":"set_model","agent":"tony","provider":"p","model":"m"}`, want: session.SetModel{Agent: protocol.Tony, Provider: "p", Model: "m"}},
		{line: `{"type":"command","kind":"cycle_thinking","agent":"austin"}`, want: session.CycleThinking{Agent: protocol.Austin}},
		{line: `{"type":"command","kind":"quit"}`, quit: true},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			got, quit, err := decodeCommand([]byte(tc.line))
			if err != nil {
				t.Fatalf("decodeCommand: %v", err)
			}
			if quit != tc.quit {
				t.Fatalf("quit = %v; want %v", quit, tc.quit)
			}
			if tc.quit {
				return
			}
			if got != tc.want {
				t.Errorf("command = %#v; want %#v", got, tc.want)
			}
		})
	}
}

func TestDecodeCommandRejectsBadFrames(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{name: "not json", line: `{`},
		{name: "wrong envelope", line: `{"type":"view","kind":"quit"}`},
		{name: "unknown kind", line: `{"type":"command","kind":"not_a_command"}`},
		{name: "set_model without model", line: `{"type":"command","kind":"set_model","agent":"austin"}`},
		{name: "cycle thinking unknown agent", line: `{"type":"command","kind":"cycle_thinking","agent":"duo"}`},
		{name: "submit without text", line: `{"type":"command","kind":"submit_task","text":"   "}`},
		{name: "set_language without language", line: `{"type":"command","kind":"set_language"}`},
		{name: "restart unknown agent", line: `{"type":"command","kind":"restart_agent","agent":"duo"}`},
		{name: "restart without agent", line: `{"type":"command","kind":"restart_agent"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := decodeCommand([]byte(tc.line)); err == nil {
				t.Fatalf("decodeCommand(%s) accepted a bad frame", tc.line)
			}
		})
	}
}
