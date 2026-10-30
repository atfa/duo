package gotui

import (
	"errors"
	"testing"

	"github.com/atfa/duo/internal/protocol"
	"github.com/gdamore/tcell/v3"
)

func TestDriverPickerShortcutsRespectSessionState(t *testing.T) {
	for _, key := range []*tcell.EventKey{
		tcell.NewEventKey(tcell.KeyF4, "", tcell.ModNone),
		tcell.NewEventKey(tcell.KeyRune, "d", tcell.ModAlt),
	} {
		fake := newFakeSession()
		app := New(fake)
		app.handleKey(key)
		if app.overlay != overlayNone || !app.statusErr {
			t.Fatalf("%v should reject a running session", key)
		}
		fake.view.Project.Started = false
		app = New(fake)
		app.handleKey(tcell.NewEventKey(tcell.KeyRune, "x", tcell.ModNone))
		app.handleKey(key)
		if app.overlay != overlayDrivers || app.composer.Text() != "x" {
			t.Fatalf("%v: overlay = %v, draft = %q", key, app.overlay, app.composer.Text())
		}
	}
}

func TestDriverPickerSelectsCurrentDriverAfterLoad(t *testing.T) {
	p := newDriverPicker(newTheme())
	if !p.begin(protocol.Tony, "codex") {
		t.Fatal("begin did not request an initial catalog load")
	}
	p.beginLoad()
	p.setNames([]string{"pi", "codex", "agy"})
	selected, ok := p.selected()
	if !ok {
		t.Fatal("no driver selected")
	}
	if selected != "codex" {
		t.Fatalf("selected %q, want the currently active driver", selected)
	}
	p.begin(protocol.Tony, "codex")
	if p.begin(protocol.Tony, "codex") {
		t.Fatal("begin requested a reload although the catalog is loaded")
	}
}

func TestDriverPickerFiltersAndSwitchesAgent(t *testing.T) {
	p := newDriverPicker(newTheme())
	p.setNames([]string{"pi", "codex", "agy"})
	p.addFilter('p')
	p.addFilter('i')
	if _, ok := p.selected(); !ok {
		t.Fatal("filtered picker has no selection")
	}
	if got, _ := p.selected(); got != "pi" {
		t.Fatalf("filtered selection = %q, want pi", got)
	}

	p.setCurrent("pi")
	if p.agent != protocol.Austin {
		t.Fatalf("agent = %q, want Austin initially", p.agent)
	}
	if got := p.switchAgent(); got != protocol.Tony {
		t.Fatalf("switchAgent = %q, want Tony", got)
	}
	if len(p.filter) != 0 {
		t.Fatalf("filter survived an agent switch: %q", string(p.filter))
	}
	p.setCurrent("agy")
	if got, _ := p.selected(); got != "agy" {
		t.Fatalf("selection after switch = %q, want the Tony driver agy", got)
	}
}

func TestDriverPickerReportsLoadError(t *testing.T) {
	p := newDriverPicker(newTheme())
	p.begin(protocol.Austin, "")
	p.beginLoad()
	p.fail(errors.New("discovery failed"))
	if p.loading {
		t.Fatal("picker still loading after failure")
	}
	if p.err != "discovery failed" {
		t.Fatalf("err = %q, want the discovery failure", p.err)
	}
	if _, ok := p.selected(); ok {
		t.Fatal("failed catalog must not expose a selection")
	}
}
