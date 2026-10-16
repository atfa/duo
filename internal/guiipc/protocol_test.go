package guiipc

import (
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
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
			Mode:        project.ModeStep,
			Phase:       project.PhaseExecute,
			Plan:        "the shared plan",
			PlanVersion: 3,
			Ready:       map[protocol.AgentID]bool{protocol.Austin: true},
			Step:        2,
			Steps:       project.Steps{{Title: "one"}, {Title: "two"}, {Title: "three"}},
		},
		Worktrees: workspace.Set{
			Repository: "/home/user/duo",
			BaseBranch: "main",
			BaseCommit: "d434ddbfc6",
		},
		Delivery: sessionstore.Delivery{Status: sessionstore.DeliveryPending},
		Notices:  []string{"driver reported a diagnostic"},
		Agents: map[protocol.AgentID]session.AgentView{
			protocol.Austin: {
				Present:   true,
				Connected: true,
				Process:   agent.ProcessRunning,
				Driver:    "pi",
				Model:     "claude-sonnet",
				Thinking:  "high",
				Runtime: harness.AgentRuntime{
					Busy:       true,
					Tool:       "edit",
					ToolDetail: "internal/guiipc/protocol.go",
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
	if !austin.Busy || austin.Activity != "internal/guiipc/protocol.go" {
		t.Errorf("austin busy/activity = %v/%q", austin.Busy, austin.Activity)
	}
	if _, ok := got.Agents["Austin"]; ok {
		t.Error("agents uses a title-case key; the wire spelling must be lower case")
	}
	if tony, ok := got.Agents["tony"]; !ok || tony.Process != "stopping" || tony.Present != true {
		t.Errorf("tony = %+v, present=%v", tony, ok)
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
		{name: "unknown kind", line: `{"type":"command","kind":"set_model"}`},
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
