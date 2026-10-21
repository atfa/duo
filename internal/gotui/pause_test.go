package gotui

import (
	"context"
	"image"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
	"github.com/gdamore/tcell/v3"
	ui "github.com/metaspartan/gotui/v5"
)

func pausedView(state session.PauseState, austin, tony bool) session.PauseView {
	return session.PauseView{
		State:  state,
		Paused: map[protocol.AgentID]bool{protocol.Austin: austin, protocol.Tony: tony},
	}
}

func TestPauseSlashCommandsReachTheSession(t *testing.T) {
	fake := newFakeSession()
	app := New(fake)
	app.refreshView()

	for _, line := range []string{"/pause", "/unpause", "/continue"} {
		if !app.runSlash(line) {
			t.Fatalf("%s was not handled", line)
		}
	}
	commands := fake.snapshotCommands()
	if len(commands) != 3 {
		t.Fatalf("commands = %#v, want pause, unpause and continue", commands)
	}
	if _, ok := commands[0].(session.Pause); !ok {
		t.Fatalf("first command = %T, want session.Pause", commands[0])
	}
	if _, ok := commands[1].(session.Unpause); !ok {
		t.Fatalf("second command = %T, want session.Unpause", commands[1])
	}
	if _, ok := commands[2].(session.Continue); !ok {
		t.Fatalf("third command = %T, want session.Continue", commands[2])
	}
}

func TestPausedInputKeepsTheDraftAndSendIsRefused(t *testing.T) {
	for _, state := range []session.PauseState{session.PausePausing, session.PausePaused} {
		fake := newFakeSession()
		fake.view.Pause = pausedView(state, false, false)
		app := New(fake)
		app.composer.SetText("keep working")

		app.submit()

		if got := app.composer.Text(); got != "keep working" {
			t.Fatalf("%s: composer = %q, want the unsent draft preserved", state, got)
		}
		if commands := fake.snapshotCommands(); len(commands) != 0 {
			t.Fatalf("%s: a paused session submitted %#v", state, commands)
		}
		if !app.statusErr || app.status == "" {
			t.Fatalf("%s: blocked send did not explain itself: %q", state, app.status)
		}
	}

	// An absolute path is a task, not a slash command, so it stays blocked too.
	pathFake := newFakeSession()
	pathFake.view.Pause = pausedView(session.PausePaused, true, true)
	pathApp := New(pathFake)
	pathApp.composer.SetText("/usr/bin/python script.py")
	pathApp.submit()
	if commands := pathFake.snapshotCommands(); len(commands) != 0 {
		t.Fatalf("a paused session submitted an absolute path: %#v", commands)
	}
	if got := pathApp.composer.Text(); got != "/usr/bin/python script.py" {
		t.Fatalf("composer = %q, want the blocked path preserved", got)
	}

	// A slash verb is still allowed while paused: it is the way out.
	fake := newFakeSession()
	fake.view.Pause = pausedView(session.PausePaused, true, true)
	app := New(fake)
	app.composer.SetText("/continue")
	app.submit()
	commands := fake.snapshotCommands()
	if len(commands) != 1 {
		t.Fatalf("commands = %#v, want only the continue verb", commands)
	}
	if _, ok := commands[0].(session.Continue); !ok {
		t.Fatalf("command = %T, want session.Continue", commands[0])
	}
	if app.composer.Text() != "" {
		t.Fatal("an accepted slash command left its text in the composer")
	}
}

func TestPauseFooterNamesWhoIsStillWorking(t *testing.T) {
	fake := newFakeSession()
	fake.view.Pause = pausedView(session.PausePausing, false, true)
	app := New(fake)
	app.refreshView()

	hint := app.footerHint()
	for _, want := range []string{"Austin", "working", "Tony", "paused", "/unpause cancels"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("footer %q is missing %q", hint, want)
		}
	}

	fake.mu.Lock()
	fake.view.Pause = pausedView(session.PausePaused, true, true)
	fake.mu.Unlock()
	app.refreshView()
	if hint := app.footerHint(); !strings.Contains(hint, "/continue resume") {
		t.Fatalf("paused footer = %q, want the /continue hint", hint)
	}
}

func TestDriverPickerRequiresAFullPause(t *testing.T) {
	fake := newFakeSession()
	app := New(fake)
	app.refreshView()

	app.runSlash("/driver")
	if app.overlay != overlayNone {
		t.Fatal("driver picker opened while the session was running")
	}
	if !strings.Contains(app.status, "fully paused") {
		t.Fatalf("status = %q, want the driver-change restrictions", app.status)
	}

	// A fresh Duo session can select a driver before its first task.
	fake.view.Project.Started = false
	fake.view.Project.Phase = project.PhasePlan
	app.refreshView()
	app.runSlash("/driver")
	if app.overlay != overlayDrivers {
		t.Fatalf("overlay = %v before first task, want the driver picker", app.overlay)
	}
	app.overlay = overlayNone

	fake.view.Pause = pausedView(session.PausePaused, true, true)
	fake.view.Project.Started = true
	app.refreshView()
	app.runSlash("/driver")
	if app.overlay != overlayDrivers {
		t.Fatalf("overlay = %v, want the driver picker", app.overlay)
	}
	if got, want := strings.Join(app.drivers.names, ","), "codex,opencode,pi"; got != want {
		t.Fatalf("driver catalog = %q, want %q", got, want)
	}

	// Choosing a driver applies it to the picker's agent and does not resume.
	app.drivers.cursor = 0
	app.applyDriver(true)
	commands := fake.snapshotCommands()
	var set *session.SetDriver
	for _, command := range commands {
		if typed, ok := command.(session.SetDriver); ok {
			set = &typed
		}
	}
	if set == nil {
		t.Fatalf("driver choice sent %#v, want session.SetDriver", commands)
	}
	if set.Agent != protocol.Austin || set.Driver != "codex" {
		t.Fatalf("SetDriver = %+v, want Austin → codex", *set)
	}
	for _, command := range commands {
		if _, ok := command.(session.Continue); ok {
			t.Fatal("changing a driver resumed the session")
		}
	}

	// A completed workflow can select a driver for the next task without pausing.
	fake.view.Pause = pausedView(session.PauseRunning, false, false)
	fake.view.Project.Phase = project.PhaseDone
	app.refreshView()
	app.runSlash("/driver")
	if app.overlay != overlayDrivers {
		t.Fatalf("overlay = %v after DONE, want the driver picker", app.overlay)
	}
}

func TestModeSwitchDuringPauseSkipsResumePrompt(t *testing.T) {
	fake := newFakeSession()
	fake.view.Pause = pausedView(session.PausePaused, true, true)
	app := New(fake)

	app.runSlash("/mode goal")

	if app.overlay != overlayNone {
		t.Fatal("the mode-switch resume prompt opened while the session was paused")
	}
	switched := false
	for _, command := range fake.snapshotCommands() {
		switch command.(type) {
		case session.SwitchMode:
			switched = true
		case session.ResumeModeWork:
			t.Fatal("resume work was sent while the session was paused")
		}
	}
	if !switched {
		t.Fatalf("commands = %#v, want a quiet mode switch", fake.snapshotCommands())
	}
	if !strings.Contains(app.status, "/continue") {
		t.Fatalf("status = %q, want the pause-aware resume hint", app.status)
	}
}

func TestPausedDashboardBadgeAndDriverOverlay(t *testing.T) {
	fake := newFakeSession()
	fake.view.Pause = pausedView(session.PausePaused, true, true)
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

	waitForText(t, sim, "PAUSED")
	waitForText(t, sim, "/continue resume")

	// A driver change is possible while paused, and the picker closes after one
	// choice without sending any resume command.
	typeText(sim, "/driver")
	injectKey(sim, tcell.KeyEnter, 0, tcell.ModNone)
	waitForText(t, sim, "DRIVERS")
	waitForText(t, sim, "codex")
	injectKey(sim, tcell.KeyEnter, 0, tcell.ModNone)
	waitGone(t, sim, "DRIVERS")
	waitForCommands(t, fake, func(commands []session.Command) bool {
		for _, command := range commands {
			if _, ok := command.(session.SetDriver); ok {
				return true
			}
		}
		return false
	})
	for _, command := range fake.snapshotCommands() {
		if _, ok := command.(session.Continue); ok {
			t.Fatal("choosing a driver resumed the session")
		}
	}
}
