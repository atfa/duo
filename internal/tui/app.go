package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/terminal"
)

func (a *App) Run(ctx context.Context) error {
	tty, err := terminal.Open()
	if err != nil {
		return fmt.Errorf("open terminal: %w", err)
	}
	a.tty = tty
	defer tty.Close()

	if err := tty.Raw(); err != nil {
		return err
	}
	_, _ = tty.File.WriteString(terminal.EnterAltScreen + terminal.HideCursor + terminal.MouseOn + terminal.ClearHome)
	defer func() { _, _ = tty.File.WriteString(terminal.MouseOff + terminal.ShowCursor + terminal.ExitAltScreen) }()

	a.width, a.height = tty.Size()
	eventCh, cancelSub := a.bus.Subscribe(256)
	defer cancelSub()

	inputCh := make(chan byte, 256)
	go readBytes(tty.File, inputCh)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()

	a.add(protocol.Duo, "Duo v0.3 ready. Type a task and press Enter; Austin will wake Tony when collaboration is needed.")
	a.render()

	for {
		select {
		case <-ctx.Done():
			a.leaveNative()
			return nil
		case ev, ok := <-eventCh:
			if !ok {
				return nil
			}
			a.route(ev)
			a.render()
		case b := <-inputCh:
			if a.native != "" {
				detach, forward := a.nativeDetach(b)
				if forward {
					if s, ok := a.agents.Session(a.native); ok {
						_ = s.Write([]byte{b})
					}
				}
				if detach {
					a.leaveNative()
					a.render()
					continue
				}
				continue
			}
			action := a.handleByte(b)
			if action.quit {
				return nil
			}
			if action.attach != "" {
				if err := a.enterNative(action.attach); err != nil {
					a.add(protocol.Duo, "ERROR: "+err.Error())
					a.status = err.Error()
					a.render()
				}
				continue
			}
			if action.submit {
				a.submit(ctx)
			}
			a.render()
		case <-tick.C:
			w, h := tty.Size()
			if w != a.width || h != a.height {
				a.width, a.height = w, h
			}
			a.render()
		}
	}
}

func readBytes(r io.Reader, out chan<- byte) {
	buf := make([]byte, 256)
	for {
		n, err := r.Read(buf)
		for _, b := range buf[:n] {
			out <- b
		}
		if err != nil {
			close(out)
			return
		}
	}
}

func (a *App) enterNative(agent protocol.AgentID) error {
	s, ok := a.agents.Session(agent)
	if !ok || !s.Running() {
		return fmt.Errorf("%s native Pi session is not running", agent)
	}
	a.native = agent
	a.nativeDetachBuf = nil
	a.tracker.SetHumanAttached(agent, true)
	_, _ = a.tty.File.WriteString(terminal.MouseOff + terminal.ShowCursor + terminal.ExitAltScreen + terminal.ClearHome)
	recent := s.Attach(a.tty.File)
	if len(recent) > 0 {
		_, _ = a.tty.File.Write(recent)
	}
	// Pi/Ink normally repaints on Ctrl+L. This also makes a long-hidden PTY usable immediately.
	_ = s.Write([]byte{0x0c})
	return nil
}

func (a *App) leaveNative() {
	if a.native == "" {
		return
	}
	if s, ok := a.agents.Session(a.native); ok {
		s.Detach()
	}
	a.tracker.SetHumanAttached(a.native, false)
	a.native = ""
	a.nativeDetachBuf = nil
	_, _ = a.tty.File.WriteString(terminal.EnterAltScreen + terminal.HideCursor + terminal.MouseOn + terminal.ClearHome)
}

var nativeDetachSequences = [][]byte{
	[]byte("\x1b[93;5u"),    // CSI-u Ctrl+]
	[]byte("\x1b[27;5;93~"), // xterm modifyOtherKeys Ctrl+]
}

// nativeDetach reports whether b returns to Duo and whether it still belongs to Pi input.
func (a *App) nativeDetach(b byte) (detach, forward bool) {
	if b == 0x1d { // legacy Ctrl+]
		a.nativeDetachBuf = nil
		return true, false
	}
	a.nativeDetachBuf = append(a.nativeDetachBuf, b)
	if len(a.nativeDetachBuf) > len(nativeDetachSequences[1]) {
		a.nativeDetachBuf = a.nativeDetachBuf[1:]
	}
	for _, seq := range nativeDetachSequences {
		if bytes.HasSuffix(a.nativeDetachBuf, seq) {
			a.nativeDetachBuf = nil
			return true, true
		}
	}
	return false, true
}
