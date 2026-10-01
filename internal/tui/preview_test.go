package tui

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/protocol"
)

// The preview band is the only always-visible view of what an agent is doing
// while a long turn runs, so it must show the tool, a failure and the streamed
// text, and must not break the frame's exact geometry.
func TestWorkPreviewShowsToolFailureAndStreamTail(t *testing.T) {
	a := testApp(100, 30)
	a.tracker.Handle(protocol.Austin, protocol.ActivityAgentStart)
	a.tracker.Handle(protocol.Austin, protocol.ActivityToolStart)
	a.tracker.Note(protocol.Austin, protocol.ActivityToolStart, "bash", "go test ./...")
	a.tracker.Handle(protocol.Tony, protocol.ActivityProviderStart)
	a.tracker.Note(protocol.Tony, protocol.ActivityStream, "", "checking the diff on Tony's branch")
	a.tracker.NoteError(protocol.Austin, "provider 429")

	frame := a.buildFrame(renderNormal)
	plain := ansiPattern.ReplaceAllString(frame, "")
	for _, want := range []string{
		"Austin preview",
		"Tony preview",
		"▶ bash · go test ./...",
		"✗ provider 429",
		"▸ checking the diff on Tony's branch",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("preview missing %q:\n%s", want, plain)
		}
	}

	// buildFrame deliberately leaves the last terminal row unused.
	lines := strings.Split(plain, "\r\n")
	if len(lines) != a.height-1 {
		t.Fatalf("frame has %d rows, want %d", len(lines), a.height-1)
	}
	for i, line := range lines {
		if got := displayWidth(line); got != a.width {
			t.Errorf("row %d is %d columns, want %d: %q", i, got, a.width, line)
		}
	}
}

func TestPreviewCanBeHiddenAndIsSkippedWhenShort(t *testing.T) {
	a := testApp(100, 30)
	if a.layoutFor(a.width, a.height).preview == 0 {
		t.Fatal("preview should be visible at 100x30")
	}
	a.handleKey("ctrl-p")
	if !a.hidePreview {
		t.Fatal("ctrl-p did not collapse the preview")
	}
	if got := a.layoutFor(a.width, a.height).preview; got != 0 {
		t.Fatalf("hidden preview still reports %d rows", got)
	}
	if strings.Contains(ansiPattern.ReplaceAllString(a.buildFrame(renderNormal), ""), "Austin preview") {
		t.Fatal("hidden preview is still rendered")
	}

	// A short terminal keeps usable panes instead of a preview band.
	short := testApp(100, 24)
	if got := short.layoutFor(short.width, short.height).preview; got != 0 {
		t.Fatalf("24-row terminal got a %d-row preview", got)
	}
	if got := short.layoutFor(short.width, short.height).content; got < 6 {
		t.Fatalf("24-row terminal has only %d pane rows", got)
	}
}

// The band's rows and the panes' rows are complementary: shrinking one must give
// the lost rows to the other so the frame always exactly fills the terminal.
func TestPreviewRowsTradeWithPaneRows(t *testing.T) {
	a := testApp(100, 40)
	shown := a.layoutFor(a.width, a.height)
	a.hidePreview = true
	hidden := a.layoutFor(a.width, a.height)

	gained := hidden.content - shown.content
	if gained != shown.preview+1 {
		t.Fatalf("hiding the preview gave %d pane rows, want %d", gained, shown.preview+1)
	}
}

// The band is the only live view of a long turn, so it has to show the shape of
// the turn — model, running tool, recent tool trail — not just one word.
func TestWorkPreviewShowsTurnShape(t *testing.T) {
	a := testApp(120, 34)
	a.currentModel = map[protocol.AgentID]string{protocol.Austin: "workbuddy/hy4-preview-f"}
	a.currentThinking = map[protocol.AgentID]string{protocol.Austin: "high"}

	for _, activity := range []protocol.ActivityType{
		protocol.ActivityAgentStart, protocol.ActivityToolStart, protocol.ActivityToolEnd,
		protocol.ActivityToolStart, protocol.ActivityToolEnd,
		protocol.ActivityToolStart, protocol.ActivityToolError,
		protocol.ActivityToolStart,
	} {
		a.tracker.Handle(protocol.Austin, activity)
	}
	a.tracker.Note(protocol.Austin, protocol.ActivityAgentStart, "", "")
	a.tracker.Note(protocol.Austin, protocol.ActivityToolStart, "read", "server.js")
	a.tracker.Note(protocol.Austin, protocol.ActivityToolEnd, "read", "")
	a.tracker.Note(protocol.Austin, protocol.ActivityToolStart, "edit", "server.js")
	a.tracker.Note(protocol.Austin, protocol.ActivityToolEnd, "edit", "")
	a.tracker.Note(protocol.Austin, protocol.ActivityToolStart, "bash", "node --check server.js")
	a.tracker.Note(protocol.Austin, protocol.ActivityToolError, "bash", "exit status 1")
	a.tracker.Note(protocol.Austin, protocol.ActivityToolStart, "bash", "go test ./... -count=1")

	plain := ansiPattern.ReplaceAllString(a.buildFrame(renderNormal), "")
	for _, want := range []string{
		"Austin preview",                  // band header
		"▶ bash · go test ./... -count=1", // what it is doing now
		"✓ read", "✓ edit",                // trail
		"✗ bash exit status 1", // failed call keeps its reason
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("preview missing %q:\n%s", want, plain)
		}
	}

	// A thinking agent shows the model and level it is thinking with.
	a.tracker.Handle(protocol.Tony, protocol.ActivityAgentStart)
	a.tracker.Handle(protocol.Tony, protocol.ActivityProviderStart)
	a.tracker.Note(protocol.Tony, protocol.ActivityProviderStart, "", "")
	a.currentModel[protocol.Tony] = "workbuddy/hy4-preview-f"
	a.currentThinking[protocol.Tony] = "xhigh"
	plain = ansiPattern.ReplaceAllString(a.buildFrame(renderNormal), "")
	if !strings.Contains(plain, "◆ hy4-preview-f · xhigh") {
		t.Errorf("preview does not name the thinking model:\n%s", plain)
	}
}

// The preview header is now the only live agent state, so it carries the
// spinner: a running, connected agent must animate frame to frame.
func TestPreviewHeaderAnimatesWhileTheAgentWorks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := testApp(100, 30)

	go func() { _ = a.server.ListenAndServe(ctx) }()
	<-a.server.Ready()
	conn, err := net.Dial("tcp", a.server.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	hello := protocol.Message{Version: protocol.Version, Type: protocol.MsgHello, Agent: protocol.Tony, SessionID: "session", Token: "token"}
	if err := json.NewEncoder(conn).Encode(hello); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); !a.server.IsConnected(protocol.Tony) && time.Now().Before(deadline); time.Sleep(time.Millisecond) {
	}
	if !a.server.IsConnected(protocol.Tony) {
		t.Fatal("Tony did not connect")
	}

	session := agent.NewSession(agent.Config{Agent: protocol.Tony, Dir: t.TempDir(), Command: "sleep 30"})
	a.agents.Add(session)
	if err := session.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer session.Stop()
	a.tracker.Handle(protocol.Tony, protocol.ActivityProviderStart)

	for frame, want := range []string{"thinking |", "thinking /", "thinking -", "thinking \\"} {
		a.frame = frame
		if got := a.previewHeader(protocol.Tony, 40); !strings.Contains(got, want) {
			t.Fatalf("frame %d header = %q, want %q", frame, got, want)
		}
	}
}

// A narrow half drops history from the left and keeps the count, so the newest
// call is never the thing that disappears.
func TestPreviewTrailDropsOldestFirst(t *testing.T) {
	notes := []harness.ToolNote{
		{Name: "read", OK: true, Duration: 2 * time.Second},
		{Name: "edit", OK: true},
		{Name: "bash", OK: false, Detail: "exit status 1"},
	}
	rt := harness.AgentRuntime{Tools: 7, Recent: notes}

	wide := previewTrail(rt, 60)
	if !strings.Contains(wide, "7 tools") || !strings.Contains(wide, "✓ read") || !strings.Contains(wide, "✗ bash exit status 1") {
		t.Fatalf("wide trail = %q", wide)
	}
	narrow := previewTrail(rt, 28)
	if !strings.Contains(narrow, "✗ bash") {
		t.Fatalf("narrow trail dropped the newest call: %q", narrow)
	}
	if strings.Contains(narrow, "✓ read") {
		t.Fatalf("narrow trail kept the oldest call: %q", narrow)
	}
	if displayWidth(narrow) > 27 {
		t.Fatalf("narrow trail is %d columns: %q", displayWidth(narrow), narrow)
	}
}

func TestPreviewHeaderShowsDriver(t *testing.T) {
	a := testApp(140, 30)
	mgr := agent.NewManager()
	mgr.Add(agent.NewAgySession(agent.Config{Agent: protocol.Austin, DriverType: "agy", Model: "gemini-3.8-flash-low"}))
	mgr.Add(agent.NewPiSession(agent.Config{Agent: protocol.Tony, DriverType: "pi", Model: "workbuddy/deepseek-v4.1-flash"}))
	a.agents = mgr

	austinHeader := a.previewHeader(protocol.Austin, 70)
	if !strings.Contains(austinHeader, "Austin preview (agy · gemini-3.8-flash-low)") {
		t.Fatalf("austinHeader = %q, want 'Austin preview (agy · gemini-3.8-flash-low)'", austinHeader)
	}

	tonyHeader := a.previewHeader(protocol.Tony, 70)
	if !strings.Contains(tonyHeader, "Tony preview (pi · workbuddy/deepseek-v4.1-flash)") {
		t.Fatalf("tonyHeader = %q, want 'Tony preview (pi · workbuddy/deepseek-v4.1-flash)'", tonyHeader)
	}
}

type previewMockDriver struct {
	agent.Driver
	agentID    protocol.AgentID
	driverType string
	state      agent.ProcessState
}

func (m *previewMockDriver) Agent() protocol.AgentID       { return m.agentID }
func (m *previewMockDriver) DriverType() string            { return m.driverType }
func (m *previewMockDriver) State() agent.ProcessState     { return m.state }
func (m *previewMockDriver) SetOnExit(fn func(agent.ExitEvent)) {}
func (m *previewMockDriver) Model() string                 { return "" }
func (m *previewMockDriver) SetModel(model string)         {}

func TestPreviewHeaderAgyRunningState(t *testing.T) {
	a := testApp(100, 30)
	mgr := agent.NewManager()
	mgr.Add(&previewMockDriver{agentID: protocol.Austin, driverType: "agy", state: agent.ProcessRunning})
	mgr.Add(&previewMockDriver{agentID: protocol.Tony, driverType: "pi", state: agent.ProcessRunning})
	a.agents = mgr

	// Austin (agy running, no TCP server connection) should show "idle" instead of "connecting"
	austinHeader := a.previewHeader(protocol.Austin, 50)
	if !strings.Contains(austinHeader, "Austin preview (agy") || !strings.Contains(austinHeader, "idle") {
		t.Fatalf("austinHeader = %q, want agy and idle", austinHeader)
	}

	// Tony (pi running, no TCP connection to server) should show "connecting"
	tonyHeader := a.previewHeader(protocol.Tony, 50)
	if !strings.Contains(tonyHeader, "Tony preview (pi") || !strings.Contains(tonyHeader, "connecting") {
		t.Fatalf("tonyHeader = %q, want pi and connecting", tonyHeader)
	}

	// When agy process fails, it should show "failed [Restart]"
	mgr.Add(&previewMockDriver{agentID: protocol.Austin, driverType: "agy", state: agent.ProcessFailed})
	austinFailedHeader := a.previewHeader(protocol.Austin, 50)
	if !strings.Contains(austinFailedHeader, "failed [Restart]") {
		t.Fatalf("austinFailedHeader = %q, want 'failed [Restart]'", austinFailedHeader)
	}
}
