package main

import (
	"image"
	"net/http"
	"strings"
	"time"

	"github.com/gdamore/tcell/v3"
)

// tmux runs this in the local client's terminal, not the shared manager pane.
// Browser clients therefore keep seeing and controlling the real dashboard.
func runServerLock(address string) error {
	s, err := tcell.NewScreen()
	if err != nil {
		return err
	}
	if err = s.Init(); err != nil {
		return err
	}
	defer s.Fini()
	s.EnableMouse(tcell.MouseButtonEvents)
	client := &http.Client{Timeout: time.Second}
	stopping := false
	var button image.Rectangle
	draw := func() {
		s.SetStyle(base)
		s.Clear()
		s.HideCursor()
		w, h := s.Size()
		label := "  STOP SERVER  "
		x, y := max(0, (w-len(label))/2), h/2
		button = image.Rect(x, y, min(w, x+len(label)), y+1)
		s.PutStrStyled(x, y, label, primaryButton)
		s.Show()
	}
	stop := func() {
		if stopping {
			return
		}
		req, err := http.NewRequest(http.MethodPost, address+"api/stop", nil)
		if err != nil {
			return
		}
		req.Header.Set("Origin", strings.TrimSuffix(address, "/"))
		res, err := client.Do(req)
		if err == nil {
			res.Body.Close()
			stopping = res.StatusCode == http.StatusNoContent
		}
	}
	draw()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	down := false
	for {
		select {
		case event := <-s.EventQ():
			switch e := event.(type) {
			case *tcell.EventResize:
				s.Sync()
				draw()
			case *tcell.EventKey:
				if e.Key() == tcell.KeyEnter || e.Key() == tcell.KeyRune && e.Str() == " " {
					stop()
				}
			case *tcell.EventMouse:
				pressed := e.Buttons()&tcell.Button1 != 0
				x, y := e.Position()
				if pressed && !down && image.Pt(x, y).In(button) {
					stop()
				}
				down = pressed
			}
		case <-ticker.C:
			res, err := client.Get(address + "api/session")
			if err != nil {
				return nil
			}
			res.Body.Close()
		}
	}
}
