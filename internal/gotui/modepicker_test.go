package gotui

import (
	"context"
	"image"
	"testing"
	"time"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/session"
	"github.com/gdamore/tcell/v3"
	ui "github.com/metaspartan/gotui/v5"
)

func TestModePickerStartsAtCurrentAndWraps(t *testing.T) {
	p := newModePicker(newTheme())
	p.begin(project.ModeGoal)
	if got := p.selected(); got != project.ModeGoal {
		t.Fatalf("initial selection = %s, want GOAL", got)
	}
	p.move(1)
	if got := p.selected(); got != project.ModeStep {
		t.Fatalf("after one move = %s, want STEP", got)
	}
	p.move(1)
	if got := p.selected(); got != project.ModeFast {
		t.Fatalf("wrapped selection = %s, want FAST", got)
	}
	p.move(-1)
	if got := p.selected(); got != project.ModeStep {
		t.Fatalf("wrapped back = %s, want STEP", got)
	}
	p.begin(project.ModeFast)
	if got := p.selected(); got != project.ModeFast {
		t.Fatalf("begin(FAST) selection = %s, want FAST", got)
	}
}

// The panel is reachable from the keyboard, the slash command and the header
// badge, and every apply path still goes through switchModeFromComposer.
func TestModePickerEntryPointsAndApply(t *testing.T) {
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
	purpose := "Austin implements; Tony verifies before Duo lands"

	// Ctrl+S opens the panel; Enter on the current mode closes it without a
	// command, because nothing changes.
	injectKey(sim, tcell.KeyCtrlS, 0, tcell.ModNone)
	waitForText(t, sim, purpose)
	waitForText(t, sim, "Both agents plan, implement and cross-review")
	injectKey(sim, tcell.KeyEnter, 0, tcell.ModNone)
	waitGone(t, sim, purpose)
	waitForText(t, sim, "already in FAST mode")
	if got := len(fake.snapshotCommands()); got != 0 {
		t.Fatalf("Enter on the current mode issued %d command(s)", got)
	}

	// Esc cancels without a command.
	injectKey(sim, tcell.KeyCtrlS, 0, tcell.ModNone)
	waitForText(t, sim, purpose)
	injectKey(sim, tcell.KeyEsc, 0, tcell.ModNone)
	waitGone(t, sim, purpose)
	if got := len(fake.snapshotCommands()); got != 0 {
		t.Fatalf("Esc issued %d command(s)", got)
	}

	// Alt+S opens, the cursor starts on the current mode, and Down + Enter
	// applies GOAL quietly before the resume prompt.
	injectKey(sim, tcell.KeyRune, 's', tcell.ModAlt)
	waitForText(t, sim, purpose)
	injectKey(sim, tcell.KeyDown, 0, tcell.ModNone)
	injectKey(sim, tcell.KeyEnter, 0, tcell.ModNone)
	waitForText(t, sim, "Mode switched to GOAL")
	waitForCommands(t, fake, func(commands []session.Command) bool {
		for _, command := range commands {
			if set, ok := command.(session.SwitchMode); ok && set.Mode == "goal" && set.Quiet {
				return true
			}
		}
		return false
	})
	injectKey(sim, tcell.KeyEsc, 0, tcell.ModNone)
	waitGone(t, sim, "Mode switched to GOAL")

	// `/mode` without an argument opens the same panel instead of only printing
	// the current mode.
	typeText(sim, "/mode")
	injectKey(sim, tcell.KeyEnter, 0, tcell.ModNone)
	waitForText(t, sim, purpose)
	injectKey(sim, tcell.KeyEsc, 0, tcell.ModNone)
	waitGone(t, sim, purpose)

	// The header badge is the mouse entry point.
	sim.InjectMouse(108, 0, tcell.Button1, tcell.ModNone)
	waitForText(t, sim, purpose)
	injectKey(sim, tcell.KeyEsc, 0, tcell.ModNone)
	waitGone(t, sim, purpose)
}

// TestModePickerStringsHaveChineseTranslations pins the panel's own chrome: a
// missing entry would leave English text in the zh interface.
func TestModePickerStringsHaveChineseTranslations(t *testing.T) {
	for _, mode := range pickerModes {
		if zhUI[modePurpose(mode)] == "" {
			t.Errorf("no zh translation for mode purpose %q", modePurpose(mode))
		}
	}
	for _, text := range []string{"MODE", "current", "↑/↓ choose · Enter switch · Esc cancel"} {
		if zhUI[text] == "" {
			t.Errorf("no zh translation for mode picker text %q", text)
		}
	}
}
