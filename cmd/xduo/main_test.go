package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/xduo"
	"github.com/gdamore/tcell/v3"
)

func TestPanelRenderAndNavigation(t *testing.T) {
	now := time.Now()
	p := panel{rows: []row{
		{instance: xduo.Instance{Project: "/work/alpha"}, status: xduo.Classify(xduo.Status{}, false, true, true, now), remote: "no"},
		{instance: xduo.Instance{Project: "/work/beta"}, status: xduo.Classify(xduo.Status{UpdatedAt: now.Add(-10 * time.Second), Austin: xduo.Agent{Driver: "pi", Model: "m1"}, Tony: xduo.Agent{Driver: "codex", Model: "m2"}}, true, true, true, now), remote: "yes"},
	}}
	s := tcell.NewSimulationScreen("")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	s.SetSize(120, 14)
	p.draw(s)
	var all strings.Builder
	for y := 0; y < 14; y++ {
		for x := 0; x < 120; x++ {
			c, _, _ := s.Get(x, y)
			all.WriteString(c)
		}
		all.WriteByte('\n')
	}
	text := all.String()
	for _, want := range []string{"alpha", "/work/alpha", "Starting", "updated -", "beta", "Unknown", "pi / m1", "codex / m2", "Remote yes", "Remote no"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %q", want, text)
		}
	}
	p.key(tcell.NewEventKey(tcell.KeyDown, "", tcell.ModNone))
	if p.selected != 1 {
		t.Fatal(p.selected)
	}
	p.key(tcell.NewEventKey(tcell.KeyUp, "", tcell.ModNone))
	if p.selected != 0 {
		t.Fatal(p.selected)
	}
	if p.key(tcell.NewEventKey(tcell.KeyRune, "q", tcell.ModNone)) {
		t.Fatal("q did not quit")
	}
}

func TestBootstrap(t *testing.T) {
	var calls [][]string
	runner := func(interactive bool, args ...string) ([]byte, error) {
		calls = append(calls, args)
		if args[0] == "has-session" {
			return nil, errors.New("absent")
		}
		return nil, nil
	}
	handled, err := bootstrap("/path/duo", "", "", true, runner)
	if err != nil || !handled || len(calls) != 3 || calls[1][0] != "new-session" || calls[2][0] != "attach-session" {
		t.Fatalf("%v %v %v", handled, err, calls)
	}
	calls = nil
	handled, err = bootstrap("", "", "1", true, runner)
	if err != nil || handled || len(calls) != 0 {
		t.Fatal(handled, err, calls)
	}
	_, err = bootstrap("", "", "", false, runner)
	if err == nil {
		t.Fatal("non-TTY accepted")
	}
}
