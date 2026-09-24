package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/coordinator"
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/terminal"
	"github.com/atfa/duo/internal/transport"
	"github.com/atfa/duo/internal/workspace"
)

type entry struct {
	at    time.Time
	text  string
	error bool
}

type App struct {
	coord   *coordinator.Coordinator
	state   *project.State
	tracker *harness.Tracker
	ws      workspace.Manager
	server  *transport.Server
	agents  *agent.Manager
	bus     *events.Bus

	tty *terminal.TTY

	renderer *renderer

	width       int
	height      int
	input       []byte
	status      string
	statusError bool
	version     string
	view        viewMode
	helpOffset  int

	austin []entry
	tony   []entry
	duo    []entry
	frame  int

	native          protocol.AgentID
	escBuf          []byte
	nativeDetachBuf []byte
}

func New(
	coord *coordinator.Coordinator,
	state *project.State,
	tracker *harness.Tracker,
	ws workspace.Manager,
	server *transport.Server,
	agents *agent.Manager,
	bus *events.Bus,
	version string,
) *App {
	return &App{coord: coord, state: state, tracker: tracker, ws: ws, server: server, agents: agents, bus: bus, version: version}
}

func (a *App) setStatus(text string, isError bool) {
	a.status, a.statusError = text, isError
}

// markDirty schedules a frame for the next renderer tick.
func (a *App) markDirty() {
	if a.renderer != nil {
		a.renderer.markDirty(false)
	}
}

// requestFullClear schedules a frame that first clears the whole screen. It is
// required whenever the previous frame may no longer cover the terminal
// (resize, layout change, alternate-screen re-entry).
func (a *App) requestFullClear() {
	if a.renderer != nil {
		a.renderer.markDirty(true)
	}
}

// spinnerActive reports whether either agent is doing animated work.
func (a *App) spinnerActive() bool {
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		rt := a.tracker.Snapshot(id)
		if rt.Busy || rt.ProviderActive || rt.ToolDepth > 0 {
			return true
		}
	}
	return false
}

// spinnerTick advances the spinner only while work is visible, so an idle Duo
// does not repaint periodically.
func (a *App) spinnerTick() bool {
	if a.view != viewMain || !a.spinnerActive() {
		return false
	}
	a.frame++
	a.markDirty()
	return true
}

func (a *App) route(event events.Event) {
	text := strings.TrimSpace(event.Text)
	if text == "" {
		return
	}
	switch event.Kind {
	case events.KindAssistant:
		a.add(event.Agent, text)
	case events.KindPeer:
		a.add(event.Agent, fmt.Sprintf("→ %s: sent", event.Peer))
		a.add(event.Peer, fmt.Sprintf("← %s: %s", event.Agent, text))
	case events.KindUser:
		a.add(protocol.Duo, "Human → Austin: "+text)
	case events.KindHarness:
		a.add(protocol.Duo, "Harness: "+text)
	case events.KindError:
		if event.Agent == protocol.Austin || event.Agent == protocol.Tony {
			a.addError(event.Agent, "ERROR: "+text)
		}
		label := "ERROR"
		if event.Agent != "" && event.Agent != protocol.Duo {
			label += " " + string(event.Agent)
		}
		a.addError(protocol.Duo, fmt.Sprintf("%s: %s", label, text))
	default:
		if event.Agent == protocol.Austin || event.Agent == protocol.Tony {
			a.add(event.Agent, text)
		} else {
			a.add(protocol.Duo, text)
		}
	}
}

func (a *App) add(agent protocol.AgentID, text string) {
	a.addEntry(agent, text, false)
}

func (a *App) addError(agent protocol.AgentID, text string) {
	a.addEntry(agent, text, true)
}

func (a *App) addEntry(agent protocol.AgentID, text string, isError bool) {
	list := &a.duo
	if agent == protocol.Austin {
		list = &a.austin
	}
	if agent == protocol.Tony {
		list = &a.tony
	}
	*list = append(*list, entry{at: time.Now(), text: text, error: isError})
	if len(*list) > 200 {
		*list = append([]entry(nil), (*list)[len(*list)-200:]...)
	}
}

func (a *App) submit(ctx context.Context) {
	text := strings.TrimSpace(string(a.input))
	if text == "" {
		return
	}
	a.input = a.input[:0]
	if err := a.coord.SubmitUserTask(ctx, text); err != nil {
		a.setStatus(err.Error(), true)
		a.add(protocol.Duo, "ERROR: "+err.Error())
	} else {
		a.setStatus("sent to Austin", false)
	}
}
