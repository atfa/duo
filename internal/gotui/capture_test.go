package gotui

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/gdamore/tcell/v3"
	ui "github.com/metaspartan/gotui/v5"
)

// TestCaptureFrames renders deterministic frames from a real tcell simulation
// screen. It is skipped unless DUO_GOTUI_CAPTURE_DIR names an output directory;
// the committed captures under docs/gotui/screens were produced this way.
func TestCaptureFrames(t *testing.T) {
	dir := strings.TrimSpace(os.Getenv("DUO_GOTUI_CAPTURE_DIR"))
	if dir == "" {
		t.Skip("set DUO_GOTUI_CAPTURE_DIR to write simulation frames")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := newFakeSession()
	fake.history = []sessionstore.TUIEntry{
		{Time: time.Date(2026, 10, 20, 16, 2, 0, 0, time.Local), Pane: "Duo", Label: "Human → Austin", Text: "Add cursor pagination to GET /sessions and document the new parameters."},
		{Time: time.Date(2026, 10, 20, 16, 2, 12, 0, time.Local), Pane: "Austin", Label: "Austin → Human", Text: "## Result\n\nImplemented cursor pagination.\n\n- `limit` is capped at 200\n- opaque `cursor` values\n- OpenAPI updated\n\n```go\npage, err := store.Sessions(ctx, limit, cursor)\n```\n\nTests: `go test ./internal/sessionstore` — 你好世界"},
		{Time: time.Date(2026, 10, 20, 16, 3, 1, 0, time.Local), Pane: "Tony", Label: "Tony → Austin", Text: "Verified the boundary cases. One request with an expired cursor returns 400 rather than silently restarting."},
		{Time: time.Date(2026, 10, 20, 16, 3, 20, 0, time.Local), Pane: "Duo", Label: "Duo → Human", Text: "Verification passed. The change is ready to deliver."},
	}
	av := fake.view.Agents[protocol.Austin]
	av.Model = "openai/gpt-5"
	av.Thinking = "high"
	av.Runtime = harness.AgentRuntime{
		Busy: true, ProviderActive: true, ContextTokens: 48210, ContextWindow: 200000, Tools: 7,
		TurnStarted: time.Now().Add(-95 * time.Second), LastActivity: time.Now(),
		Tool: "exec", ToolDetail: "go test ./...", ToolDepth: 1,
	}
	fake.view.Agents[protocol.Austin] = av
	tv := fake.view.Agents[protocol.Tony]
	tv.Model = "anthropic/claude-sonnet"
	tv.Thinking = "medium"
	tv.Runtime = harness.AgentRuntime{Busy: false, LastActivity: time.Now().Add(-32 * time.Second), ContextTokens: 22000, ContextWindow: 200000}
	fake.view.Agents[protocol.Tony] = tv
	fake.view.Project.Verification.Status = project.VerificationPassed
	fake.view.Project.Verification.Head = "abc123456789"
	fake.view.Delivery = sessionstore.Delivery{Status: sessionstore.DeliveryPending}

	app := New(fake)
	backend, err := ui.NewBackend(&ui.InitConfig{SimulationMode: true, SimulationSize: image.Pt(120, 42)})
	if err != nil {
		t.Fatal(err)
	}
	sim, ok := backend.Screen.(tcell.SimulationScreen)
	if !ok {
		t.Fatalf("screen %T is not simulated", backend.Screen)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.runWithBackend(ctx, backend) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("app did not stop")
		}
	}()
	capture := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name+".txt"), []byte(trimmedScreen(sim)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	waitForText(t, sim, "DUO")
	capture("01-dashboard-120x42")

	injectKey(sim, tcell.KeyF2, 0, tcell.ModNone)
	waitForText(t, sim, "CONVERSATION")
	capture("02-timeline-120x42")

	typeText(sim, "/")
	waitForText(t, sim, "COMMANDS")
	capture("03-slash-120x42")
	injectKey(sim, tcell.KeyEsc, 0, tcell.ModNone)
	injectKey(sim, tcell.KeyCtrlU, 0, tcell.ModNone)

	injectKey(sim, tcell.KeyF1, 0, tcell.ModNone)
	waitForText(t, sim, "NATIVE TERMINALS")
	capture("04-help-120x42")
	injectKey(sim, tcell.KeyEsc, 0, tcell.ModNone)
	waitGone(t, sim, "NATIVE TERMINALS")

	injectKey(sim, tcell.KeyCtrlO, 0, tcell.ModNone)
	waitForText(t, sim, "STATUS OK")
	capture("05-details-120x42")
	injectKey(sim, tcell.KeyEsc, 0, tcell.ModNone)
	waitGone(t, sim, "SESSION DETAILS")

	injectKey(sim, tcell.KeyCtrlM, 0, tcell.ModNone)
	waitForText(t, sim, "MODELS & THINKING")
	waitForText(t, sim, "openai/gpt-5")
	capture("06-models-120x42")
	injectKey(sim, tcell.KeyEsc, 0, tcell.ModNone)
	waitGone(t, sim, "MODELS & THINKING")

	sim.SetSize(80, 24)
	waitGone(t, sim, "WORKFLOW")
	injectKey(sim, tcell.KeyCtrlQ, 0, tcell.ModNone)
	waitForText(t, sim, "Quit Duo?")
	capture("07-confirm-80x24")
	injectKey(sim, tcell.KeyRune, 'n', tcell.ModNone)
	waitGone(t, sim, "Quit Duo?")

	sim.SetSize(64, 22)
	waitForText(t, sim, "·  TIMELINE")
	waitForText(t, sim, "CONVERSATION")
	capture("08-narrow-64x22")
}

func trimmedScreen(sim tcell.SimulationScreen) string {
	// A redraw writes cells one at a time, so wait for two identical reads
	// before treating a frame as final.
	text := screenText(sim)
	for i := 0; i < 8; i++ {
		time.Sleep(10 * time.Millisecond)
		next := screenText(sim)
		if next == text {
			break
		}
		text = next
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return strings.Join(lines, "\n") + "\n"
}
