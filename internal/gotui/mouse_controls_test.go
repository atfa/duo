package gotui

import (
	rw "github.com/mattn/go-runewidth"
	"image"
	"strings"
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

func TestFooterMouseActionsInBothLanguages(t *testing.T) {
	app := New(newFakeSession())
	app.layout.width = 300
	want := []tcell.Key{tcell.KeyEnter, tcell.KeyCtrlJ, tcell.KeyCtrlE, tcell.KeyCtrlS, tcell.KeyCtrlM, tcell.KeyCtrlA, tcell.KeyF1, tcell.KeyF2, tcell.KeyCtrlQ}
	for _, lang := range []string{"en", "zh"} {
		t.Run(lang, func(t *testing.T) {
			*app.theme.Lang = lang
			hint, actions := app.footerHintData()
			parts := strings.Split(hint, " · ")
			if len(parts) != len(want) {
				t.Fatalf("hint %q", hint)
			}
			x := 0
			for i, part := range parts {
				key, ok := footerKeyAt(hint, actions, x)
				if !ok || key != want[i] {
					t.Errorf("%q: got %v, %v; want %v", part, key, ok, want[i])
				}
				if i == 5 {
					key, ok = footerKeyAt(hint, actions, x+rw.StringWidth(part)-1)
					if !ok || key != tcell.KeyCtrlT {
						t.Errorf("Tony target got %v, %v", key, ok)
					}
				}
				x += rw.StringWidth(part) + rw.StringWidth(" · ")
			}
		})
	}
}
