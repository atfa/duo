package tui

import (
	"context"
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
