package main

import (
	"image"
	"testing"

	"github.com/gdamore/tcell/v3"
)

func TestChromeResizeClearsHiddenControls(t *testing.T) {
	s := tcell.NewSimulationScreen("")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	defer s.Fini()
	p := panel{selected: -1, serverError: true}
	s.SetSize(120, 30)
	p.draw(s)
	if p.tunnelRect.Empty() || p.tunnelRect.Min.Y != 1 {
		t.Fatal("tunnel missing from toolbar")
	}
	for _, size := range []image.Point{{80, 24}, {32, 16}, {120, 3}} {
		s.SetSize(size.X, size.Y)
		p.draw(s)
		bounds := image.Rect(0, 0, size.X, size.Y)
		for _, r := range append(p.cardRects, p.tunnelRect, p.copyCodeRect) {
			if !r.Empty() && !r.In(bounds) {
				t.Fatalf("%v: control out of bounds: %v", size, r)
			}
		}
		if size.X < 36 || size.Y < 12 {
			if len(p.cardRects) != 0 || !p.tunnelRect.Empty() {
				t.Fatal("hidden controls remain clickable")
			}
		}
	}
}
