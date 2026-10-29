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
	"github.com/atfa/duo/internal/terminal"
	"github.com/atfa/duo/internal/xduo"
	"github.com/gdamore/tcell/v3"
	rw "github.com/mattn/go-runewidth"
)

type row struct {
	instance xduo.Instance
	status   xduo.DisplayStatus
	remote   string
}
type panel struct {
	dir                 string
	tmux                xduo.Tmux
	rows                []row
	selected            int
	offset              int
	message             string
	duo                 string
	mode                string
	input               string
	columns             int
	cardRects           []image.Rectangle
	mouseDown           bool
	picker              directoryPicker
	server              *xduo.WebServer
	serverError         bool
	copyCodeRect        image.Rectangle
	tunnel              *xduo.Tunnel
	tunnelRect          image.Rectangle
	tunnelPending       chan tunnelStartResult
	tunnelCancel        chan struct{}
	tunnelFinished      chan struct{}
	tunnelError         error
	tunnelFrame         int
	tunnelFocus         int
	tunnelOKRect        image.Rectangle
	tunnelCopyRect      image.Rectangle
	tunnelCopyRequested bool
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
var selected = base.Background(tcell.NewRGBColor(36, 48, 55))

var surface = base.Background(tcell.NewRGBColor(20, 27, 34))
var surfaceMuted = surface.Foreground(tcell.NewRGBColor(139, 158, 180))
var surfaceAccent = surface.Foreground(tcell.NewRGBColor(94, 234, 212)).Bold(true)
var borderStyle = surface.Foreground(tcell.NewRGBColor(54, 75, 96))
var fieldBase = surface.Background(tcell.NewRGBColor(28, 40, 55))
var primaryButton = base.Background(tcell.NewRGBColor(94, 234, 212)).Foreground(tcell.NewRGBColor(10, 25, 30))
var dim = base.Foreground(tcell.NewRGBColor(53, 68, 83))
var shadow = base.Background(tcell.NewRGBColor(5, 8, 12))
var errorStyle = surface.Foreground(tcell.NewRGBColor(251, 146, 137))

func cardBg(active bool) tcell.Color {
	if active {
		return tcell.NewRGBColor(36, 48, 55)
	}
	return tcell.NewRGBColor(20, 27, 34)
}

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
	p.tunnelRect, p.copyCodeRect = image.Rectangle{}, image.Rectangle{}
	p.cardRects = nil
	put := func(x, y int, text string, style tcell.Style) {
		if x >= 0 && x < w && y >= 0 && y < h {
			s.PutStrStyled(x, y, rw.Truncate(text, w-x, "…"), style)
		}
	}
	p.columns = max(1, w/36)
	if w < 36 || h < 12 {
		p.picker.hits = nil
		p.tunnelOKRect, p.tunnelCopyRect = image.Rectangle{}, image.Rectangle{}
		hint := "Enlarge terminal · Q quits"
		if p.mode == "input" || p.mode == "tunnel" {
			hint = "Enlarge terminal · Esc closes"
		}
		put(0, 0, hint, muted)
		s.Show()
		return
	}
	put(1, 0, "◆ XDUO   /   Workspaces", accent)
	put(1, 1, "[ R Refresh ]   [ X Close ]   [ Q Quit ]", muted)
	label, style := "[ S Server: Off ]", muted
	if p.server != nil {
		label, style = "[ S Server: On ]", primaryButton

	} else if p.serverError {
		label, style = "[ S Server: Error ]", errorStyle
	}
	put(43, 1, label, style)
	tunnelLabel, tunnelStyle := "[ T Tunnel: Off ]", muted
	if p.tunnelPending != nil {
		tunnelLabel = "[ T Tunnel: Starting ]"
	} else if p.tunnelError != nil {
		tunnelLabel, tunnelStyle = "[ T Tunnel: Error ]", errorStyle
		if errors.Is(p.tunnelError, xduo.ErrTunnelNotConfigured) {
			tunnelLabel, tunnelStyle = "[ T Tunnel: Setup ]", muted
		}
	}
	if p.tunnel != nil {
		if !p.tunnel.Running() {
			tunnelLabel, tunnelStyle = "[ T Tunnel: Error ]", errorStyle
		} else if p.tunnel.Ready() {
			tunnelLabel, tunnelStyle = "[ T Tunnel: On ]", primaryButton
		} else {
			tunnelLabel = "[ T Tunnel: Starting ]"
		}
	}
	tunnelX := 43 + rw.StringWidth(label) + 3
	if tunnelX+rw.StringWidth(tunnelLabel) <= w-1 {
		put(tunnelX, 1, tunnelLabel, tunnelStyle)
		p.tunnelRect = image.Rect(tunnelX, 1, tunnelX+rw.StringWidth(tunnelLabel), 2)
	}
	if w > 80 {
		put(w-27, 0, fmt.Sprintf("%d workspaces  ·  local", len(p.rows)), muted)
	}
	p.copyCodeRect = image.Rectangle{}
	if p.server != nil {
		address := p.server.URL + "  Code: " + p.server.AccessCode
		addressX := tunnelX + rw.StringWidth(tunnelLabel) + 3
		put(addressX, 1, address, accent)
		copyX := addressX + rw.StringWidth(address) + 1
		if copyX+6 <= w-1 {
			put(copyX, 1, "[COPY]", fieldBase)
			p.copyCodeRect = image.Rect(copyX, 1, copyX+6, 2)
		}
		if len(p.server.LANURLs) > 0 {
			put(copyX+8, 1, "LAN: "+p.server.LANURLs[0], muted)
		}
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
	for index := 0; index <= len(p.rows); index++ {
		row := index/p.columns - p.offset
		if row < 0 || row >= visible {
			continue
		}
		rect := image.Rect(index%p.columns*width+1, gridTop+row*9, min(w-1, (index%p.columns+1)*width-1), min(h-2, gridTop+row*9+8))
		p.cardRects[index] = rect
		if rect.Dx() < 4 || rect.Dy() < 2 {
			continue
		}
		active := index == p.selected+1
		cardStyle := surface
		if active {
			cardStyle = selected
		}
		for y := rect.Min.Y; y < rect.Max.Y; y++ {
			put(rect.Min.X, y, strings.Repeat(" ", rect.Dx()), cardStyle)
			if active {
				put(rect.Min.X, y, "▏", cardStyle.Foreground(tcell.NewRGBColor(111, 218, 193)))
			}
		}
		cardText := func(y int, text string, style tcell.Style) {
			if y < rect.Max.Y {
				put(rect.Min.X+2, y, rw.Truncate(text, max(0, rect.Dx()-4), "…"), style.Background(cardBg(active)))
			}
		}
		if index == 0 {
			cardText(rect.Min.Y+1, "＋  NEW WORKSPACE", accent)
			cardText(rect.Min.Y+3, "Create a workspace", base.Bold(true))
			cardText(rect.Min.Y+4, "Choose a project directory", muted)
			cardText(rect.Min.Y+6, "Enter  ↗", muted)
		} else {
			r := p.rows[index-1]
			live := "unknown"
			if r.status.AliveKnown {
				live = "exited · Enter to resume"
				if r.status.Alive {
					live = "running"
				}
			}
			cardText(rect.Min.Y+1, filepath.Base(r.instance.Project), base.Bold(true))
			cardText(rect.Min.Y+2, r.instance.Project, muted)
			cardText(rect.Min.Y+3, fmt.Sprintf("%s  ·  %s  ·  %s", r.status.State, value(r.status.Snapshot.Phase), live), accent)
			cardText(rect.Min.Y+4, fmt.Sprintf("Austin  %s / %s", value(r.status.Snapshot.Austin.Driver), value(r.status.Snapshot.Austin.Model)), base)
			cardText(rect.Min.Y+5, fmt.Sprintf("Tony    %s / %s", value(r.status.Snapshot.Tony.Driver), value(r.status.Snapshot.Tony.Model)), base)
			cardText(rect.Min.Y+6, fmt.Sprintf("Remote %s  ·  updated %s", r.remote, age(r.status.Snapshot.UpdatedAt, time.Now())), muted)
		}
	}
	put(1, h-2, "Arrow keys select · Enter open · click card to open · N new", muted)
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
			put(1, h-1, line, accent)
		}
	}
	if p.mode == "input" {
		p.drawPicker(s)
	}
	if p.mode == "tunnel" {
		p.drawTunnel(s)
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
	if p.mode == "tunnel" {
		switch e.Key() {
		case tcell.KeyEsc:
			p.mode = ""
		case tcell.KeyTab, tcell.KeyBacktab, tcell.KeyLeft, tcell.KeyRight:
			if p.tunnelReady() {
				p.tunnelFocus = 1 - p.tunnelFocus
			} else {
				p.tunnelFocus = 0
			}
		case tcell.KeyEnter, tcell.KeyRune:
			if e.Key() == tcell.KeyRune && e.Str() != " " {
				return true
			}
			if p.tunnelFocus == 0 {
				p.mode = ""
			} else {
				p.tunnelCopyRequested = true
			}
		}
		return true
	}
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
		case "t":
			p.toggleTunnel()
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
		p.cancelTunnelStart()
		if p.tunnel != nil {
			p.tunnel.Close()
			p.tunnel = nil
		}
		err := p.server.Close()
		p.server = nil
		p.serverError = err != nil
		p.message = "Browser server stopped. Duo sessions keep running."
		if err != nil {
			p.message = err.Error()
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		p.serverError = true
		p.message = err.Error()
		return
	}
	server, err := xduo.StartWeb(p.tmux, executable)
	p.serverError = err != nil
	if err != nil {
		p.message = err.Error()
		return
	}
	p.server = server
	p.message = "Server ready. Local access is passwordless; other devices use the remote code above."
}

func (p *panel) copyAccessCode(screen tcell.Screen) {
	if p.server == nil {
		return
	}
	code := strings.ReplaceAll(p.server.AccessCode, "-", "")
	screen.SetClipboard([]byte(code))
	if err := terminal.WriteClipboard(code); err != nil {
		p.message = "Code sent to terminal clipboard; system clipboard: " + err.Error()
		return
	}
	p.message = "Access code copied (without hyphen)."
}

func (p *panel) serverStopRequests() <-chan struct{} {
	if p.server == nil {
		return nil
	}
	return p.server.StopRequests
}

func (p *panel) serverConnections() <-chan struct{} {
	if p.server == nil {
		return nil
	}
	return p.server.Connections
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
	defer p.cancelTunnelStart()
	defer func() {
		if p.tunnel != nil {
			p.tunnel.Close()
		}
	}()
	defer func() {
		if p.server != nil {
			_ = p.server.Close()
		}
	}()
	screen.EnableFocus()
	screen.EnableMouse(tcell.MouseButtonEvents)
	p.refresh()
	p.draw(screen)
	animation := time.NewTicker(150 * time.Millisecond)
	defer animation.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case result := <-p.tunnelPending:
			p.finishTunnelStart(result)
			p.draw(screen)
		case <-animation.C:
			if p.mode == "tunnel" && (p.tunnelPending != nil || p.tunnel != nil && p.tunnel.Running() && !p.tunnel.Ready()) {
				p.tunnelFrame++
				p.draw(screen)
			}
		case <-p.serverConnections():
			if p.mode == "tunnel" {
				p.mode = ""
			}
			p.draw(screen)
		case <-p.serverStopRequests():
			if p.server != nil {
				p.toggleServer()
			}
			p.draw(screen)
		case <-ticker.C:
			if p.mode == "tunnel" {
				p.draw(screen)
			}
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
				if p.tunnelCopyRequested {
					p.tunnelCopyRequested = false
					p.copyTunnelCode(screen)
				}
				if e.Key() == tcell.KeyEnter {
					screen.Sync()
				}
			case *tcell.EventMouse:
				down := e.Buttons()&tcell.Button1 != 0
				wasPicker := p.mode == "input"
				if down && !p.mouseDown && p.mode == "tunnel" {
					x, y := e.Position()
					if image.Pt(x, y).In(p.tunnelOKRect) {
						p.mode = ""
					} else if image.Pt(x, y).In(p.tunnelCopyRect) {
						p.copyTunnelCode(screen)
					}
					p.mouseDown = down
					p.draw(screen)
					continue
				}
				if down && !p.mouseDown && wasPicker {
					x, y := e.Position()
					p.pickerClick(x, y)
				}
				if down && !p.mouseDown && !wasPicker && p.mode == "" {
					x, y := e.Position()
					if image.Pt(x, y).In(p.tunnelRect) {
						p.toggleTunnel()
					} else if image.Pt(x, y).In(p.copyCodeRect) {
						p.copyAccessCode(screen)
					} else if y == 1 {
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
	if url := os.Getenv("XDUO_LOCK_SERVER"); url != "" {
		if err := runServerLock(url); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

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
