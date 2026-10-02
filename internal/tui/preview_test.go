package tui

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/events"
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

func TestPreviewHeaderShowsContextAndLiveTokenRate(t *testing.T) {
	for _, tc := range []struct {
		driver  string
		agent   protocol.AgentID
		tokens  int
		window  int
		speed   float64
		wantCtx string
		wantSpd string
	}{
		{driver: "pi", agent: protocol.Austin, tokens: 45000, window: 200000, speed: 32.5, wantCtx: "ctx 45k/200k", wantSpd: "32 tok/s"},
		{driver: "agy", agent: protocol.Austin, tokens: 50000, window: 1048576, speed: 65.2, wantCtx: "ctx 50k/1.0M", wantSpd: "65 tok/s"},
		{driver: "opencode", agent: protocol.Tony, tokens: 12000, window: 128000, speed: 24.8, wantCtx: "ctx 12k/128k", wantSpd: "25 tok/s"},
	} {
		t.Run(tc.driver, func(t *testing.T) {
			a := testApp(140, 30)
			mgr := agent.NewManager()
			mgr.Add(&previewMockDriver{agentID: tc.agent, driverType: tc.driver, state: agent.ProcessRunning})
			a.agents = mgr

			// Live streaming state shows both context usage and active token rate
			if tc.driver == "agy" {
				a.tracker.Handle(tc.agent, protocol.ActivityAgentStart)
			} else {
				a.tracker.Handle(tc.agent, protocol.ActivityProviderStart)
			}
			a.tracker.Handle(tc.agent, protocol.ActivityStream)
			a.tracker.UpdateUsage(tc.agent, tc.tokens, tc.window, tc.speed)
			header := a.previewHeader(tc.agent, 120)
			if !strings.Contains(header, tc.wantCtx) || !strings.Contains(header, tc.wantSpd) {
				t.Fatalf("%s header = %q, want %q and %q", tc.driver, header, tc.wantCtx, tc.wantSpd)
			}

			// Tool execution clears the active token speed but retains context usage
			a.tracker.Handle(tc.agent, protocol.ActivityToolStart)
			header = a.previewHeader(tc.agent, 120)
			if !strings.Contains(header, tc.wantCtx) || strings.Contains(header, "tok/s") {
				t.Fatalf("%s tool execution header = %q, want retained %q without active token rate", tc.driver, header, tc.wantCtx)
			}

			// Idle state retains context without token rate
			if tc.driver == "agy" {
				a.tracker.Handle(tc.agent, protocol.ActivityAgentSettled)
			} else {
				a.tracker.Handle(tc.agent, protocol.ActivityProviderEnd)
			}
			header = a.previewHeader(tc.agent, 120)
			if !strings.Contains(header, tc.wantCtx) || strings.Contains(header, "tok/s") {
				t.Fatalf("%s idle header = %q, want retained %q without inactive token rate", tc.driver, header, tc.wantCtx)
			}
		})
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

func (m *previewMockDriver) Agent() protocol.AgentID            { return m.agentID }
func (m *previewMockDriver) DriverType() string                 { return m.driverType }
func (m *previewMockDriver) State() agent.ProcessState          { return m.state }
func (m *previewMockDriver) SetOnExit(fn func(agent.ExitEvent)) {}
func (m *previewMockDriver) Model() string                      { return "" }
func (m *previewMockDriver) SetModel(model string)              {}

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

// A dead process must not read as "waiting": that hides the one fact that
// explains an agent which never connected.
func TestPreviewBodyReportsExitedProcessInsteadOfWaiting(t *testing.T) {
	for _, tc := range []struct {
		state agent.ProcessState
		want  string
	}{
		{agent.ProcessExited, "exited"},
		{agent.ProcessFailed, "failed"},
		{agent.ProcessStopping, "stopping"},
	} {
		a := testApp(100, 30)
		mgr := agent.NewManager()
		mgr.Add(&previewMockDriver{agentID: protocol.Austin, driverType: "opencode", state: tc.state})
		mgr.Add(&previewMockDriver{agentID: protocol.Tony, driverType: "pi", state: agent.ProcessRunning})
		a.agents = mgr

		line := a.previewNow(protocol.Austin, a.tracker.Snapshot(protocol.Austin))
		if strings.Contains(line, "waiting") {
			t.Errorf("state %v: preview line = %q, must not read as waiting", tc.state, line)
		}
		if !strings.Contains(line, tc.want) {
			t.Errorf("state %v: preview line = %q, want it to mention %q", tc.state, line, tc.want)
		}
	}
}

// opencode's bridge only attaches once its TUI has a session, so before the
// first task it has no socket at all. A running process is still a present agent
// and must not be reported as "connecting".
func TestPreviewHeaderTreatsRunningOpencodeAsPresent(t *testing.T) {
	a := testApp(100, 30)
	mgr := agent.NewManager()
	mgr.Add(&previewMockDriver{agentID: protocol.Austin, driverType: "opencode", state: agent.ProcessRunning})
	mgr.Add(&previewMockDriver{agentID: protocol.Tony, driverType: "pi", state: agent.ProcessRunning})
	a.agents = mgr

	header := a.previewHeader(protocol.Austin, 60)
	if !strings.Contains(header, "opencode") {
		t.Fatalf("header = %q, want the opencode driver shown", header)
	}
	if strings.Contains(header, "connecting") {
		t.Fatalf("header = %q, want a running opencode agent to count as present", header)
	}

	// Once the process is gone it must report that instead.
	mgr.Add(&previewMockDriver{agentID: protocol.Austin, driverType: "opencode", state: agent.ProcessExited})
	if header := a.previewHeader(protocol.Austin, 60); !strings.Contains(header, "exited") {
		t.Fatalf("header = %q, want exited", header)
	}
}

// Both agents are launched before the TUI subscribes to the bus, so a driver
// that cannot announce itself over the bridge has to be announced from state at
// timeline start. Otherwise it looks absent until its first task.
func TestAnnounceRunningAgentsCoversBridgeLessDrivers(t *testing.T) {
	a := testApp(100, 30)
	mgr := agent.NewManager()
	mgr.Add(&previewMockDriver{agentID: protocol.Austin, driverType: "opencode", state: agent.ProcessRunning})
	mgr.Add(&previewMockDriver{agentID: protocol.Tony, driverType: "pi", state: agent.ProcessRunning})
	a.agents = mgr

	a.announceRunningAgents()

	joined := timelineText(a)
	if !strings.Contains(joined, "Austin connected") {
		t.Fatalf("timeline = %q, want Austin announced", joined)
	}
	if strings.Contains(joined, "Tony connected") {
		t.Fatalf("timeline = %q, pi announces itself over the bridge and must not be repeated", joined)
	}

	// An agent that is not running must not be announced.
	a2 := testApp(100, 30)
	mgr2 := agent.NewManager()
	mgr2.Add(&previewMockDriver{agentID: protocol.Austin, driverType: "opencode", state: agent.ProcessExited})
	mgr2.Add(&previewMockDriver{agentID: protocol.Tony, driverType: "pi", state: agent.ProcessRunning})
	a2.agents = mgr2
	a2.announceRunningAgents()
	if joined := timelineText(a2); strings.Contains(joined, "Austin connected") {
		t.Fatalf("timeline = %q, want no announcement for an exited agent", joined)
	}
}

// timelineText flattens every pane's entries so a test can assert on what the
// human would read, regardless of which pane a message landed in.
func timelineText(a *App) string {
	var texts []string
	for _, list := range [][]entry{a.austin, a.tony, a.duo} {
		for _, e := range list {
			texts = append(texts, e.text)
		}
	}
	return strings.Join(texts, "\n")
}

func makePreviewProtoPayload(prompt, candidates, cached int, model string) []byte {
	var f2 []byte
	f2 = binary.AppendUvarint(f2, (2<<3)|0)
	f2 = binary.AppendUvarint(f2, uint64(prompt))
	f2 = binary.AppendUvarint(f2, (3<<3)|0)
	f2 = binary.AppendUvarint(f2, uint64(candidates))
	f2 = binary.AppendUvarint(f2, (5<<3)|0)
	f2 = binary.AppendUvarint(f2, uint64(cached))

	var f17 []byte
	f17 = binary.AppendUvarint(f17, (2<<3)|2)
	f17 = binary.AppendUvarint(f17, uint64(len(f2)))
	f17 = append(f17, f2...)

	var f1 []byte
	f1 = binary.AppendUvarint(f1, (17<<3)|2)
	f1 = binary.AppendUvarint(f1, uint64(len(f17)))
	f1 = append(f1, f17...)
	f1 = binary.AppendUvarint(f1, (19<<3)|2)
	f1 = binary.AppendUvarint(f1, uint64(len(model)))
	f1 = append(f1, []byte(model)...)

	var top []byte
	top = binary.AppendUvarint(top, (1<<3)|2)
	top = binary.AppendUvarint(top, uint64(len(f1)))
	top = append(top, f1...)
	return top
}

func createTestDBForPreview(t *testing.T, dir string, populateMode string) string {
	t.Helper()
	dbPath := filepath.Join(dir, "preview_test.db")
	switch populateMode {
	case "with_data":
		proto := makePreviewProtoPayload(5000, 250, 45000, "gemini-3.8-flash")
		hexStr := hex.EncodeToString(proto)
		sql := fmt.Sprintf("CREATE TABLE gen_metadata (idx INTEGER PRIMARY KEY, data BLOB); INSERT INTO gen_metadata (idx, data) VALUES (1, X'%s');", hexStr)
		cmd := exec.Command("sqlite3", dbPath, sql)
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to create sqlite3 test db: %v", err)
		}
	case "no_rows":
		sql := "CREATE TABLE gen_metadata (idx INTEGER PRIMARY KEY, data BLOB);"
		cmd := exec.Command("sqlite3", dbPath, sql)
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to create sqlite3 test db: %v", err)
		}
	case "empty_db":
		f, err := os.Create(dbPath)
		if err != nil {
			t.Fatalf("failed to create empty db file: %v", err)
		}
		f.Close()
	}
	return dbPath
}

func TestPreviewAgySqlite3DiagnosticsTableDriven(t *testing.T) {
	origLookPath := agent.LookPath
	defer func() {
		agent.LookPath = origLookPath
	}()

	tests := []struct {
		name              string
		sqlite3Exists     bool
		dbMode            string
		wantHeaderContain string
		wantHeaderReject  string
		wantTimeline      bool
	}{
		{
			name:              "sqlite3 exists and has gen_metadata rows",
			sqlite3Exists:     true,
			dbMode:            "with_data",
			wantHeaderContain: "ctx 50k/1.0M",
			wantHeaderReject:  "no sqlite3",
			wantTimeline:      false,
		},
		{
			name:              "sqlite3 does not exist",
			sqlite3Exists:     false,
			dbMode:            "with_data",
			wantHeaderContain: "no sqlite3",
			wantHeaderReject:  "ctx ",
			wantTimeline:      true,
		},
		{
			name:              "sqlite3 exists but database is empty",
			sqlite3Exists:     true,
			dbMode:            "empty_db",
			wantHeaderContain: "",
			wantHeaderReject:  "no sqlite3",
			wantTimeline:      false,
		},
		{
			name:              "sqlite3 exists but no gen_metadata rows",
			sqlite3Exists:     true,
			dbMode:            "no_rows",
			wantHeaderContain: "",
			wantHeaderReject:  "no sqlite3",
			wantTimeline:      false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.sqlite3Exists {
				agent.LookPath = exec.LookPath
			} else {
				agent.LookPath = func(string) (string, error) {
					return "", exec.ErrNotFound
				}
			}

			tmpDir := t.TempDir()
			dbPath := createTestDBForPreview(t, tmpDir, tc.dbMode)

			a := testApp(140, 30)
			mgr := agent.NewManager()
			mgr.Add(&previewMockDriver{agentID: protocol.Austin, driverType: "agy", state: agent.ProcessRunning})
			a.agents = mgr

			// Simulate token usage query as done by watcher
			if usage, ok := agent.QueryLatestAgyUsage(dbPath); ok {
				a.tracker.UpdateUsage(protocol.Austin, usage.TotalInputTokens, usage.ContextWindow, 0)
			}

			a.announceRunningAgents()

			header := a.previewHeader(protocol.Austin, 120)
			if tc.wantHeaderContain != "" && !strings.Contains(header, tc.wantHeaderContain) {
				t.Errorf("previewHeader = %q, want it to contain %q", header, tc.wantHeaderContain)
			}
			if tc.wantHeaderReject != "" && strings.Contains(header, tc.wantHeaderReject) {
				t.Errorf("previewHeader = %q, want it NOT to contain %q", header, tc.wantHeaderReject)
			}
			if tc.dbMode == "empty_db" || tc.dbMode == "no_rows" {
				if strings.Contains(header, "ctx ") {
					t.Errorf("previewHeader = %q, want no context tokens displayed when database has no data", header)
				}
			}

			tl := timelineText(a)
			if tc.wantTimeline {
				if !strings.Contains(tl, agent.AgySqlite3MissingNotice) {
					t.Errorf("timeline = %q, want diagnostic notice %q", tl, agent.AgySqlite3MissingNotice)
				}
			} else {
				if strings.Contains(tl, "sqlite3 is required") {
					t.Errorf("timeline = %q, want no sqlite3 missing notice", tl)
				}
			}
		})
	}
}

func TestPreviewAgySqlite3PreservesLiveTokenSpeed(t *testing.T) {
	origLookPath := agent.LookPath
	defer func() { agent.LookPath = origLookPath }()

	agent.LookPath = func(string) (string, error) {
		return "", exec.ErrNotFound
	}

	a := testApp(140, 30)
	mgr := agent.NewManager()
	mgr.Add(&previewMockDriver{agentID: protocol.Austin, driverType: "agy", state: agent.ProcessRunning})
	a.agents = mgr

	a.tracker.Handle(protocol.Austin, protocol.ActivityAgentStart)
	a.tracker.Handle(protocol.Austin, protocol.ActivityStream)
	a.tracker.UpdateUsage(protocol.Austin, 0, 0, 42.0)

	header := a.previewHeader(protocol.Austin, 120)
	if !strings.Contains(header, "42 tok/s · no sqlite3") {
		t.Fatalf("previewHeader = %q, want '42 tok/s · no sqlite3'", header)
	}

	// Tool execution clears the active token speed; "no sqlite3" badge persists
	a.tracker.Handle(protocol.Austin, protocol.ActivityToolStart)
	header = a.previewHeader(protocol.Austin, 120)
	if !strings.Contains(header, "no sqlite3") || strings.Contains(header, "tok/s") {
		t.Fatalf("tool execution header = %q, want 'no sqlite3' without token rate", header)
	}

	// Idle state retains "no sqlite3" without token rate
	a.tracker.Handle(protocol.Austin, protocol.ActivityAgentSettled)
	header = a.previewHeader(protocol.Austin, 120)
	if !strings.Contains(header, "no sqlite3") || strings.Contains(header, "tok/s") {
		t.Fatalf("idle header = %q, want 'no sqlite3' without token rate", header)
	}
}

func TestPreviewAgySqlite3WatcherAndTUIInteraction(t *testing.T) {
	origLookPath := agent.LookPath
	defer func() { agent.LookPath = origLookPath }()

	agent.LookPath = func(string) (string, error) {
		return "", exec.ErrNotFound
	}

	// 1. Construct watcher first (simulating watcher start before TUI subscribes to bus)
	w := agent.NewAgyWatcher(protocol.Austin, "/tmp/nonexistent/transcript.jsonl", nil)
	w.ProcessLine([]byte(`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-10-02T00:00:00Z","content":"start"}`))

	a := testApp(140, 30)
	mgr := agent.NewManager()
	mgr.Add(&previewMockDriver{agentID: protocol.Austin, driverType: "agy", state: agent.ProcessRunning})
	a.agents = mgr

	// 2. TUI startup announces running agents
	a.announceRunningAgents()

	tl := timelineText(a)
	if !strings.Contains(tl, agent.AgySqlite3MissingNotice) {
		t.Fatalf("timeline = %q, want diagnostic notice even when watcher was constructed first", tl)
	}

	// 3. Repeated announcements or incoming events must not duplicate the notice
	a.announceRunningAgents()
	a.route(events.Event{Agent: protocol.Austin, Kind: events.KindActivity})
	count := strings.Count(timelineText(a), agent.AgySqlite3MissingNotice)
	if count != 1 {
		t.Fatalf("expected notice once, got %d times in timeline: %q", count, timelineText(a))
	}
}
