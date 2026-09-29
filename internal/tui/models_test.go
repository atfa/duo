package tui

import (
	"strings"
	"testing"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/models"
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
