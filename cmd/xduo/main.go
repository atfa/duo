package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atfa/duo/internal/remote"
	"github.com/atfa/duo/internal/xduo"
	"github.com/gdamore/tcell/v3"
	ui "github.com/metaspartan/gotui/v5"
	"github.com/metaspartan/gotui/v5/widgets"
)

type row struct {
	instance xduo.Instance
	status   xduo.DisplayStatus
	remote   string
}
type panel struct {
	dir         string
	tmux        xduo.Tmux
	rows        []row
	selected    int
	offset      int
	message     string
	duo         string
	mode        string
	input       string
	columns     int
	cardRects   []image.Rectangle
	mouseDown   bool
	picker      directoryPicker
	server      *xduo.WebServer
	serverError bool
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
	if p.selected < -1 {
		p.selected = -1
	}
}

var base = tcell.StyleDefault.Background(tcell.NewRGBColor(10, 14, 20)).Foreground(tcell.NewRGBColor(220, 227, 236))
var accent = base.Foreground(tcell.NewRGBColor(94, 234, 212)).Bold(true)
var muted = base.Foreground(tcell.NewRGBColor(139, 152, 168))
var selected = base.Background(tcell.NewRGBColor(35, 52, 66))

var surface = base.Background(tcell.NewRGBColor(19, 27, 38))
var surfaceMuted = surface.Foreground(tcell.NewRGBColor(139, 158, 180))
var surfaceAccent = surface.Foreground(tcell.NewRGBColor(94, 234, 212)).Bold(true)
var borderStyle = surface.Foreground(tcell.NewRGBColor(54, 75, 96))
var fieldBase = surface.Background(tcell.NewRGBColor(28, 40, 55))
var primaryButton = base.Background(tcell.NewRGBColor(94, 234, 212)).Foreground(tcell.NewRGBColor(10, 25, 30))
var dim = base.Foreground(tcell.NewRGBColor(53, 68, 83))
var shadow = base.Background(tcell.NewRGBColor(5, 8, 12))
var errorStyle = surface.Foreground(tcell.NewRGBColor(251, 146, 137))

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
			live = "exited · Enter to resume"
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
	s.HideCursor()
	w, h := s.Size()
	if w < 1 || h < 1 {
		return
	}
	s.PutStrStyled(1, 0, "◆ XDUO   /   Workspaces", accent)
	s.PutStrStyled(1, 1, "[ R Refresh ]   [ X Close ]   [ Q Quit ]", muted)
	label, style := "[ S Server: Off ]", muted
	if p.server != nil {
		label, style = "[ S Server: On ]", primaryButton

	} else if p.serverError {
		label, style = "[ S Server: Error ]", errorStyle
	}
	s.PutStrStyled(43, 1, label, style)
	if w > 80 {
		s.PutStrStyled(w-27, 0, fmt.Sprintf("%d workspaces  ·  local", len(p.rows)), muted)
	}
	if p.server != nil {
		address := p.server.URL + "  Code: " + p.server.AccessCode
		if len(p.server.LANURLs) > 0 {
			address += "  LAN: " + p.server.LANURLs[0]
		}
		s.PutStrStyled(64, 1, address, accent)
	}
	gridTop := 3
	p.columns = max(1, w/36)
	width := max(1, w/p.columns)
	visible := max(1, (h-gridTop-2)/9)
	focusRow := (p.selected + 1) / p.columns
	if focusRow < p.offset {
		p.offset = focusRow
	}
	if focusRow >= p.offset+visible {
		p.offset = focusRow - visible + 1
	}
	p.cardRects = make([]image.Rectangle, len(p.rows)+1)
	buf := ui.NewBuffer(image.Rect(0, 0, w, h))
	for index := 0; index <= len(p.rows); index++ {
		row := index/p.columns - p.offset
		if row < 0 || row >= visible {
			continue
		}
		rect := image.Rect(index%p.columns*width+1, gridTop+row*9, min(w, (index%p.columns+1)*width), min(h-2, gridTop+row*9+8))
		p.cardRects[index] = rect
		card := widgets.NewParagraph()
		card.SetRect(rect.Min.X, rect.Min.Y, rect.Max.X, rect.Max.Y)
		card.BorderStyle.Fg = ui.ColorBlue
		card.TextStyle.Fg = ui.ColorWhite
		card.WrapText = false
		if index == 0 {
			card.Title = " New workspace "
			card.Text = "\n  ＋  Create a workspace\n\n  Choose a project directory\n  Enter or click to begin"
		} else {
			r := p.rows[index-1]
			card.Title = " " + filepath.Base(r.instance.Project) + " "
			live := "unknown"
			if r.status.AliveKnown {
				live = "exited · Enter to resume"
				if r.status.Alive {
					live = "alive"
				}
			}
			card.Text = fmt.Sprintf("%s · %s · %s\n%s\nAustin %s / %s\nTony %s / %s\nRemote %s · updated %s", r.status.State, value(r.status.Snapshot.Phase), live, r.instance.Project, value(r.status.Snapshot.Austin.Driver), value(r.status.Snapshot.Austin.Model), value(r.status.Snapshot.Tony.Driver), value(r.status.Snapshot.Tony.Model), r.remote, age(r.status.Snapshot.UpdatedAt, time.Now()))
		}
		if index == p.selected+1 {
			card.BorderStyle.Fg = ui.ColorCyan
		}
		card.Draw(buf)
	}
	for y := gridTop; y < h-2; y++ {
		for x := 0; x < w; x++ {
			c := buf.GetCell(image.Pt(x, y))
			if c.Rune != 0 {
				style := surface
				if c.Style.Fg == ui.ColorBlue {
					style = borderStyle
				}
				if c.Style.Fg == ui.ColorCyan {
					style = surfaceAccent
				}
				s.PutStrStyled(x, y, string(c.Rune), style)
			}
		}
	}
	s.PutStrStyled(1, h-2, "Arrow keys select · Enter open · click card to open · N new", muted)
	if h > 1 {
		line := p.message
		if p.mode == "input" {
			line = ""
		}
		if p.mode == "confirm" {
			line = "Close running instance? y/N"
		}
		if line == "" {
			line = "Select [+] to create a workspace. Duo keeps running when you return here."
		}
		if line != "" {
			s.PutStrStyled(1, h-1, line, accent)
		}
	}
	if p.mode == "input" {
		p.drawPicker(s)
	}
	s.Show()
}

func (p *panel) closeSelected(confirmed bool) {
	if len(p.rows) == 0 || p.selected < 0 {
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
		p.pickerKey(e)
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
		p.selected = max(-1, p.selected-max(1, p.columns))
	case tcell.KeyLeft:
		p.selected = max(-1, p.selected-1)
	case tcell.KeyRight:
		p.selected = min(len(p.rows)-1, p.selected+1)
	case tcell.KeyDown:
		if p.selected+1 < len(p.rows) {
			p.selected = min(len(p.rows)-1, p.selected+max(1, p.columns))
		}
	case tcell.KeyEnter:
		if p.selected == -1 {
			p.openPicker()
			return true
		}
		if len(p.rows) > 0 {
			r := p.rows[p.selected]
			alive, err := p.tmux.Alive(r.instance.Session)
			if err != nil {
				p.message = err.Error()
			} else {
				if !alive {
					err = xduo.Resume(context.Background(), p.tmux, r.instance, p.duo)
				}
				if err != nil {
					p.message = err.Error()
					return true
				}
				if err = p.tmux.Switch(context.Background(), r.instance.Session); err != nil {
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
						p.message = "Switched. Use Back to xduo to return."
						if err := p.remember(r.instance.Project); err != nil {
							p.message = "Switched; history: " + err.Error()
						}
					}
				}
			}
		}
	case tcell.KeyRune:
		switch e.Str() {
		case "q":
			return false
		case "s":
			p.toggleServer()
		case "r":
			p.message = ""
			p.refresh()
		case "n":
			p.openPicker()
		case "x":
			p.closeSelected(false)
		}
	}
	return true
}

func (p *panel) toggleServer() {
	if p.server != nil {
		err := p.server.Close()
		p.server = nil
		p.serverError = err != nil
		p.message = "Browser server stopped. Duo sessions keep running."
		if err != nil {
			p.message = err.Error()
		}
		return
	}
	server, err := xduo.StartWeb(p.tmux)
	p.serverError = err != nil
	if err != nil {
		p.message = err.Error()
		return
	}
	p.server = server
	p.message = "Server ready. Local access is passwordless; other devices use the remote code above."
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
	defer func() {
		if p.server != nil {
			_ = p.server.Close()
		}
	}()
	screen.EnableFocus()
	screen.EnableMouse(tcell.MouseButtonEvents)
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
			case *tcell.EventMouse:
				down := e.Buttons()&tcell.Button1 != 0
				wasPicker := p.mode == "input"
				if down && !p.mouseDown && wasPicker {
					x, y := e.Position()
					p.pickerClick(x, y)
				}
				if down && !p.mouseDown && !wasPicker && p.mode == "" {
					x, y := e.Position()
					if y == 1 {
						for i, key := range []string{"r", "x", "q", "s"} {
							starts := []int{1, 17, 31, 43}
							ends := []int{14, 28, 41, 63}
							if x >= starts[i] && x < ends[i] {
								if !p.key(tcell.NewEventKey(tcell.KeyRune, key, tcell.ModNone)) {
									return nil
								}
							}
						}
					} else {
						for i, rect := range p.cardRects {
							if image.Pt(x, y).In(rect) {
								p.selected = i - 1
								p.key(tcell.NewEventKey(tcell.KeyEnter, "", tcell.ModNone))
								break
							}
						}
					}
				}
				p.mouseDown = down
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
	if err = runPanel(&panel{dir: dir, tmux: tm, duo: *duo, selected: -1}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
