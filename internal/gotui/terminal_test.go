package gotui

import (
	"context"
	"image"
	"testing"
	"time"

	"github.com/gdamore/tcell/v3"
	ui "github.com/metaspartan/gotui/v5"
)

// TestTerminalOutlivesTheApp is what /new relies on: an App that stops on a
// caller-owned Terminal must leave it open, so the next session's App can run
// on the same screen.
func TestTerminalOutlivesTheApp(t *testing.T) {
	backend, err := ui.NewBackend(&ui.InitConfig{SimulationMode: true, SimulationSize: image.Pt(120, 42)})
	if err != nil {
		t.Fatal(err)
	}
	term := &Terminal{backend: backend}
	defer term.Close()
	sim := backend.Screen.(tcell.SimulationScreen)

	for i := 0; i < 2; i++ {
		app := New(newFakeSession())
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- app.RunTerminal(ctx, term) }()
		waitForText(t, sim, "DUO")
		app.stopApp()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("app %d: %v", i, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("app %d did not stop", i)
		}
		cancel()
	}
	if err := (&App{}).RunTerminal(context.Background(), nil); err == nil {
		t.Fatal("RunTerminal without a terminal must fail")
	}
}
