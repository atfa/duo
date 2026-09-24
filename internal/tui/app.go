package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/atfa/duo/internal/events"
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
	defer func() { _, _ = tty.File.WriteString(terminal.ResetOuterModes + terminal.ExitAltScreen) }()

	a.renderer = newRenderer(frameInterval, tty.File, a.buildFrame)

	a.syncSize()
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	eventCh, cancelSub := a.bus.Subscribe(256)
	defer cancelSub()

	inputCh := make(chan byte, 256)
	go readBytes(tty.File, inputCh)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()

	a.add(protocol.Duo, "Duo v0.4.5 ready. Type a task and press Enter; Austin will wake Tony when collaboration is needed.")
	a.requestFullClear()

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
			a.markDirty()
		case b, ok := <-inputCh:
			if !ok {
				return nil
			}
			if a.native != "" {
				detach, forward := a.nativeDetach(b)
				if forward {
					if s, ok := a.agents.Session(a.native); ok {
						_ = s.Write([]byte{b})
					}
				}
				if detach {
					a.leaveNative()
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
					a.markDirty()
				}
				continue
			}
			if action.restart != "" {
				if err := a.agents.Restart(ctx, action.restart); err != nil {
					a.status = err.Error()
					a.addError(protocol.Duo, err.Error())
				} else {
					a.tracker.Reset(action.restart)
					a.status = string(action.restart) + " restarted"
				}
			}
			if action.submit {
				a.submit(ctx)
			}
			a.markDirty()
		case <-winch:
			a.resize()
		case <-a.renderer.dueChan():
			a.renderer.flush()
		case <-tick.C:
			a.spinnerTick()
		}
	}
}

// resize applies the latest host terminal size to the agent PTYs and schedules
// one full-clear frame. Any intermediate SIGWINCH sizes are intentionally
// dropped; only the most recent size matters.
func (a *App) resize() {
	w, h := a.tty.Size()
	a.width, a.height = w, h
	if err := a.agents.ResizeAll(w, h); err != nil {
		a.bus.Emit(events.Event{Kind: events.KindError, Agent: protocol.Duo, Text: err.Error()})
	}
	a.requestFullClear()
}

func (a *App) syncSize() {
	w, h := a.tty.Size()
	a.width, a.height = w, h
	if err := a.agents.ResizeAll(w, h); err != nil {
		a.status = err.Error()
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
	a.syncSize()
	a.native = agent
	a.nativeDetachBuf = nil
	a.tracker.SetHumanAttached(agent, true)
	_, _ = a.tty.File.WriteString(terminal.MouseOff + terminal.ShowCursor + terminal.ExitAltScreen + terminal.ClearHome)
	recent := s.Attach(a.tty.File)
	if len(recent) > 0 {
		_, _ = a.tty.File.Write(recent)
	}
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
	a.syncSize()
	a.nativeDetachBuf = nil
	_, _ = a.tty.File.WriteString(terminal.ResetOuterModes + terminal.EnterAltScreen + terminal.HideCursor + terminal.MouseOn + terminal.ClearHome)
	a.requestFullClear()
}

// nativeDetach reports whether b returns to Duo and whether it still belongs to Pi input.
func (a *App) nativeDetach(b byte) (detach, forward bool) {
	if b == 0x1c || b == 0x1d { // legacy Ctrl+\\ and Ctrl+]
		a.nativeDetachBuf = nil
		return true, false
	}
	a.nativeDetachBuf = append(a.nativeDetachBuf, b)
	if len(a.nativeDetachBuf) > 32 {
		a.nativeDetachBuf = a.nativeDetachBuf[1:]
	}
	if (b == 'u' || b == '~') && nativeDetachSequence(a.nativeDetachBuf) {
		a.nativeDetachBuf = nil
		return true, true
	}
	return false, true
}

func nativeDetachSequence(buf []byte) bool {
	i := strings.LastIndex(string(buf), "\x1b[")
	if i < 0 {
		return false
	}
	seq := string(buf[i+2:])
	if strings.HasSuffix(seq, "u") {
		parts := strings.Split(strings.TrimSuffix(seq, "u"), ";")
		if len(parts) != 2 {
			return false
		}
		if !nativeDetachCodepoint(parts[0]) {
			return false
		}
		modifier, _, _ := strings.Cut(parts[1], ":")
		return ctrlModifier(modifier)
	}
	if strings.HasSuffix(seq, "~") {
		parts := strings.Split(strings.TrimSuffix(seq, "~"), ";")
		return len(parts) == 3 && parts[0] == "27" && ctrlModifier(parts[1]) && nativeDetachCodepoint(parts[2])
	}
	return false
}

func nativeDetachCodepoint(s string) bool {
	codepoint, err := strconv.Atoi(s)
	return err == nil && (codepoint == 92 || codepoint == 93 || codepoint == 12305) // \, ], 】
}

func ctrlModifier(s string) bool {
	modifier, err := strconv.Atoi(s)
	return err == nil && modifier > 0 && (modifier-1)&4 != 0
}
