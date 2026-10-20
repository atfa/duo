package gotui

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/terminal"
)

// nativeInputAssembler keeps terminal escape sequences intact while the real
// agent PTY owns the terminal. Without it a split ESC can be delivered as an
// Escape key before the rest of a function-key reply arrives.
type nativeInputAssembler struct{ pending []byte }

func (f *nativeInputAssembler) add(b byte) []byte {
	if len(f.pending) == 0 {
		if b == 0x1b {
			f.pending = []byte{b}
			return nil
		}
		return []byte{b}
	}
	f.pending = append(f.pending, b)
	if len(f.pending) == 2 {
		switch b {
		case '[', ']', 'O':
			return nil
		default:
			return f.flush()
		}
	}
	if len(f.pending) > 256 {
		return f.flush()
	}
	switch f.pending[1] {
	case '[':
		if b >= 0x40 && b <= 0x7e {
			return f.flush()
		}
	case ']':
		if b == 0x07 || (len(f.pending) >= 2 && f.pending[len(f.pending)-2] == 0x1b && b == '\\') {
			return f.flush()
		}
	case 'O':
		if b >= 0x40 && b <= 0x7e {
			return f.flush()
		}
	}
	return nil
}

func (f *nativeInputAssembler) flush() []byte {
	out := f.pending
	f.pending = nil
	return out
}

// enterNative hands the terminal to the agent. It returns after the human
// detaches, so the dashboard's event loop is paused rather than consuming the
// keystrokes meant for the agent.
func (a *App) enterNative(target protocol.AgentID) {
	if a.native != "" {
		return
	}
	if err := a.nativeAttach(target); err != nil {
		a.setStatus(err.Error(), true)
		a.addLabeled(protocol.Duo, directionLabel(protocol.Duo, ""), "ERROR: "+err.Error(), true, false)
	}
}

func (a *App) nativeAttach(target protocol.AgentID) error {
	if a.live.ProcessState(target) != agent.ProcessRunning {
		return fmt.Errorf("%s agent session is not running", target)
	}
	if a.screen != nil {
		if err := a.screen.Suspend(); err != nil {
			return fmt.Errorf("suspend dashboard: %w", err)
		}
	}
	openTTY := a.openTTY
	if openTTY == nil {
		openTTY = terminal.Open
	}
	tty, err := openTTY()
	if err != nil {
		a.resumeDashboard()
		return fmt.Errorf("open terminal: %w", err)
	}
	if err := tty.Raw(); err != nil {
		_ = tty.Close()
		a.resumeDashboard()
		return err
	}
	a.nativeTTY = tty
	a.nativeDetachBuf = nil
	a.nativeInput = nativeInputAssembler{}
	defer a.leaveNative()

	_, _ = tty.File.WriteString(terminal.MouseOff + terminal.BracketedPasteOff + terminal.ShowCursor + terminal.ExitAltScreen + terminal.ClearHome)
	recent, err := a.svc.Attach(target, tty.File)
	if err != nil {
		return fmt.Errorf("attach %s: %w", target, err)
	}
	a.native = target
	if len(recent) > 0 {
		_, _ = tty.File.Write(recent)
	}
	if cols, rows := tty.Size(); cols > 0 && rows > 0 {
		_ = a.svc.Resize(cols, rows)
	}
	return a.nativeLoop(tty, target)
}

func (a *App) nativeLoop(tty *terminal.TTY, target protocol.AgentID) error {
	inputCh := make(chan []byte, 32)
	errCh := make(chan error, 1)
	go readNative(tty.File, inputCh, errCh)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	lastCols, lastRows := tty.Size()
	var pendingAt time.Time
	for {
		select {
		case <-a.ctx.Done():
			return nil
		case <-a.stop:
			return nil
		case chunk, ok := <-inputCh:
			if !ok {
				return nil
			}
			forward, consumed, detach := a.nativeInputChunk(chunk)
			if len(forward) > 0 {
				if err := a.svc.Write(target, forward); err != nil {
					return err
				}
			}
			if detach {
				if consumed < len(chunk) {
					// Bytes after the detach belong to the dashboard. They are
					// deliberately dropped: replaying arbitrary input into the
					// composer after a PTY handoff is more surprising than losing
					// the tail of one escape sequence.
				}
				return nil
			}
			if a.nativeInput.pending != nil {
				pendingAt = time.Now()
			}
		case err := <-errCh:
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			return nil
		case ev, ok := <-a.subEvents:
			if ok {
				a.refreshView()
				a.route(ev)
				a.refreshNotices()
			}
		case res := <-a.asyncCh:
			a.handleAsync(res)
		case <-tick.C:
			a.refreshView()
			a.refreshNotices()
			cols, rows := tty.Size()
			if cols != lastCols || rows != lastRows {
				lastCols, lastRows = cols, rows
				_ = a.svc.Resize(cols, rows)
			}
			if a.nativeInput.pending != nil && !pendingAt.IsZero() && time.Since(pendingAt) > 120*time.Millisecond {
				if data := a.nativeInput.flush(); len(data) > 0 {
					_ = a.svc.Write(target, data)
				}
				pendingAt = time.Time{}
			}
		}
	}
}

func readNative(r io.Reader, out chan<- []byte, errs chan<- error) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			select {
			case out <- chunk:
			case <-time.After(time.Second):
			}
		}
		if err != nil {
			select {
			case errs <- err:
			default:
			}
			close(out)
			return
		}
	}
}

func (a *App) leaveNative() {
	target := a.native
	if target != "" {
		a.svc.Detach(target)
	}
	a.native = ""
	a.nativeDetachBuf = nil
	a.nativeInput = nativeInputAssembler{}
	if a.nativeTTY != nil {
		_, _ = a.nativeTTY.File.WriteString(terminal.ResetOuterModes + terminal.ShowCursor)
		_ = a.nativeTTY.Close()
		a.nativeTTY = nil
	}
	a.resumeDashboard()
	if target != "" {
		a.setStatus("detached from "+string(target), false)
		a.refreshView()
	}
}

func (a *App) resumeDashboard() {
	if a.screen == nil {
		return
	}
	if err := a.screen.Resume(); err != nil {
		return
	}
	a.screen.Sync()
	a.resize()
}

// nativeDetach reports whether b returns to Duo and whether it is ordinary
// attached-agent input. Detach sequences are consumed rather than forwarded, so
// the agent never sees the chord that took the terminal back.
func (a *App) nativeDetach(b byte) (detach, forward bool) {
	if b == 0x1c || b == 0x1d { // legacy Ctrl+\ and Ctrl+]
		a.nativeDetachBuf = nil
		return true, false
	}
	a.nativeDetachBuf = append(a.nativeDetachBuf, b)
	if len(a.nativeDetachBuf) > 32 {
		a.nativeDetachBuf = a.nativeDetachBuf[1:]
	}
	if (b == 'u' || b == '~') && nativeDetachSequence(a.nativeDetachBuf) {
		a.nativeDetachBuf = nil
		return true, false
	}
	return false, true
}

// nativeInputChunk assembles bytes for the attached process and stops at the
// detach key. Bytes after it are dropped rather than replayed into Duo.
func (a *App) nativeInputChunk(input []byte) (forward []byte, consumed int, detach bool) {
	for _, b := range input {
		consumed++
		detach, send := a.nativeDetach(b)
		if send {
			forward = append(forward, a.nativeInput.add(b)...)
		}
		if detach {
			return forward, consumed, true
		}
	}
	return forward, consumed, false
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
