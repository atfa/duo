package gotui

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/gdamore/tcell/v3"
	ui "github.com/metaspartan/gotui/v5"
)

// shotFrames are the frames also exported as colour SVG screenshots for the
// product site. The set is deliberately small: three states tell the story.
var shotFrames = map[string]bool{
	"01-dashboard-120x42": true,
	"02-timeline-120x42":  true,
	"08-narrow-64x22":     true,
}

// TestCaptureFrames renders deterministic frames from a real tcell simulation
// screen. DUO_GOTUI_CAPTURE_DIR writes the plain text captures committed under
// docs/gotui/screens; DUO_GOTUI_SHOT_DIR writes colour SVG screenshots of
// shotFrames, which the site embeds. Both come from the same frame.
func TestCaptureFrames(t *testing.T) {
	dir := strings.TrimSpace(os.Getenv("DUO_GOTUI_CAPTURE_DIR"))
	shotDir := strings.TrimSpace(os.Getenv("DUO_GOTUI_SHOT_DIR"))
	if dir == "" && shotDir == "" {
		t.Skip("set DUO_GOTUI_CAPTURE_DIR and/or DUO_GOTUI_SHOT_DIR to write simulation frames")
	}
	for _, out := range []string{dir, shotDir} {
		if out == "" {
			continue
		}
		if err := os.MkdirAll(out, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fake := newFakeSession()
	fake.history = []sessionstore.TUIEntry{
		{Time: time.Date(2026, 10, 20, 16, 2, 0, 0, time.Local), Pane: "Duo", Label: "Human → Austin", Text: "Add cursor pagination to GET /sessions and document the new parameters."},
		{Time: time.Date(2026, 10, 20, 16, 2, 12, 0, time.Local), Pane: "Austin", Label: "Austin → Human", Text: "## Result\n\nImplemented cursor pagination.\n\n- `limit` is capped at 200\n- opaque `cursor` values\n- OpenAPI updated\n\n```go\npage, err := store.Sessions(ctx, limit, cursor)\n```\n\nTests: `go test ./internal/sessionstore` — all green"},
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
	// These frames double as the product site's screenshots, so show a release
	// build and a plausible session instead of the fixture's "test" strings.
	fake.view.Version = "v0.14.3"
	fake.view.SessionID = "20261022-211328"
	fake.view.Worktrees.Repository = "/home/ana/Projects/atlas-api"
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		av := fake.view.Agents[id]
		av.Driver = "pi"
		if av.Manifest != nil {
			manifest := *av.Manifest
			manifest.Name = "pi"
			av.Manifest = &manifest
		}
		fake.view.Agents[id] = av
	}

	app := New(fake)
	app.viewMode = viewSplit
	app.saveViewPref = nil
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
	th := newTheme()
	capture := func(name string) {
		t.Helper()
		text := trimmedScreen(sim)
		if dir != "" {
			if err := os.WriteFile(filepath.Join(dir, name+".txt"), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if shotDir != "" && shotFrames[name] {
			if err := os.WriteFile(filepath.Join(shotDir, name+".svg"), []byte(screenSVG(sim, th)), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	waitForText(t, sim, "DUO")
	capture("01-dashboard-120x42")

	// Persistence is covered by the App tests. This test only captures the
	// visual transition and must not write to the fixed frame repository.
	injectKey(sim, tcell.KeyF2, 0, tcell.ModNone)
	waitForText(t, sim, "CONVERSATION")
	capture("02-timeline-120x42")

	typeText(sim, "/")
	waitForText(t, sim, "COMMANDS")
	capture("03-slash-120x42")
	injectKey(sim, tcell.KeyEsc, 0, tcell.ModNone)
	injectKey(sim, tcell.KeyCtrlU, 0, tcell.ModNone)

	// The guide scrolls, so only its top is guaranteed to be painted: wait for
	// the second section title instead of a lower one that may be off-screen.
	injectKey(sim, tcell.KeyF1, 0, tcell.ModNone)
	waitForText(t, sim, " KEYS ")
	capture("04-help-120x42")
	injectKey(sim, tcell.KeyEsc, 0, tcell.ModNone)
	waitGone(t, sim, " KEYS ")

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

	// A paused session shows the badge and progress, and the driver picker is its
	// only process-changing control. Both agents are idle by definition: a busy
	// card in a PAUSED frame would contradict the state the badge reports.
	fake.mu.Lock()
	savedAustin, savedTony := fake.view.Agents[protocol.Austin], fake.view.Agents[protocol.Tony]
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		av := fake.view.Agents[id]
		av.Runtime = harness.AgentRuntime{LastActivity: time.Now()}
		fake.view.Agents[id] = av
	}
	fake.view.Pause = session.PauseView{State: session.PausePaused, Paused: map[protocol.AgentID]bool{protocol.Austin: true, protocol.Tony: true}}
	fake.mu.Unlock()
	fake.events <- events.Event{Time: time.Now(), Kind: events.KindActivity, Agent: protocol.Austin}
	waitForText(t, sim, "PAUSED")
	capture("09-paused-120x42")

	typeText(sim, "/driver")
	injectKey(sim, tcell.KeyEnter, 0, tcell.ModNone)
	waitForText(t, sim, "DRIVERS")
	capture("10-drivers-120x42")
	injectKey(sim, tcell.KeyEsc, 0, tcell.ModNone)
	waitGone(t, sim, "DRIVERS")

	fake.mu.Lock()
	fake.view.Pause = session.PauseView{State: session.PauseRunning, Paused: map[protocol.AgentID]bool{}}
	fake.view.Agents[protocol.Austin], fake.view.Agents[protocol.Tony] = savedAustin, savedTony
	fake.mu.Unlock()
	fake.events <- events.Event{Time: time.Now(), Kind: events.KindActivity, Agent: protocol.Tony}
	waitGone(t, sim, "PAUSED")

	sim.SetSize(80, 24)
	waitGone(t, sim, "WORKFLOW")
	injectKey(sim, tcell.KeyCtrlQ, 0, tcell.ModNone)
	waitForText(t, sim, "Quit Duo?")
	capture("07-confirm-80x24")
	injectKey(sim, tcell.KeyRune, 'n', tcell.ModNone)
	waitGone(t, sim, "Quit Duo?")

	sim.SetSize(64, 22)
	waitForText(t, sim, "Timeline")
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
