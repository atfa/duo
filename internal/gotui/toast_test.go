package gotui

import (
	"context"
	"image"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/sessionstore"
	"github.com/gdamore/tcell/v3"
	ui "github.com/metaspartan/gotui/v5"
)

func TestCopiedToastAppearsAndExpires(t *testing.T) {
	fake := newFakeSession()
	fake.history = []sessionstore.TUIEntry{
		{Time: time.Now(), Pane: "Austin", Text: "hello world"},
	}
	app := New(fake)
	app.toastDuration = 100 * time.Millisecond
	var copied string
	app.clipboard = func(text string) error {
		copied = text
		return nil
	}
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

	waitForText(t, sim, "hello world")
	x, y := findTextPosition(t, sim, "hello world")
	sim.InjectMouse(x, y, tcell.Button1, tcell.ModNone)
	sim.InjectMouse(x+len("hello world"), y, tcell.Button1, tcell.ModNone)
	sim.InjectMouse(x+len("hello world"), y, tcell.ButtonNone, tcell.ModNone)

	waitForText(t, sim, "COPIED")
	if !strings.Contains(copied, "hello") {
		t.Fatalf("clipboard content = %q", copied)
	}
	waitGone(t, sim, "COPIED")
}

func findTextPosition(t *testing.T, sim tcell.SimulationScreen, want string) (int, int) {
	t.Helper()
	w, h := sim.Size()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var b strings.Builder
			for cx := x; cx < w && b.Len() < len(want); {
				text, _, width := sim.Get(cx, y)
				if width < 1 {
					width = 1
				}
				if text == "" {
					b.WriteByte(' ')
				} else {
					b.WriteString(text)
				}
				cx += width
			}
			if strings.HasPrefix(b.String(), want) {
				return x, y
			}
		}
	}
	t.Fatalf("text %q not found", want)
	return 0, 0
}
