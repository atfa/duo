package tui

import (
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
	if row != 28 || col != 5 {
		t.Fatalf("empty cursor = %d,%d", row, col)
	}
	_, col = inputCursor(100, 30, "中文")
	if col != 9 {
		t.Fatalf("Chinese cursor col = %d", col)
	}
	_, col = inputCursor(60, 18, strings.Repeat("x", 100))
	if col != 59 {
		t.Fatalf("long cursor col = %d", col)
	}
}
