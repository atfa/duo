package tui

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/terminal"
	"github.com/atfa/duo/internal/workspace"
)

// startupMessage announces the session mode, because the two modes ask for very
// different things from the human and from the agents. It also names the
// repository and session so the target of a run is visible without opening Help.
func (a *App) startupMessage() string {
	set := workspace.Set{}
	if a.ws != nil {
		set = a.ws.Set()
	}
	where := fmt.Sprintf("\nRepository: %s (branch %s) · Session: %s", set.Repository, set.BaseBranch, set.Session)
	if a.state.Snapshot().EffectiveMode() == project.ModeFast {
		return "Duo " + a.version + " ready (FAST mode). Type a task and press Enter; Austin drives while Tony independently verifies before Duo delivers the result." + where
	}
	return "Duo " + a.version + " ready (GOAL mode). Type a task and press Enter; Austin will wake Tony when collaboration is needed." + where
}

// announceRunningAgents reports the agents that are already up when the timeline
// starts, for drivers that cannot announce themselves over the bridge.
//
// This is derived from state rather than from the start event on purpose. Both
// agents are launched before the TUI subscribes to the bus, so a "connected"
// notice emitted at process start reaches nobody. agy has no bridge at all and
// opencode's only attaches once its TUI has a session, so for those drivers this
// is the only moment the human is told the agent is there.
func (a *App) announceRunningAgents() {
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		if agent.SelfReportsOnLaunch(a.driverName(id)) {
			continue
		}
		if a.processState(id) != agent.ProcessRunning {
			continue
		}
		a.add(id, fmt.Sprintf("%s connected", id))
	}
}

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
	_, _ = tty.File.WriteString(terminal.EnterAltScreen + terminal.HideCursor + terminal.MouseOn + terminal.ModifyOtherKeysOn + terminal.BracketedPasteOn + terminal.ClearHome)
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
	var escTimer <-chan time.Time
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()

	a.add(protocol.Duo, a.startupMessage())
	a.announceRunningAgents()
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
			if len(a.escBuf) > 0 {
				escTimer = time.After(25 * time.Millisecond)
			} else {
				escTimer = nil
			}
			if a.applyAction(ctx, action) {
				return nil
			}
			if action.kind == actionNone && len(a.escBuf) == 0 {
				a.markDirty()
			}
		case <-winch:
			a.resize()
		case <-escTimer:
			escTimer = nil
			if a.applyAction(ctx, a.handleEscapeTimeout()) {
				return nil
			}
		case <-a.renderer.dueChan():
			a.renderer.flush()
		case res := <-a.modelCh:
			a.applyModelList(res)
		case <-tick.C:
			a.spinnerTick()
		}
	}
}

// applyAction is the one place TUI-mode actions are applied. Native Pi input
// bypasses it entirely in Run so Pi keeps ownership of its Ctrl+/ handling.
func (a *App) applyAction(ctx context.Context, action inputAction) bool {
	switch action.kind {
	case actionNone:
		return false
	case actionQuit:
		return true
	case actionToggleHelp:
		if a.view == viewHelp {
			a.view = viewMain
		} else {
			a.view = viewHelp
			a.clampHelpOffset()
		}
		a.requestFullClear()
	case actionToggleDetail:
		if a.view == viewDetail {
			a.view = viewMain
		} else {
			a.view = viewDetail
			a.refreshChanges()
			a.clampDetailOffset()
		}
		a.requestFullClear()
	case actionToggleTimestamps:
		a.showTimestamps = !a.showTimestamps
		if a.showTimestamps {
			a.setStatus("timestamps on", false)
		} else {
			a.setStatus("timestamps off", false)
		}
		a.markDirty()
	case actionCloseHelp:
		a.view = viewMain
		a.requestFullClear()
	case actionScrollUp:
		a.scrollOverlay(-1)
		a.markDirty()
	case actionScrollDown:
		a.scrollOverlay(1)
		a.markDirty()
	case actionPageUp:
		a.scrollOverlay(-a.helpVisibleRows())
		a.markDirty()
	case actionPageDown:
		a.scrollOverlay(a.helpVisibleRows())
		a.markDirty()
	case actionHome:
		a.overlayTop()
		a.markDirty()
	case actionEnd:
		a.overlayBottom()
		a.markDirty()
	case actionScrollPane:
		a.scrollPane(action.agent, action.delta)
		a.markDirty()
	case actionAttach:
		if action.agent != "" {
			if err := a.enterNative(action.agent); err != nil {
				a.add(protocol.Duo, "ERROR: "+err.Error())
				a.setStatus(err.Error(), true)
				a.markDirty()
			}
		}
	case actionSelectStart:
		a.startSelection(action.mouse)
		a.markDirty()
	case actionSelectMove:
		if a.moveSelection(action.mouse) {
			a.markDirty()
		}
	case actionSelectEnd:
		if a.moveSelection(action.mouse) && a.selection.moved {
			text := a.selectedText()
			a.selection.active = false
			if text == "" {
				a.setStatus("nothing selected", false)
			} else if err := a.copyClipboard(text); err != nil {
				a.setStatus("copy failed: "+err.Error(), true)
			} else {
				a.setStatus("copied selection", false)
			}
			a.markDirty()
		} else {
			a.selection = paneSelection{}
			a.markDirty()
		}
	case actionRestart:
		if err := a.agents.Restart(ctx, action.agent); err != nil {
			a.setStatus(err.Error(), true)
			a.addError(protocol.Duo, err.Error())
		} else {
			a.tracker.Reset(action.agent)
			a.setStatus(string(action.agent)+" restarted", false)
		}
		a.markDirty()
	case actionSubmit:
		if a.submit(ctx) {
			return true
		}
		a.markDirty()
	case actionApplyModel:
		a.applySelectedModel(ctx, false)
		a.markDirty()
	case actionApplyModelKeepOpen:
		a.applySelectedModel(ctx, true)
		a.markDirty()
	case actionCycleThinking:
		if err := a.coord.CycleThinking(ctx, a.modelTarget); err != nil {
			a.setStatus(err.Error(), true)
		} else {
			thinking := a.coord.Thinking(a.modelTarget)
			if thinking == "" {
				thinking = "unknown"
			}
			if a.agents != nil {
				if d, ok := a.agents.Driver(a.modelTarget); ok {
					d.SetEffort(thinking)
					if needsRestartForModel(d.DriverType()) {
						// The effort only reaches a non-pi agent as a startup flag,
						// and RestartRunning stops before it starts, so a failure
						// here leaves the agent dead. Report it rather than
						// claiming the new level took effect.
						if err := d.RestartRunning(ctx); err != nil {
							// Report the failure and skip the success line;
							// returning true here would quit the TUI. The level
							// is recorded only once the agent has it, so the
							// picker header cannot advertise one it never got.
							a.setStatus(fmt.Sprintf("%s thinking: %s failed: %v", a.modelTarget, thinking, err), true)
							a.markDirty()
							return false
						}
					}
				}
			}
			if a.currentThinking == nil {
				a.currentThinking = make(map[protocol.AgentID]string)
			}
			a.currentThinking[a.modelTarget] = thinking
			a.setStatus(fmt.Sprintf("%s thinking: %s", a.modelTarget, thinking), false)
		}
		a.markDirty()
	}
	return false
}

func (a *App) copyClipboard(text string) error {
	var osc52Err error
	if a.tty != nil && a.tty.File != nil {
		_, osc52Err = a.tty.File.WriteString(osc52Sequence(text))
	}

	var sysErr error
	switch runtime.GOOS {
	case "darwin":
		cmd := exec.Command("pbcopy")
		cmd.Stdin = bytes.NewBufferString(text)
		sysErr = cmd.Run()
	case "linux":
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			cmd := exec.Command("wl-copy")
			cmd.Stdin = bytes.NewBufferString(text)
			sysErr = cmd.Run()
		} else {
			cmd := exec.Command("xclip", "-selection", "clipboard")
			cmd.Stdin = bytes.NewBufferString(text)
			sysErr = cmd.Run()
			if sysErr != nil {
				cmd2 := exec.Command("xsel", "--clipboard", "--input")
				cmd2.Stdin = bytes.NewBufferString(text)
				sysErr = cmd2.Run()
			}
		}
	}

	if osc52Err == nil || sysErr == nil {
		return nil
	}
	return sysErr
}

func osc52Sequence(text string) string {
	b64 := base64.StdEncoding.EncodeToString([]byte(text))
	if os.Getenv("TMUX") != "" {
		return fmt.Sprintf("\x1bPtmux;\x1b\x1b]52;c;%s\x07\x1b\\", b64)
	}
	if strings.HasPrefix(os.Getenv("TERM"), "screen") {
		return fmt.Sprintf("\x1bP\x1b]52;c;%s\x07\x1b\\", b64)
	}
	return fmt.Sprintf("\x1b]52;c;%s\x07", b64)
}

// resize applies the latest host terminal size to the agent PTYs and schedules
// one full-clear frame. Any intermediate SIGWINCH sizes are intentionally
// dropped; only the most recent size matters.
func (a *App) resize() {
	w, h := a.tty.Size()
	a.width, a.height = w, h
	a.clampOffsets()
	if err := a.agents.ResizeAll(w, h); err != nil {
		a.bus.Emit(events.Event{Kind: events.KindError, Agent: protocol.Duo, Text: err.Error()})
	}
	a.requestFullClear()
}

func (a *App) syncSize() {
	w, h := a.tty.Size()
	a.width, a.height = w, h
	if err := a.agents.ResizeAll(w, h); err != nil {
		a.setStatus(err.Error(), true)
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
	_, _ = a.tty.File.WriteString(terminal.MouseOff + terminal.BracketedPasteOff + terminal.ShowCursor + terminal.ExitAltScreen + terminal.ClearHome)
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
	_, _ = a.tty.File.WriteString(terminal.ResetOuterModes + terminal.EnterAltScreen + terminal.HideCursor + terminal.MouseOn + terminal.ModifyOtherKeysOn + terminal.BracketedPasteOn + terminal.ClearHome)
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
