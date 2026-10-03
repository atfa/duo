package coordinator

import (
	"context"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

func coordWith(t *testing.T, d *mockDriver) *Coordinator {
	t.Helper()
	coord := New(nil, project.NewStateFor(project.ModeFast), harness.NewTracker(), nil, events.NewBus())
	manager := agent.NewManager()
	manager.Add(d)
	coord.SetAgents(manager)
	return coord
}

func prompt() protocol.Message {
	return protocol.Message{Type: protocol.MsgHumanPrompt, Text: "do the thing"}
}

// TestPTYFallbackIsGatedOnTheCapability is the silent drop this exists to stop.
// Writing a prompt to a terminal that cannot act on it looks delivered and is not:
// the message is gone and nothing says so. A driver that declares no delivery path
// must produce a visible error instead.
func TestPTYFallbackIsGatedOnTheCapability(t *testing.T) {
	d := newMockDriver(protocol.Austin, "strict", driver.Capabilities{
		Bridge:       driver.BridgeNone,
		PTYFallback:  false,
		LiveSteering: false,
	})
	d.running = true
	coord := coordWith(t, d)

	err := coord.sendToAgent(context.Background(), protocol.Austin, prompt())
	if err == nil {
		t.Fatal("a driver with no delivery path took the prompt without complaining")
	}
	if !strings.Contains(err.Error(), "no delivery path") {
		t.Fatalf("error should say the prompt cannot be delivered: %v", err)
	}
	if len(d.written) != 0 {
		t.Fatalf("the prompt was written to a terminal anyway: %q", d.written)
	}
}

// TestPTYFallbackStillDeliversWhenDeclared is the other half: gating must not cost
// the drivers that do accept a terminal their fallback. agy is the real case —
// liveSteering is false, so the terminal is its only path.
func TestPTYFallbackStillDeliversWhenDeclared(t *testing.T) {
	d := newMockDriver(protocol.Austin, "agy-shaped", driver.Capabilities{
		Bridge:       driver.BridgePlugin,
		PTYFallback:  true,
		LiveSteering: false,
	})
	d.running = true
	coord := coordWith(t, d)

	if err := coord.sendToAgent(context.Background(), protocol.Austin, prompt()); err != nil {
		t.Fatalf("a driver that declares ptyFallback was refused: %v", err)
	}
	if len(d.written) == 0 || !strings.Contains(d.written[0], "do the thing") {
		t.Fatalf("the prompt did not reach the terminal: %q", d.written)
	}
}
