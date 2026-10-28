package main

import (
	"errors"
	"image"
	"path/filepath"
	"strings"

	"github.com/atfa/duo/internal/xduo"
	"github.com/gdamore/tcell/v3"
	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui/v5"
	"github.com/metaspartan/gotui/v5/widgets"
)

type tunnelStartResult struct {
	tunnel *xduo.Tunnel
	err    error
}

func (p *panel) toggleTunnel() {
	if p.tunnelPending != nil || p.tunnel != nil && p.tunnel.Running() && !p.tunnel.Ready() {
		p.mode = "tunnel"
		return
	}
	if p.tunnel != nil && p.tunnel.Running() {
		p.tunnel.Close()
		p.tunnel = nil
		p.message = "Tunnel stopped. Local Server stays available."
		return
	}
	p.tunnel = nil
	p.mode = "tunnel"
	p.tunnelError = nil
	p.tunnelFocus = 0
	p.tunnelFrame = 0
	if p.server == nil {
		p.toggleServer()
		if p.server == nil {
			p.tunnelError = errors.New(p.message)
			return
		}
	}
	p.tunnelPending = make(chan tunnelStartResult)
	p.tunnelCancel = make(chan struct{})
	p.tunnelFinished = make(chan struct{})
	results, cancel, finished := p.tunnelPending, p.tunnelCancel, p.tunnelFinished
	dir, address, entry := p.dir, p.server.URL, p.server.EntryPath
	go func() {
		defer close(finished)
		tunnel, err := xduo.StartTunnel(dir, address, entry)
		select {
		case results <- tunnelStartResult{tunnel, err}:
		case <-cancel:
			if tunnel != nil {
				tunnel.Close()
			}
		}
	}()
}
func (p *panel) finishTunnelStart(result tunnelStartResult) {
	p.tunnel = result.tunnel
	p.tunnelError = result.err
	<-p.tunnelFinished
	p.tunnelPending = nil
	p.tunnelCancel = nil
	p.tunnelFinished = nil
}
func (p *panel) cancelTunnelStart() {
	if p.tunnelCancel == nil {
		return
	}
	close(p.tunnelCancel)
	<-p.tunnelFinished
	p.tunnelPending = nil
	p.tunnelCancel = nil
	p.tunnelFinished = nil
}
func (p *panel) tunnelReady() bool { return p.tunnel != nil && p.tunnel.Ready() && p.server != nil }
func (p *panel) copyTunnelCode(s tcell.Screen) {
	if !p.tunnelReady() {
		return
	}
	p.copyAccessCode(s)
}
func (p *panel) drawTunnel(s tcell.Screen) {
	p.tunnelOKRect = image.Rectangle{}
	p.tunnelCopyRect = image.Rectangle{}
	w, h := s.Size()
	width, height := min(w-4, 88), min(h-2, 17)
	if width < 40 || height < 12 {
		s.PutStrStyled(1, 2, "Enlarge terminal · Esc closes", accent)
		return
	}
	x, y := (w-width)/2, (h-height)/2
	for yy := 0; yy < h; yy++ {
		for xx := 0; xx < w; xx++ {
			c, _, _ := s.Get(xx, yy)
			s.PutStrStyled(xx, yy, c, dim)
		}
	}
	box := widgets.NewParagraph()
	box.Title = " Cloudflare Tunnel "
	box.SetRect(x, y, x+width, y+height)
	buf := ui.NewBuffer(image.Rect(0, 0, w, h))
	box.Draw(buf)
	for yy := y; yy < y+height; yy++ {
		for xx := x; xx < x+width; xx++ {
			c := buf.GetCell(image.Pt(xx, yy))
			s.PutStrStyled(xx, yy, string(c.Rune), surface)
		}
	}
	text := func(row int, label string, style tcell.Style) {
		s.PutStrStyled(x+3, row, rw.Truncate(label, width-6, "…"), style)
	}
	err := p.tunnelError
	if p.tunnel != nil && err == nil {
		err = p.tunnel.Failure()
	}
	ready := p.tunnelReady()
	switch {
	case errors.Is(err, xduo.ErrTunnelNotConfigured):
		text(y+2, "Set up remote access", surfaceAccent)
		text(y+4, xduo.TunnelSetupURL, surfaceAccent.Url(xduo.TunnelSetupURL))
		text(y+6, "This computer needs its own domain and Cloudflare tunnel.", surface)
		text(y+8, "Configuration: "+filepath.Join(p.dir, "tunnel.json"), surfaceMuted)
	case err != nil:
		text(y+2, "Tunnel could not start", errorStyle)
		text(y+4, err.Error(), surface)
		text(y+7, "Setup guide: "+xduo.TunnelSetupURL, surfaceMuted)
	case ready:
		text(y+2, "●  Remote access ready", surfaceAccent)
		text(y+4, p.tunnel.URL, surface.Url(p.tunnel.URL))
		text(y+6, "Room code: "+p.server.AccessCode, surfaceAccent)
		text(y+8, "Open the link on another device and enter this code.", surfaceMuted)
	default:
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		text(y+2, frames[p.tunnelFrame%len(frames)]+"  Loading Cloudflare Tunnel…", surfaceAccent)
		text(y+5, "Establishing the connection. Please wait…", surface)
		text(y+8, "You may close this panel; startup will continue.", surfaceMuted)
	}
	if strings.HasPrefix(p.message, "Access code copied") {
		text(y+height-4, "Copied room code without the hyphen.", surfaceAccent)
	}
	row := y + height - 2
	button := func(rect image.Rectangle, label string, style tcell.Style) {
		s.PutStrStyled(rect.Min.X, row, strings.Repeat(" ", rect.Dx()), style)
		s.PutStrStyled(rect.Min.X+1, row, label, style)
	}
	p.tunnelOKRect = image.Rect(x+width-32, row, x+width-24, row+1)
	okStyle := fieldBase
	if p.tunnelFocus == 0 {
		okStyle = primaryButton
	}
	button(p.tunnelOKRect, " OK ↵", okStyle)
	copyStyle := surfaceMuted
	if ready {
		copyStyle = fieldBase
		if p.tunnelFocus == 1 {
			copyStyle = primaryButton
		}
		p.tunnelCopyRect = image.Rect(x+width-22, row, x+width-3, row+1)
	}
	button(image.Rect(x+width-22, row, x+width-3, row+1), "Copy room code", copyStyle)
}
