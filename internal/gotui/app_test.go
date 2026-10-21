package gotui

import (
	"context"
	"image"
	"io"
	"strings"
	"sync"
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
	"github.com/gdamore/tcell/v3"
	ui "github.com/metaspartan/gotui/v5"
)

type fakeSession struct {
	mu       sync.Mutex
	view     session.View
	history  []sessionstore.TUIEntry
	events   chan events.Event
	recorded []sessionstore.TUIEntry
	commands []session.Command
	loads    []protocol.AgentID
	writes   map[protocol.AgentID][][]byte
	detached []protocol.AgentID
	cols     int
	rows     int
}

func newFakeSession() *fakeSession {
	manifest := &driver.Manifest{
		Name:           "test",
		DefaultModel:   "openai/gpt-5",
		ModelReference: driver.ModelQualified,
		Capabilities: driver.Capabilities{
			Models:      true,
			Thinking:    true,
			SelfReports: true,
		},
	}
	agentView := func(id protocol.AgentID, color string) session.AgentView {
		return session.AgentView{
			Present:   true,
			Connected: true,
			Process:   agent.ProcessRunning,
			Driver:    "test",
			Command:   "test-agent",
			Model:     color,
			Thinking:  "high",
			Manifest:  manifest,
			Runtime: harness.AgentRuntime{
				Busy:          id == protocol.Austin,
				Tool:          "Read",
				ContextTokens: 12000,
				ContextWindow: 200000,
				Tools:         3,
				TurnStarted:   time.Now().Add(-8 * time.Second),
			},
		}
	}
	return &fakeSession{
		view: session.View{
			Version:   "test",
			SessionID: "session-1",
			Language:  "en",
			Project: project.Snapshot{
				Mode:    project.ModeFast,
				Phase:   project.PhaseRunning,
				Started: true,
			},
			Worktrees: workspace.Set{
				Repository: "/tmp/example",
				BaseBranch: "main",
				Session:    "session-1",
			},
			Agents: map[protocol.AgentID]session.AgentView{
				protocol.Austin: agentView(protocol.Austin, "openai/gpt-5"),
				protocol.Tony:   agentView(protocol.Tony, "anthropic/claude"),
			},
		},
		history: []sessionstore.TUIEntry{
			{Time: time.Now().Add(-2 * time.Minute), Pane: "Duo", Text: "old task", Label: "Human → Austin"},
			{Time: time.Now().Add(-time.Minute), Pane: "Austin", Text: "old answer", Label: "Austin → Human"},
		},
		events: make(chan events.Event, 256),
		writes: map[protocol.AgentID][][]byte{},
	}
}

func (f *fakeSession) View() session.View {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view
}

func (f *fakeSession) History() []sessionstore.TUIEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sessionstore.TUIEntry(nil), f.history...)
}

func (f *fakeSession) Subscribe(buffer int) (session.Subscription, func()) {
	return session.Subscription{View: f.View(), Events: f.events}, func() {}
}

func (f *fakeSession) Do(ctx context.Context, cmd session.Command) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, cmd)
	if set, ok := cmd.(session.SetLanguage); ok {
		f.view.Language = strings.ToLower(set.Language)
	}
	return nil
}

func (f *fakeSession) Models(ctx context.Context, id protocol.AgentID) ([]models.Model, error) {
	f.mu.Lock()
	f.loads = append(f.loads, id)
	f.mu.Unlock()
	return []models.Model{
		{Provider: "openai", ID: "gpt-5", Thinking: true, Images: true},
		{Provider: "anthropic", ID: "claude", Thinking: true},
	}, nil
}

func (f *fakeSession) StatusText(ctx context.Context) string { return "STATUS OK" }

func (f *fakeSession) Changes(ctx context.Context) []string {
	return []string{"M internal/gotui/app.go", "A internal/gotui/picker.go"}
}

func (f *fakeSession) Notices() []string { return nil }

func (f *fakeSession) RecordEntry(entry sessionstore.TUIEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recorded = append(f.recorded, entry)
}

func (f *fakeSession) Attach(id protocol.AgentID, w io.Writer) ([]byte, error) {
	return []byte("native history"), nil
}

func (f *fakeSession) Detach(id protocol.AgentID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.detached = append(f.detached, id)
}

func (f *fakeSession) Write(id protocol.AgentID, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes[id] = append(f.writes[id], append([]byte(nil), data...))
	return nil
}

func (f *fakeSession) Resize(cols, rows int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cols, f.rows = cols, rows
	return nil
}

func (f *fakeSession) snapshotCommands() []session.Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]session.Command(nil), f.commands...)
}

func (f *fakeSession) snapshotRecorded() []sessionstore.TUIEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sessionstore.TUIEntry(nil), f.recorded...)
}

func TestSimulationDashboardCommandsAndOverlays(t *testing.T) {
	fake := newFakeSession()
	app := New(fake)
	backend, err := ui.NewBackend(&ui.InitConfig{SimulationMode: true, SimulationSize: image.Pt(120, 42)})
	if err != nil {
		t.Fatal(err)
	}
	sim, ok := backend.Screen.(tcell.SimulationScreen)
	if !ok {
		t.Fatalf("screen %T is not a simulation screen", backend.Screen)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.runWithBackend(ctx, backend) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("runWithBackend did not stop")
		}
	})

	waitForText(t, sim, "DUO")
	text := screenText(sim)
	for _, want := range []string{"WORKFLOW", "AUSTIN", "TONY", "DUO / HUMAN", "old answer", "old task"} {
		if !strings.Contains(text, want) {
			t.Fatalf("initial screen missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "ready (FAST mode)") {
		t.Fatalf("resumed session showed a startup banner:\n%s", text)
	}

	// Unicode survives both the composer and the transcript.
	typeText(sim, "hello 你好")
	waitForText(t, sim, "hello 你")
	waitForText(t, sim, "好")
	injectKey(sim, tcell.KeyCtrlU, 0, tcell.ModNone)
	waitGone(t, sim, "hello 你")
	typeText(sim, "hello")
	injectKey(sim, tcell.KeyEnter, 0, tcell.ModNone)
	waitForCommands(t, fake, func(commands []session.Command) bool {
		for _, command := range commands {
			if _, ok := command.(session.SubmitTask); ok {
				return true
			}
		}
		return false
	})

	fake.events <- events.Event{Time: time.Now(), Kind: events.KindAssistant, Agent: protocol.Austin, Text: "new answer"}
	waitForText(t, sim, "new answer")
	recorded := fake.snapshotRecorded()
	for _, item := range recorded {
		if strings.Contains(item.Text, "old answer") {
			t.Fatalf("restored history was recorded again: %+v", item)
		}
	}
	foundNew := false
	for _, item := range recorded {
		if strings.Contains(item.Text, "new answer") {
			foundNew = true
		}
	}
	if !foundNew {
		t.Fatalf("new assistant message was not recorded: %+v", recorded)
	}

	// Slash palette: filter, close without running.
	typeText(sim, "/")
	waitForText(t, sim, "COMMANDS")
	// The command column must show the full mode spelling; a fixed narrow column
	// used to truncate it to `/mode [fast|goal…`.
	waitForText(t, sim, "/mode [fast|goal|step]")
	typeText(sim, "he")
	waitForText(t, sim, "Help and workflow guide")
	injectKey(sim, tcell.KeyEsc, 0, tcell.ModNone)
	waitGone(t, sim, "COMMANDS")
	injectKey(sim, tcell.KeyCtrlU, 0, tcell.ModNone)
	waitGone(t, sim, "/he")

	// Help and details are independent scrollable overlays.
	injectKey(sim, tcell.KeyF1, 0, tcell.ModNone)
	waitForText(t, sim, "NATIVE TERMINALS")
	injectKey(sim, tcell.KeyEsc, 0, tcell.ModNone)
	waitGone(t, sim, "NATIVE TERMINALS")

	injectKey(sim, tcell.KeyCtrlO, 0, tcell.ModNone)
	waitForText(t, sim, "SESSION DETAILS")
	waitForText(t, sim, "STATUS OK")
	waitForText(t, sim, "M internal/gotui/app.go")
	injectKey(sim, tcell.KeyEsc, 0, tcell.ModNone)
	waitGone(t, sim, "SESSION DETAILS")

	// The model picker reads the catalog asynchronously, then applies a choice.
	injectKey(sim, tcell.KeyCtrlM, 0, tcell.ModNone)
	waitForText(t, sim, "MODELS & THINKING")
	waitForText(t, sim, "openai/gpt-5")
	injectKey(sim, tcell.KeyEnter, 0, tcell.ModNone)
	waitGone(t, sim, "MODELS & THINKING")
	waitForCommands(t, fake, func(commands []session.Command) bool {
		for _, command := range commands {
			if set, ok := command.(session.SetModel); ok && set.Provider == "openai" && set.Model == "gpt-5" {
				return true
			}
		}
		return false
	})

	// Quit is confirmed, and the default is Cancel.
	injectKey(sim, tcell.KeyCtrlQ, 0, tcell.ModNone)
	waitForText(t, sim, "Quit Duo?")
	injectKey(sim, tcell.KeyRune, 'n', tcell.ModNone)
	waitGone(t, sim, "Quit Duo?")

	// A narrow terminal remains usable and drops the sidebar.
	sim.SetSize(84, 26)
	waitGone(t, sim, "WORKFLOW")
	waitForText(t, sim, "AUSTIN")
}

func waitForText(t *testing.T, sim tcell.SimulationScreen, want string) {
	t.Helper()
	waitScreen(t, sim, func(text string) bool { return strings.Contains(text, want) }, "text "+want)
}

func waitGone(t *testing.T, sim tcell.SimulationScreen, unwanted string) {
	t.Helper()
	waitScreen(t, sim, func(text string) bool { return !strings.Contains(text, unwanted) }, "removal of "+unwanted)
}

func waitScreen(t *testing.T, sim tcell.SimulationScreen, predicate func(string) bool, label string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		text := screenText(sim)
		if predicate(text) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s:\n%s", label, screenText(sim))
}

func screenText(sim tcell.SimulationScreen) string {
	w, h := sim.Size()
	var b strings.Builder
	for y := 0; y < h; y++ {
		// Read logical cells under tcell's lock. Get returns a wide rune with
		// its display width, so skip its continuation cell instead of emitting
		// the blank placeholder that GetContents exposes there.
		for x := 0; x < w; {
			text, _, width := sim.Get(x, y)
			if width < 1 {
				width = 1
			}
			if text == "" {
				b.WriteByte(' ')
			} else {
				b.WriteString(text)
			}
			x += width
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func typeText(sim tcell.SimulationScreen, text string) {
	for _, r := range text {
		sim.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
}

func injectKey(sim tcell.SimulationScreen, key tcell.Key, r rune, mod tcell.ModMask) {
	sim.InjectKey(key, r, mod)
}

func waitForCommands(t *testing.T, fake *fakeSession, predicate func([]session.Command) bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if predicate(fake.snapshotCommands()) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for command; got %#v", fake.snapshotCommands())
}

// The language command must change the dashboard chrome live, not only the
// Duo-authored notices. This pins the shared theme language used by every
// already-constructed panel and the translated slash descriptions.
// Clicking a sidebar panel expands it and shrinks the other two. Clicking a
// different panel moves the expansion; clicking the expanded panel restores
// the even layout.
func TestSidebarPanelClickExpands(t *testing.T) {
	fake := newFakeSession()
	austin := fake.view.Agents[protocol.Austin]
	austin.Runtime.Recent = []harness.ToolNote{{Name: "RECENT-TOOL-MARKER", OK: true, Duration: time.Second}}
	austin.Runtime.LastError = "ERROR-MARKER"
	fake.view.Agents[protocol.Austin] = austin
	app := New(fake)
	backend, err := ui.NewBackend(&ui.InitConfig{SimulationMode: true, SimulationSize: image.Pt(120, 42)})
	if err != nil {
		t.Fatal(err)
	}
	sim, ok := backend.Screen.(tcell.SimulationScreen)
	if !ok {
		t.Fatalf("screen %T is not a simulation screen", backend.Screen)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.runWithBackend(ctx, backend) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("runWithBackend did not stop")
		}
	})

	waitForText(t, sim, "AUSTIN ▸")
	if strings.Contains(screenText(sim), "RECENT-TOOL-MARKER") {
		t.Fatal("compact Austin card unexpectedly showed its recent-tool history")
	}
	sim.InjectMouse(10, 20, tcell.Button1, tcell.ModNone)
	waitForText(t, sim, "AUSTIN ▾")
	waitForText(t, sim, "RECENT-TOOL-MARKER")
	waitForText(t, sim, "ERROR-MARKER")

	// The expanded Austin card now occupies the middle of the sidebar.
	sim.InjectMouse(10, 20, tcell.Button1, tcell.ModNone)
	waitGone(t, sim, "AUSTIN ▾")

	sim.InjectMouse(10, 5, tcell.Button1, tcell.ModNone)
	waitForText(t, sim, "WORKFLOW ▾")

	// Clicking Tony moves the expansion instead of stacking two open panels.
	sim.InjectMouse(10, 34, tcell.Button1, tcell.ModNone)
	waitForText(t, sim, "TONY ▾")
	waitForText(t, sim, "WORKFLOW ▸")
}

// Expanding the workflow panel must make room for the full current step, not
// just draw an empty taller border. The marker only fits after the title wraps
// across the additional rows.
func TestExpandedWorkflowWrapsCurrentStep(t *testing.T) {
	fake := newFakeSession()
	fake.view.Project.Mode = project.ModeStep
	fake.view.Project.Phase = project.PhaseExecute
	fake.view.Project.Plan = strings.Repeat("plan ", 20)
	fake.view.Project.PlanVersion = 4
	fake.view.Project.Step = 1
	fake.view.Project.Steps = project.Steps{
		{Title: strings.Repeat("多行步骤信息 ", 30) + "TAIL-MARKER", Policy: project.ModeFast},
		{Title: "next step", Policy: project.ModeFast},
	}

	app := New(fake)
	backend, err := ui.NewBackend(&ui.InitConfig{SimulationMode: true, SimulationSize: image.Pt(120, 42)})
	if err != nil {
		t.Fatal(err)
	}
	sim, ok := backend.Screen.(tcell.SimulationScreen)
	if !ok {
		t.Fatalf("screen %T is not a simulation screen", backend.Screen)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.runWithBackend(ctx, backend) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("runWithBackend did not stop")
		}
	})

	waitForText(t, sim, "WORKFLOW ▸")
	if strings.Contains(screenText(sim), "TAIL-MARKER") {
		t.Fatal("the collapsed workflow panel unexpectedly showed the whole step title")
	}
	sim.InjectMouse(10, 5, tcell.Button1, tcell.ModNone)
	waitForText(t, sim, "WORKFLOW ▾")
	waitForText(t, sim, "TAIL-MARKER")
}

func TestLanguageCommandTranslatesDashboard(t *testing.T) {
	fake := newFakeSession()
	app := New(fake)
	backend, err := ui.NewBackend(&ui.InitConfig{SimulationMode: true, SimulationSize: image.Pt(120, 42)})
	if err != nil {
		t.Fatal(err)
	}
	sim, ok := backend.Screen.(tcell.SimulationScreen)
	if !ok {
		t.Fatalf("screen %T is not a simulation screen", backend.Screen)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.runWithBackend(ctx, backend) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("runWithBackend did not stop")
		}
	})

	waitForText(t, sim, "WORKFLOW")
	typeText(sim, "/language zh")
	injectKey(sim, tcell.KeyEnter, 0, tcell.ModNone)
	waitForText(t, sim, "工作流")
	waitForText(t, sim, "语言 →")

	typeText(sim, "/")
	waitForText(t, sim, "命令")
	waitForText(t, sim, "切换会话模式")
}

// Selecting Austin or Tony in the picker must also choose the target that
// Shift+Tab cycles after the picker closes. Without that, the status can say
// "Tony thinking" while Shift+Tab silently changes Austin.
func TestThinkingTargetFollowsPickerAgent(t *testing.T) {
	fake := newFakeSession()
	app := New(fake)
	backend, err := ui.NewBackend(&ui.InitConfig{SimulationMode: true, SimulationSize: image.Pt(120, 42)})
	if err != nil {
		t.Fatal(err)
	}
	sim, ok := backend.Screen.(tcell.SimulationScreen)
	if !ok {
		t.Fatalf("screen %T is not a simulation screen", backend.Screen)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.runWithBackend(ctx, backend) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("runWithBackend did not stop")
		}
	})

	waitForText(t, sim, "DUO")
	injectKey(sim, tcell.KeyCtrlM, 0, tcell.ModNone)
	waitForText(t, sim, "MODELS & THINKING")
	injectKey(sim, tcell.KeyTab, 0, tcell.ModNone)
	injectKey(sim, tcell.KeyBacktab, 0, tcell.ModNone)
	waitForCommands(t, fake, func(commands []session.Command) bool {
		for _, command := range commands {
			if cycle, ok := command.(session.CycleThinking); ok && cycle.Agent == protocol.Tony {
				return true
			}
		}
		return false
	})
	injectKey(sim, tcell.KeyEsc, 0, tcell.ModNone)
}

func TestThinkingEventUpdatesCardsWithoutSingleAgentStatus(t *testing.T) {
	fake := newFakeSession()
	app := New(fake)
	app.overlay = overlayPicker
	app.picker.thinking = "medium"
	app.route(events.Event{Time: time.Now(), Kind: events.KindThinking, Agent: protocol.Tony, Thinking: "high"})
	if got := app.currentThink[protocol.Tony]; got != "high" {
		t.Fatalf("Tony thinking state = %q, want high", got)
	}
	if got := app.picker.thinking; got != "high" {
		t.Fatalf("picker thinking state = %q, want the confirmed high", got)
	}
	if app.status != "" {
		t.Fatalf("thinking event set a single-agent status %q; the agent cards show both levels", app.status)
	}
}

func TestMainViewBacktabDoesNotChangeThinking(t *testing.T) {
	fake := newFakeSession()
	app := New(fake)
	app.handleKey(tcell.NewEventKey(tcell.KeyBacktab, "", tcell.ModNone))
	if commands := fake.snapshotCommands(); len(commands) != 0 {
		t.Fatalf("main-view Shift+Tab sent commands %#v; it must only work in the picker", commands)
	}
}

func TestNewSessionNoticeDoesNotSuppressStartup(t *testing.T) {
	fake := newFakeSession()
	fake.history = nil
	app := New(fake)
	app.Notices([]string{"driver warning"})
	if app.hadHistory {
		t.Fatal("a driver notice must not count as restored conversation history")
	}
}

func TestNativeDetach(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		detach  bool
		forward string
	}{
		{"legacy Ctrl+]", "\x1d", true, ""},
		{"legacy Ctrl+\\", "\x1c", true, ""},
		{"text before legacy detach", "hi\x1d", true, "hi"},
		{"CSI-u Ctrl+]", "\x1b[93;5u", true, ""},
		{"CSI-u Ctrl+】", "\x1b[12305;5u", true, ""},
		{"CSI-u event variant", "\x1b[93;5:1u", true, ""},
		{"CSI-u Ctrl with Shift", "\x1b[92;6u", true, ""},
		{"modifyOtherKeys Ctrl+]", "\x1b[27;5;93~", true, ""},
		{"modifyOtherKeys Ctrl+】", "\x1b[27;5;12305~", true, ""},
		{"modifyOtherKeys Ctrl with Alt", "\x1b[27;7;92~", true, ""},
		{"similar CSI-u without Ctrl", "\x1b[93;4u", false, "\x1b[93;4u"},
		{"similar modifyOtherKeys without Ctrl", "\x1b[27;4;93~", false, "\x1b[27;4;93~"},
		{"ordinary input", "hello", false, "hello"},
		{"native Ctrl+/ passes to attached agent", "\x1f", false, "\x1f"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &App{}
			forward, consumed, detach := a.nativeInputChunk([]byte(tt.input))
			if detach != tt.detach || string(forward) != tt.forward || consumed != len(tt.input) {
				t.Fatalf("detach=%v, forward=%q, consumed=%d; want detach=%v, forward=%q, consumed=%d",
					detach, forward, consumed, tt.detach, tt.forward, len(tt.input))
			}
		})
	}
}

func TestNativeInputAssemblerKeepsTerminalSequencesIntact(t *testing.T) {
	for _, sequence := range []string{"\x1b[1;1R", "\x1b[<35;1;1M", "\x1b]11;rgb:158e/193a/1e75\x1b\\"} {
		t.Run(sequence, func(t *testing.T) {
			var assembler nativeInputAssembler
			var writes [][]byte
			for _, chunk := range [][]byte{[]byte(sequence[:1]), []byte(sequence[1:])} {
				var write []byte
				for _, b := range chunk {
					write = append(write, assembler.add(b)...)
				}
				if len(write) > 0 {
					writes = append(writes, write)
				}
			}
			if len(writes) != 1 || string(writes[0]) != sequence || len(assembler.pending) != 0 {
				t.Fatalf("writes = %q, pending %q; want one write %q", writes, assembler.pending, sequence)
			}
		})
	}
}

func TestNativeInputAssemblerPreservesOrdinaryKeysAndEscapeTimeout(t *testing.T) {
	var assembler nativeInputAssembler
	if got := string(assembler.add('x')); got != "x" {
		t.Fatalf("ordinary key = %q", got)
	}
	if got := assembler.add(0x1b); len(got) != 0 {
		t.Fatalf("Escape emitted early: %q", got)
	}
	if got := string(assembler.flush()); got != "\x1b" {
		t.Fatalf("timed out Escape = %q", got)
	}
}

func TestPastedNewlinesAndControlsStayInComposer(t *testing.T) {
	fake := newFakeSession()
	app := New(fake)
	app.handleEvent(tcell.NewEventPaste(true))
	app.handleKey(tcell.NewEventKey(tcell.KeyRune, "a", tcell.ModNone))
	app.handleKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
	app.handleKey(tcell.NewEventKey(tcell.KeyRune, "b", tcell.ModNone))
	app.handleKey(tcell.NewEventKey(tcell.KeyCtrlQ, "", tcell.ModNone))
	app.handleEvent(tcell.NewEventPaste(false))
	if got := app.composer.Text(); got != "a\nb" {
		t.Fatalf("composer after paste = %q, want %q", got, "a\nb")
	}
	if app.overlay != overlayNone {
		t.Fatal("a pasted control key opened an overlay")
	}
	app.handleKey(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
	commands := fake.snapshotCommands()
	if len(commands) != 1 {
		t.Fatalf("commands after submit = %d, want 1", len(commands))
	}
	submit, ok := commands[0].(session.SubmitTask)
	if !ok || submit.Text != "a\nb" {
		t.Fatalf("submitted command = %#v", commands[0])
	}
}
