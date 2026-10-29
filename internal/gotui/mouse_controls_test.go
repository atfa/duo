package gotui

import (
	"image"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/gdamore/tcell/v3"
	ui "github.com/metaspartan/gotui/v5"
)

func TestMouseModeAndConfirmation(t *testing.T) {
	fake, app := newModeSwitchApp(t, project.Snapshot{Mode: project.ModeStep, Phase: project.PhaseExecute, Started: true, Plan: "work", PlanVersion: 1, Steps: project.Steps{{Title: "one"}}})
	app.openModePicker()
	l := layout{width: 100, height: 40, main: image.Rect(0, 2, 100, 38)}
	app.modePicker.place(l)
	app.modePicker.Draw(ui.NewBuffer(app.modePicker.GetRect()))
	p := app.modePicker
	app.handleMouse(tcell.NewEventMouse(p.Inner.Min.X, p.Inner.Min.Y+2, tcell.Button1, 0))
	if app.overlay != overlayConfirm {
		t.Fatalf("click FAST: overlay %v", app.overlay)
	}
	if len(switchCommands(fake)) != 0 {
		t.Fatal("mode changed without confirmation")
	}
	app.confirm.place(l)
	app.confirm.Draw(ui.NewBuffer(app.confirm.GetRect()))
	c := app.confirm
	app.handleMouse(tcell.NewEventMouse(c.Inner.Min.X, c.Inner.Min.Y, tcell.ButtonNone, 0))
	app.handleMouse(tcell.NewEventMouse(c.Inner.Min.X, c.Inner.Min.Y, tcell.Button1, 0))
	if app.overlay != overlayConfirm || len(switchCommands(fake)) != 0 {
		t.Fatal("click outside buttons confirmed")
	}
	app.handleMouse(tcell.NewEventMouse(c.Inner.Max.X-1, c.Inner.Max.Y-1, tcell.ButtonNone, 0))
	app.handleMouse(tcell.NewEventMouse(c.Inner.Max.X-1, c.Inner.Max.Y-1, tcell.Button1, 0))
	if len(switchCommands(fake)) != 1 {
		t.Fatal("explicit confirm did not switch mode")
	}
}
