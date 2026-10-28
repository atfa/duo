package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atfa/duo/internal/remote"
	"github.com/atfa/duo/internal/xduo"
	"github.com/gdamore/tcell/v3"
)

type row struct {
	instance xduo.Instance
	status   xduo.DisplayStatus
	remote   string
}
type panel struct {
	dir      string
	tmux     xduo.Tmux
	rows     []row
	selected int
	offset   int
	message  string
	duo      string
	mode     string
	input    string
}

func (p *panel) refresh() {
	records, err := xduo.LoadRegistry(filepath.Join(p.dir, "instances.json"))
	if err != nil {
		p.message = err.Error()
		return
	}
	p.rows = nil
	now := time.Now()
	for _, i := range records {
		status, err := xduo.Inspect(i, now, p.tmux.Alive)
		if err != nil {
			p.message = err.Error()
		}
		_, paired, pairErr := remote.LoadPairing(i.PairingFile)
		pairing := "no"
		if pairErr != nil {
			pairing = "invalid"
		} else if paired {
			pairing = "yes"
		}
		p.rows = append(p.rows, row{i, status, pairing})
	}
	if p.selected >= len(p.rows) {
		p.selected = len(p.rows) - 1
	}
	if p.selected < 0 {
		p.selected = 0
	}
}

var base = tcell.StyleDefault.Background(tcell.NewRGBColor(10, 14, 20)).Foreground(tcell.NewRGBColor(220, 227, 236))
var accent = base.Foreground(tcell.NewRGBColor(94, 234, 212)).Bold(true)
var muted = base.Foreground(tcell.NewRGBColor(139, 152, 168))
var selected = base.Background(tcell.NewRGBColor(35, 52, 66))

func value(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func age(updated, now time.Time) string {
	if updated.IsZero() {
		return "-"
	}
	elapsed := now.Sub(updated)
	if elapsed < 0 {
		elapsed = 0
	}
	if elapsed < time.Minute {
		return fmt.Sprintf("%ds ago", int(elapsed.Seconds()))
	}
	if elapsed < time.Hour {
		return fmt.Sprintf("%dm ago", int(elapsed.Minutes()))
	}
	return fmt.Sprintf("%dh ago", int(elapsed.Hours()))
}

func rowLines(r row, now time.Time) [3]string {
	live := "unknown"
	if r.status.AliveKnown {
		if r.status.Alive {
			live = "alive"
		} else {
			live = "exited"
		}
	}
	return [3]string{
		fmt.Sprintf("%s  [%s]  %s  %s", filepath.Base(r.instance.Project), r.status.State, value(r.status.Snapshot.Phase), live),
		r.instance.Project,
		fmt.Sprintf("Austin %s / %s   Tony %s / %s   Remote %s   updated %s", value(r.status.Snapshot.Austin.Driver), value(r.status.Snapshot.Austin.Model), value(r.status.Snapshot.Tony.Driver), value(r.status.Snapshot.Tony.Model), r.remote, age(r.status.Snapshot.UpdatedAt, now)),
	}
}

func (p *panel) draw(s tcell.Screen) {
	s.SetStyle(base)
	s.Clear()
	w, h := s.Size()
	if w < 1 || h < 1 {
		return
	}
	s.PutStrStyled(1, 0, "XDUO  /  Duo workspaces", accent)
	if h > 1 {
		s.PutStrStyled(1, 1, "↑↓ select   enter open   n new   x close   r refresh   q quit", muted)
	}
	// Three lines per instance keep paths and model IDs legible.
	visible := (h - 4) / 3
	if visible < 1 {
		visible = 1
	}
	if p.selected < p.offset {
		p.offset = p.selected
	}
	if p.selected >= p.offset+visible {
		p.offset = p.selected - visible + 1
	}
	if p.offset > len(p.rows) {
		p.offset = len(p.rows)
	}
	if len(p.rows) == 0 && h > 3 {
		s.PutStrStyled(1, 3, "No instances yet.", muted)
	}
	for n := p.offset; n < len(p.rows) && n < p.offset+visible; n++ {
		r := p.rows[n]
		y := 3 + (n-p.offset)*3
		if y >= h-1 {
			break
		}
		style := base
		mark := "  "
		if n == p.selected {
			style = selected
			mark = "› "
		}
		lines := rowLines(r, time.Now())
		s.PutStrStyled(1, y, mark+lines[0], style)
		if y+1 < h-1 {
			s.PutStrStyled(3, y+1, lines[1], style)
		}
		if y+2 < h-1 {
			s.PutStrStyled(3, y+2, lines[2], style)
		}
	}
	if h > 1 {
		line := p.message
		if p.mode == "input" {
			cwd, _ := os.Getwd()
			line = "Project path: " + p.input + "  (cwd: " + cwd + "; Enter: create, Esc: cancel)"
		}
		if p.mode == "confirm" {
			line = "Close running instance? y/N"
		}
		if line != "" {
			s.PutStrStyled(1, h-1, line, accent)
		}
	}
	s.Show()
}

func (p *panel) closeSelected(confirmed bool) {
	if len(p.rows) == 0 {
		return
	}
	r := p.rows[p.selected]
	err := xduo.Remove(context.Background(), p.tmux, p.dir, r.instance, r.status, confirmed, 5*time.Second)
	if errors.Is(err, xduo.ErrConfirmationRequired) {
		p.mode = "confirm"
		return
	}
	p.refresh()
	if err != nil {
		p.message = err.Error()
	} else {
		p.message = "Instance closed."
	}
}

func (p *panel) key(e *tcell.EventKey) bool {
	if p.mode == "input" {
		switch e.Key() {
		case tcell.KeyEsc:
			p.mode = ""
			p.message = "Cancelled."
		case tcell.KeyEnter:
			path := p.input
			p.mode = ""
			_, err := xduo.Create(context.Background(), p.tmux, p.dir, path, p.duo)
			p.refresh()
			if err != nil {
				p.message = err.Error()
			} else {
				p.message = "Instance started."
				p.selected = len(p.rows) - 1
			}
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			r := []rune(p.input)
			if len(r) > 0 {
				p.input = string(r[:len(r)-1])
			}
		case tcell.KeyRune:
			p.input += e.Str()
		}
		return true
	}
	if p.mode == "confirm" {
		if e.Key() == tcell.KeyRune && strings.EqualFold(e.Str(), "y") {
			p.mode = ""
			p.closeSelected(true)
		} else if e.Key() == tcell.KeyEsc || e.Key() == tcell.KeyEnter || e.Key() == tcell.KeyRune {
			p.mode = ""
			p.message = "Cancelled."
		}
		return true
	}
	switch e.Key() {
	case tcell.KeyUp:
		if p.selected > 0 {
			p.selected--
		}
	case tcell.KeyDown:
		if p.selected+1 < len(p.rows) {
			p.selected++
		}
	case tcell.KeyEnter:
		if len(p.rows) > 0 {
			r := p.rows[p.selected]
			alive, err := p.tmux.Alive(r.instance.Session)
			if err != nil {
				p.message = err.Error()
			} else if !alive {
				p.message = "Instance is not running."
			} else if err = p.tmux.Switch(context.Background(), r.instance.Session); err != nil {
				p.message = err.Error()
			} else {
				records, saveErr := xduo.LoadRegistry(filepath.Join(p.dir, "instances.json"))
				if saveErr == nil {
					for j := range records {
						if records[j].ID == r.instance.ID {
							now := time.Now().UTC()
							records[j].LastActivatedAt = &now
						}
					}
					saveErr = xduo.SaveRegistry(filepath.Join(p.dir, "instances.json"), records)
				}
				p.refresh()
				if saveErr != nil {
					p.message = saveErr.Error()
				} else {
					p.message = "Switched. Return with tmux prefix + L."
				}
			}
		}
	case tcell.KeyRune:
		switch e.Str() {
		case "q":
			return false
		case "r":
			p.message = ""
			p.refresh()
		case "n":
			p.mode = "input"
			p.input = ""
		case "x":
			p.closeSelected(false)
		}
	}
	return true
}

func runPanel(p *panel) error {
	screen, err := tcell.NewScreen()
	if err != nil {
		return err
	}
	if err = screen.Init(); err != nil {
		return err
	}
	defer screen.Fini()
	screen.EnableFocus()
	p.refresh()
	p.draw(screen)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if p.mode == "" {
				p.refresh()
				p.draw(screen)
			}
		case event, ok := <-screen.EventQ():
			if !ok {
				return nil
			}
			switch e := event.(type) {
			case *tcell.EventKey:
				if !p.key(e) {
					return nil
				}
				if e.Key() == tcell.KeyEnter {
					screen.Sync()
				}
			case *tcell.EventFocus:
				if e.Focused {
					p.refresh()
					screen.Sync()
				}
			case *tcell.EventResize:
				screen.Sync()
			}
			p.draw(screen)
		}
	}
}

func main() {
	duo := flag.String("duo", "", "path to Duo executable")
	flag.Parse()

	tm, err := xduo.FindTmux()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	dir, err := xduo.DefaultDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	handled, err := bootstrap(*duo, os.Getenv("TMUX"), os.Getenv("XDUO_MANAGER"), terminalReady(), runTmux)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if handled {
		return
	}
	if err = runPanel(&panel{dir: dir, tmux: tm, duo: *duo}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
