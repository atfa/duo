package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/coordinator"
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/models"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

func pickerFixture() *App {
	return &App{
		view:        viewModel,
		modelTarget: protocol.Austin,
		models: []models.Model{
			{Provider: "cline", ID: "anthropic/claude-opus", Thinking: true},
			{Provider: "cline", ID: "deepseek/flash"},
			{Provider: "workbuddy", ID: "glm-5"},
		},
	}
}

func TestModelFilterNarrowsAndClampsCursor(t *testing.T) {
	a := pickerFixture()
	a.modelCursor = 2
	for _, r := range "cline" { // matches both cline models, not workbuddy/glm-5
		a.handleModelKey(string(r))
	}

	if got := len(a.filteredModels()); got != 2 {
		t.Fatalf("filtered %d models, want 2", got)
	}
	if a.modelCursor != 1 {
		t.Fatalf("cursor = %d, want clamped to 1", a.modelCursor)
	}
	a.handleModelKey("backspace")
	if got := len(a.filteredModels()); got != 2 {
		t.Fatalf("after one backspace filtered %d models, want 2", got)
	}
	for i := 0; i < len("cline"); i++ {
		a.handleModelKey("backspace")
	}
	if got := len(a.filteredModels()); got != 3 {
		t.Fatalf("after clearing the filter filtered %d models, want 3", got)
	}
}

func TestModelPickerTabSwitchesTarget(t *testing.T) {
	// Tab arrives as byte 0x09; it must be decoded or the picker can never reach Tony.
	if got := keyForByte(9); got != "tab" {
		t.Fatalf("keyForByte(9) = %q, want tab", got)
	}
	a := pickerFixture()
	a.handleKey("tab")
	if a.modelTarget != protocol.Tony {
		t.Fatalf("target = %q, want Tony", a.modelTarget)
	}
	a.handleKey("tab")
	if a.modelTarget != protocol.Austin {
		t.Fatalf("target = %q, want Austin", a.modelTarget)
	}
}

func TestShiftTabRequestsThinkingCycle(t *testing.T) {
	a := pickerFixture()
	if got := a.handleKey("shift-tab"); got.kind != actionCycleThinking {
		t.Fatalf("shift-tab kind = %v, want actionCycleThinking", got.kind)
	}
}

func TestSpaceAppliesWithoutClosingAndEnterCloses(t *testing.T) {
	a := pickerFixture()
	if got := a.handleKey(" "); got.kind != actionApplyModelKeepOpen {
		t.Fatalf("space kind = %v, want actionApplyModelKeepOpen", got.kind)
	}
	// Space is an action, not filter input: the catalog must stay untouched.
	if len(a.modelFilter) != 0 || len(a.filteredModels()) != 3 {
		t.Fatalf("space filtered the catalog: %q", a.modelFilter)
	}
	if got := a.handleKey("enter"); got.kind != actionApplyModel {
		t.Fatalf("enter kind = %v, want actionApplyModel", got.kind)
	}
}

func TestModelRowMarksCursorAndCurrentSeparately(t *testing.T) {
	a := pickerFixture()
	a.currentModel = map[protocol.AgentID]string{protocol.Austin: "cline/deepseek/flash"}
	if got := a.modelRow(a.models[1], true); !strings.HasPrefix(got, "▶●") {
		t.Errorf("cursor on current model = %q, want ▶●", got)
	}
	if got := a.modelRow(a.models[0], true); !strings.HasPrefix(got, "▶ ") {
		t.Errorf("cursor off current model = %q, want ▶", got)
	}
	if got := a.modelRow(a.models[1], false); !strings.HasPrefix(got, " ●") {
		t.Errorf("current model without cursor = %q, want ●", got)
	}
}

// opencode only accepts provider/model as --model and aborts on a bare id, so
// the launch flag must receive the provider-qualified reference. agy strips any
// provider prefix itself, and pi takes the model over the bridge.
func TestApplySelectedModelPassesQualifiedReferenceToDriver(t *testing.T) {
	a := pickerFixture()
	a.width, a.height = 80, 24
	bus := events.NewBus()
	state := project.NewStateFor(project.ModeFast)
	a.coord = coordinator.New(nil, state, harness.NewTracker(), nil, bus)
	eventsCh, unsub := bus.Subscribe(20)
	defer unsub()
	// The App is single-goroutine state: production routes events on the event
	// loop, so the test drains them the same way instead of racing the apply.
	drain := func() {
		for {
			select {
			case e := <-eventsCh:
				a.route(e)
			default:
				return
			}
		}
	}
	drv := &modelRecordingDriver{agentID: protocol.Austin, driverType: "opencode"}
	mgr := agent.NewManager()
	mgr.Add(drv)
	a.agents = mgr

	// The cursor is on cline/anthropic/claude-opus.
	a.applySelectedModel(context.Background(), false)
	drain()
	if drv.model != "cline/anthropic/claude-opus" {
		t.Fatalf("driver model = %q, want the provider-qualified reference", drv.model)
	}
	if drv.restarts != 1 {
		t.Fatalf("restarts = %d, want the CLI-only driver restarted once", drv.restarts)
	}
}

type modelRecordingDriver struct {
	agent.Driver
	agentID    protocol.AgentID
	driverType string
	model      string
	restarts   int
}

func (d *modelRecordingDriver) Agent() protocol.AgentID            { return d.agentID }
func (d *modelRecordingDriver) DriverType() string                 { return d.driverType }
func (d *modelRecordingDriver) State() agent.ProcessState          { return agent.ProcessExited }
func (d *modelRecordingDriver) Running() bool                      { return false }
func (d *modelRecordingDriver) SetModel(model string)              { d.model = model }
func (d *modelRecordingDriver) SetOnExit(fn func(agent.ExitEvent)) {}
func (d *modelRecordingDriver) RestartRunning(ctx context.Context) error {
	d.restarts++
	return nil
}

func TestCtrlMIsDistinctFromEnter(t *testing.T) {
	// The m key with Ctrl, in both encodings Duo enables, must not look like Enter.
	for _, seq := range []string{"\x1b[27;5;109~", "\x1b[109;5u"} {
		if got := decodeEscape(seq); got != "ctrl-m" {
			t.Errorf("decodeEscape(%q) = %q, want ctrl-m", seq, got)
		}
	}
	if got := decodeEscape("\x1b[Z"); got != "shift-tab" {
		t.Errorf("shift-tab decode = %q", got)
	}
	// Terminals that report Shift+Tab through modifyOtherKeys/CSI-u rather than CSI Z.
	for _, seq := range []string{"\x1b[9;2u", "\x1b[27;2;9~"} {
		if got := decodeEscape(seq); got != "shift-tab" {
			t.Errorf("decodeEscape(%q) = %q, want shift-tab", seq, got)
		}
	}
	if got := decodeEscape("\x1bm"); got != "alt-m" {
		t.Errorf("alt-m decode = %q", got)
	}
	if got := decodeEscape("\r"); got != "" {
		t.Errorf("bare CR decoded to %q, want empty (handled as Enter)", got)
	}
}

func TestRouteModelEventUpdatesPickerStateWithoutPaneNoise(t *testing.T) {
	a := &App{}
	a.route(events.Event{Kind: events.KindModel, Agent: protocol.Tony, Provider: "cline", Model: "x/y"})
	if a.currentModel[protocol.Tony] != "cline/x/y" {
		t.Fatalf("currentModel = %#v", a.currentModel)
	}
	a.route(events.Event{Kind: events.KindThinking, Agent: protocol.Tony, Thinking: "high"})
	if a.currentThinking[protocol.Tony] != "high" {
		t.Fatalf("currentThinking = %#v", a.currentThinking)
	}
	if len(a.duo)+len(a.austin)+len(a.tony) != 0 {
		t.Fatalf("model/thinking reports should not add pane entries")
	}
}

func TestWriteModelMarksCurrentAndShowsThinking(t *testing.T) {
	a := pickerFixture()
	a.width, a.height = 80, 24
	a.currentModel = map[protocol.AgentID]string{protocol.Austin: "cline/anthropic/claude-opus"}
	a.currentThinking = map[protocol.AgentID]string{protocol.Austin: "high"}

	var b strings.Builder
	a.writeModel(&b, a.width, a.height)
	out := b.String()
	for _, want := range []string{"Duo Models · Austin", "▶●cline/anthropic/claude-opus", "thinking high", "Tab agent"} {
		if !strings.Contains(out, want) {
			t.Errorf("picker frame missing %q:\n%s", want, out)
		}
	}
	// The frame must exactly fill the terminal: h rows, each w columns wide.
	lines := strings.Split(out, "\r\n")
	if len(lines) != a.height {
		t.Fatalf("picker frame has %d rows, want %d", len(lines), a.height)
	}
	for i, line := range lines {
		if got := displayWidth(ansiPattern.ReplaceAllString(line, "")); got != a.width {
			t.Errorf("picker row %d is %d columns wide, want %d: %q", i, got, a.width, line)
		}
	}
}

func TestWriteModelShowsLoadingDriverName(t *testing.T) {
	a := pickerFixture()
	a.width, a.height = 80, 24
	a.modelLoading = true

	var b strings.Builder
	a.writeModel(&b, a.width, a.height)
	if !strings.Contains(b.String(), "Loading models from pi…") {
		t.Errorf("expected default pi driver loading message, got:\n%s", b.String())
	}

	mgr := agent.NewManager()
	mgr.Add(agent.NewAgySession(agent.Config{Agent: protocol.Austin, DriverType: "agy"}))
	a.agents = mgr
	b.Reset()
	a.writeModel(&b, a.width, a.height)
	if !strings.Contains(b.String(), "Loading models from agy…") {
		t.Errorf("expected agy driver loading message, got:\n%s", b.String())
	}
}

func TestApplySelectedModelAndCycleThinkingOffline(t *testing.T) {
	ctx := context.Background()
	a := pickerFixture()
	a.width, a.height = 80, 24
	bus := events.NewBus()
	state := project.NewStateFor(project.ModeFast)
	a.coord = coordinator.New(nil, state, harness.NewTracker(), nil, bus)
	eventsCh, unsub := bus.Subscribe(20)
	defer unsub()
	// The App is single-goroutine state: production routes events on the event
	// loop, so the test drains them the same way instead of racing the apply.
	drain := func() {
		for {
			select {
			case e := <-eventsCh:
				a.route(e)
			default:
				return
			}
		}
	}

	// Cursor is on index 0: cline/anthropic/claude-opus
	// 1. Space applies and keeps picker open
	a.applySelectedModel(ctx, true)
	drain()
	if a.view != viewModel {
		t.Fatalf("picker closed on space, view = %v", a.view)
	}
	if a.currentModel[protocol.Austin] != "cline/anthropic/claude-opus" {
		t.Fatalf("currentModel = %q, want cline/anthropic/claude-opus", a.currentModel[protocol.Austin])
	}

	// Frame displays status
	var b strings.Builder
	a.writeModel(&b, a.width, a.height)
	if !strings.Contains(b.String(), "Austin model → cline/anthropic/claude-opus") {
		t.Errorf("picker frame missing status on space:\n%s", b.String())
	}

	// 2. Shift+Tab cycles thinking
	a.applyAction(ctx, inputAction{kind: actionCycleThinking})
	drain()
	if a.currentThinking[protocol.Austin] != "low" {
		t.Fatalf("currentThinking = %q, want low", a.currentThinking[protocol.Austin])
	}
	b.Reset()
	a.writeModel(&b, a.width, a.height)
	if !strings.Contains(b.String(), "thinking low") {
		t.Errorf("picker frame missing thinking low:\n%s", b.String())
	}

	// 3. Enter applies and closes
	a.moveModelCursor(1) // cline/deepseek/flash
	a.applySelectedModel(ctx, false)
	drain()
	if a.view != viewMain {
		t.Fatalf("picker did not close on enter, view = %v", a.view)
	}
	if a.currentModel[protocol.Austin] != "cline/deepseek/flash" {
		t.Fatalf("currentModel = %q, want cline/deepseek/flash", a.currentModel[protocol.Austin])
	}
}
