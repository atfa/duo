package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/protocol"
)

func TestNativeDetach(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		detach  bool
		forward int
	}{
		{"legacy Ctrl+]", "\x1d", true, 0},
		{"legacy Ctrl+\\", "\x1c", true, 0},
		{"CSI-u Ctrl+]", "\x1b[93;5u", true, len("\x1b[93;5u")},
		{"CSI-u Ctrl+】", "\x1b[12305;5u", true, len("\x1b[12305;5u")},
		{"CSI-u event variant", "\x1b[93;5:1u", true, len("\x1b[93;5:1u")},
		{"CSI-u Ctrl with Shift", "\x1b[92;6u", true, len("\x1b[92;6u")},
		{"modifyOtherKeys Ctrl+]", "\x1b[27;5;93~", true, len("\x1b[27;5;93~")},
		{"modifyOtherKeys Ctrl+】", "\x1b[27;5;12305~", true, len("\x1b[27;5;12305~")},
		{"modifyOtherKeys Ctrl with Alt", "\x1b[27;7;92~", true, len("\x1b[27;7;92~")},
		{"similar CSI-u without Ctrl", "\x1b[93;4u", false, len("\x1b[93;4u")},
		{"similar modifyOtherKeys without Ctrl", "\x1b[27;4;93~", false, len("\x1b[27;4;93~")},
		{"ordinary input", "hello", false, len("hello")},
		{"native Ctrl+/ passes to Pi", "\x1f", false, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &App{}
			var got bool
			forwarded := 0
			for _, b := range []byte(tt.input) {
				detach, forward := a.nativeDetach(b)
				got = got || detach
				if forward {
					forwarded++
				}
			}
			if got != tt.detach || forwarded != tt.forward {
				t.Fatalf("detach=%v, forwarded=%d; want detach=%v, forwarded=%d", got, forwarded, tt.detach, tt.forward)
			}
		})
	}
}

func TestHelpIgnoresMouseAttach(t *testing.T) {
	a := testApp(80, 24)
	a.view = viewHelp
	var action inputAction
	for _, b := range []byte("\x1b[<0;2;1M") {
		action = a.handleByte(b)
	}
	if action.kind != actionNone || action.agent != "" {
		t.Fatalf("Help mouse action = %+v", action)
	}
}

func TestPeerRouteShowsFullTextOnlyToReceiver(t *testing.T) {
	text := "  第一行\n\n第二行 " + strings.Repeat("界", 100)
	a := &App{}
	a.route(events.Event{Kind: events.KindPeer, Agent: protocol.Austin, Peer: protocol.Tony, Text: text})
	if got := a.austin[0].text; got != "→ Tony: sent" {
		t.Fatalf("Austin message = %q", got)
	}
	if got := a.tony[0].text; got != "← Austin: "+strings.TrimSpace(text) {
		t.Fatalf("Tony message = %q", got)
	}
}

func TestErrorRouteMarksFullAgentDetail(t *testing.T) {
	a := &App{}
	a.route(events.Event{Kind: events.KindError, Agent: protocol.Austin, Text: "429 Too Many Requests"})
	if got := a.austin[0].text; got != "ERROR: 429 Too Many Requests" {
		t.Fatalf("Austin error = %q", got)
	}
	if !strings.Contains(a.duo[0].text, "429 Too Many Requests") {
		t.Fatalf("Duo error = %q", a.duo[0].text)
	}
	if !a.austin[0].error || !a.duo[0].error {
		t.Fatal("expected errors to be marked for red rendering")
	}
}

func TestAgentStatePriorityAndAnimation(t *testing.T) {
	if got := agentState(false, harness.AgentRuntime{Busy: true, ToolDepth: 1}, 0); got != "connecting" {
		t.Fatalf("disconnected state = %q", got)
	}
	runtime := harness.AgentRuntime{Busy: true, ProviderActive: true, ToolDepth: 1}
	if got := agentState(true, runtime, 0); got != "tool |" {
		t.Fatalf("tool state = %q", got)
	}
	runtime.ToolDepth = 0
	if got := agentState(true, runtime, 0); got != "thinking |" {
		t.Fatalf("thinking state = %q", got)
	}
	runtime.ProviderActive = false
	if got := agentState(true, runtime, 0); got != "working |" {
		t.Fatalf("working state = %q", got)
	}
	if agentState(true, runtime, 0) == agentState(true, runtime, 1) {
		t.Fatal("spinner did not advance")
	}
}

func TestInputCursor(t *testing.T) {
	row, col := inputCursor(100, 30, "")
	if row != 28 || col != 18 {
		t.Fatalf("empty cursor = %d,%d", row, col)
	}
	_, col = inputCursor(100, 30, "中文")
	if col != 22 {
		t.Fatalf("Chinese cursor col = %d", col)
	}
	_, col = inputCursor(60, 18, strings.Repeat("x", 100))
	if col != 60 {
		t.Fatalf("long cursor col = %d", col)
	}
}

func TestHelpInputAndComposerPreservation(t *testing.T) {
	a := testApp(80, 24)
	a.input = []byte("hello")
	if action := a.handleByte(0x1f); action.kind != actionToggleHelp {
		t.Fatalf("Ctrl+/ action = %v", action.kind)
	} else {
		a.applyAction(context.Background(), action)
	}
	if a.view != viewHelp {
		t.Fatal("Ctrl+/ did not open Help")
	}
	if !a.renderer.full {
		t.Fatal("opening Help did not request a full clear")
	}
	for _, b := range []byte{'j', 'k', 'x', '\r', 127, 8} {
		a.applyAction(context.Background(), a.handleByte(b))
	}
	if got := string(a.input); got != "hello" {
		t.Fatalf("Help modified composer: %q", got)
	}
	if action := a.handleByte(0x1f); action.kind != actionToggleHelp {
		t.Fatalf("Help Ctrl+/ action = %v", action.kind)
	} else {
		a.renderer.full = false
		a.applyAction(context.Background(), action)
	}
	if a.view != viewMain || string(a.input) != "hello" {
		t.Fatalf("Help close = view %v input %q", a.view, a.input)
	}
	if !a.renderer.full {
		t.Fatal("closing Help did not request a full clear")
	}
}

func TestBackspaceAndEnhancedHelpShortcut(t *testing.T) {
	for _, b := range []byte{127, 8} {
		a := testApp(80, 24)
		a.input = []byte("中文x")
		a.inputPos = len(a.input)
		a.handleByte(b)
		if got := string(a.input); got != "中文" {
			t.Fatalf("backspace %d = %q", b, got)
		}
	}
	for _, sequence := range []string{"\x1b[47;5u", "\x1b[27;5;47~"} {
		a := testApp(80, 24)
		var action inputAction
		for _, b := range []byte(sequence) {
			action = a.handleByte(b)
		}
		if action.kind != actionToggleHelp {
			t.Fatalf("%q action = %v", sequence, action.kind)
		}
	}
}

func TestComposerCursorEditingAndNewlines(t *testing.T) {
	a := testApp(80, 24)
	for _, b := range []byte("ac") {
		a.handleByte(b)
	}
	for _, b := range []byte("\x1b[D") {
		a.handleByte(b)
	}
	a.handleByte('b')
	if got := string(a.input); got != "abc" || a.inputPos != 2 {
		t.Fatalf("insert = %q at %d", got, a.inputPos)
	}
	a.handleByte(127)
	if got := string(a.input); got != "ac" || a.inputPos != 1 {
		t.Fatalf("backspace = %q at %d", got, a.inputPos)
	}
	a.input, a.inputPos = []byte("中x"), len([]byte("中x"))
	for _, b := range []byte("\x1b[D") {
		a.handleByte(b)
	}
	a.handleByte(8)
	if got := string(a.input); got != "x" || a.inputPos != 0 {
		t.Fatalf("unicode backspace = %q at %d", got, a.inputPos)
	}
	a.input, a.inputPos = []byte("中x"), len([]byte("中"))
	for _, b := range []byte("文") {
		a.handleByte(b)
	}
	if got := string(a.input); got != "中文x" || a.inputPos != len([]byte("中文")) {
		t.Fatalf("unicode insert = %q at %d", got, a.inputPos)
	}
	for _, sequence := range []string{"\n", "\x1b[13;2u", "\x1b[27;2;13~", "\x1b[13;5u", "\x1b[27;5;13~"} {
		a := testApp(80, 24)
		for _, b := range []byte(sequence) {
			a.handleByte(b)
		}
		if got := string(a.input); got != "\n" {
			t.Fatalf("%q newline = %q", sequence, got)
		}
	}
	if action := a.handleByte('\r'); action.kind != actionSubmit {
		t.Fatalf("CR action = %v", action.kind)
	}
}

func TestEnhancedGlobalShortcuts(t *testing.T) {
	for _, tt := range []struct {
		want      string
		sequences []string
	}{
		{"ctrl-a", []string{"\x1b[97;5u", "\x1b[27;5;97~"}},
		{"ctrl-t", []string{"\x1b[116;5u", "\x1b[27;5;116~"}},
		{"ctrl-r", []string{"\x1b[114;5u", "\x1b[27;5;114~"}},
		{"ctrl-y", []string{"\x1b[121;5u", "\x1b[27;5;121~"}},
		{"ctrl-q", []string{"\x1b[113;5u", "\x1b[27;5;113~"}},
		{"ctrl-slash", []string{"\x1b[47;5u", "\x1b[27;5;47~"}},
	} {
		for _, sequence := range tt.sequences {
			a := testApp(80, 24)
			var action inputAction
			for _, b := range []byte(sequence) {
				action = a.handleByte(b)
			}
			if got := actionKey(action); got != tt.want {
				t.Fatalf("%q = %q, want %q", sequence, got, tt.want)
			}
		}
	}
}

func actionKey(action inputAction) string {
	switch action.kind {
	case actionAttach:
		return "ctrl-" + strings.ToLower(string(action.agent[0]))
	case actionRestart:
		if action.agent == protocol.Austin {
			return "ctrl-r"
		}
		return "ctrl-y"
	case actionQuit:
		return "ctrl-q"
	case actionToggleHelp:
		return "ctrl-slash"
	}
	return ""
}

func TestComposerSubmitTextPreservesInternalNewlines(t *testing.T) {
	input := []byte(" \nfirst\nsecond\n ")
	if got := composerText(input); got != "first\nsecond" {
		t.Fatalf("submit text = %q", got)
	}
	a := testApp(80, 24)
	a.input, a.inputPos = input, len(input)
	a.clearInput()
	if len(a.input) != 0 || a.inputPos != 0 {
		t.Fatalf("cleared input = %q at %d", a.input, a.inputPos)
	}
}

func TestMousePaneScrolling(t *testing.T) {
	a := testApp(80, 24)
	for i := 0; i < 30; i++ {
		a.add(protocol.Austin, fmt.Sprintf("line %d", i))
	}
	var action inputAction
	for _, b := range []byte("\x1b[<64;2;2M") {
		action = a.handleByte(b)
	}
	if action.kind != actionScrollPane || action.agent != protocol.Austin || action.delta != 1 {
		t.Fatalf("wheel up = %+v", action)
	}
	a.applyAction(context.Background(), action)
	if a.austinOffset != 1 {
		t.Fatalf("Austin offset = %d", a.austinOffset)
	}
	left, _, rows := a.paneRows()
	latest := paneLinesAt(a.austin, left, rows, 0)
	older := paneLinesAt(a.austin, left, rows, a.austinOffset)
	if strings.Join(entryTexts(latest), "\n") == strings.Join(entryTexts(older), "\n") {
		t.Fatal("wheel up did not render earlier pane content")
	}
	for _, b := range []byte("\x1b[<65;2;2M") {
		action = a.handleByte(b)
	}
	a.applyAction(context.Background(), action)
	if a.austinOffset != 0 {
		t.Fatalf("wheel down = %d", a.austinOffset)
	}
	if got := strings.Join(entryTexts(paneLinesAt(a.austin, left, rows, a.austinOffset)), "\n"); got != strings.Join(entryTexts(latest), "\n") {
		t.Fatal("wheel down did not return to latest pane content")
	}
	for _, b := range []byte("\x1b[<64;60;2M") {
		action = a.handleByte(b)
	}
	if action.agent != protocol.Tony {
		t.Fatalf("Tony wheel = %+v", action)
	}
	a.austinOffset = 100
	a.width, a.height = 60, 18
	a.clampOffsets()
	left, _, rows = a.paneRows()
	if a.austinOffset < 0 || a.austinOffset > a.maxPaneOffset(protocol.Austin, left, rows) {
		t.Fatalf("resize offset = %d", a.austinOffset)
	}
	for _, b := range []byte("\x1b[<64;2;20M") {
		action = a.handleByte(b)
	}
	if action.kind != actionNone {
		t.Fatalf("bottom mouse action = %+v", action)
	}
	a.view = viewHelp
	offset := a.austinOffset
	for _, b := range []byte("\x1b[<64;2;2M") {
		action = a.handleByte(b)
	}
	if action.kind != actionNone || a.austinOffset != offset {
		t.Fatalf("Help wheel = %+v / %d", action, a.austinOffset)
	}
}

func entryTexts(entries []entry) []string {
	text := make([]string, len(entries))
	for i, entry := range entries {
		text[i] = entry.text
	}
	return text
}

func TestHelpScrollBoundariesAndEscape(t *testing.T) {
	a := testApp(60, 18)
	a.view = viewHelp
	a.applyAction(context.Background(), a.handleKey("up"))
	if a.helpOffset != 0 {
		t.Fatalf("up at top = %d", a.helpOffset)
	}
	a.applyAction(context.Background(), a.handleKey("end"))
	if a.helpOffset != a.maxHelpOffset() || a.helpOffset == 0 {
		t.Fatalf("end = %d, max = %d", a.helpOffset, a.maxHelpOffset())
	}
	a.applyAction(context.Background(), a.handleKey("down"))
	if a.helpOffset != a.maxHelpOffset() {
		t.Fatal("down beyond bottom moved offset")
	}
	a.applyAction(context.Background(), a.handleKey("page-up"))
	if want := maxInt(a.maxHelpOffset()-a.helpVisibleRows(), 0); a.helpOffset != want {
		t.Fatalf("page up = %d, want %d", a.helpOffset, want)
	}
	a.applyAction(context.Background(), a.handleKey("page-up"))
	for a.helpOffset > 0 {
		a.applyAction(context.Background(), a.handleKey("page-up"))
	}
	if a.helpOffset != 0 {
		t.Fatalf("page up did not clamp at top: %d", a.helpOffset)
	}
	a.applyAction(context.Background(), a.handleKey("page-down"))
	if a.helpOffset != a.helpVisibleRows() {
		t.Fatalf("page down = %d", a.helpOffset)
	}
	a.applyAction(context.Background(), a.handleByte(0x1b))
	a.applyAction(context.Background(), a.handleEscapeTimeout())
	if a.view != viewMain {
		t.Fatal("Escape did not close Help")
	}
}

func TestHelpEscapeSequences(t *testing.T) {
	tests := []struct {
		sequence string
		want     actionKind
	}{
		{"\x1b[A", actionScrollUp},
		{"\x1b[B", actionScrollDown},
		{"\x1b[H", actionHome},
		{"\x1b[1~", actionHome},
		{"\x1b[F", actionEnd},
		{"\x1b[4~", actionEnd},
		{"\x1b[5~", actionPageUp},
		{"\x1b[6~", actionPageDown},
	}
	for _, tt := range tests {
		t.Run(strings.ReplaceAll(tt.sequence, "\x1b", "ESC"), func(t *testing.T) {
			a := testApp(80, 24)
			a.view = viewHelp
			var action inputAction
			for _, b := range []byte(tt.sequence) {
				action = a.handleByte(b)
			}
			if action.kind != tt.want {
				t.Fatalf("%q action = %v, want %v", tt.sequence, action.kind, tt.want)
			}
		})
	}
}
