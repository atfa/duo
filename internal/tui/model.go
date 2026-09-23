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
	at   time.Time
	text string
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

	width  int
	height int
	input  []byte
	status string

	austin []entry
	tony   []entry
	duo    []entry

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
) *App {
	return &App{coord: coord, state: state, tracker: tracker, ws: ws, server: server, agents: agents, bus: bus}
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
		a.add(event.Agent, fmt.Sprintf("→ %s: %s", event.Peer, text))
		a.add(event.Peer, fmt.Sprintf("← %s: %s", event.Agent, text))
	case events.KindUser:
		a.add(protocol.Duo, "Human → Austin: "+text)
	case events.KindHarness:
		a.add(protocol.Duo, "Harness: "+text)
	case events.KindError:
		a.add(protocol.Duo, "ERROR: "+text)
	default:
		if event.Agent == protocol.Austin || event.Agent == protocol.Tony {
			a.add(event.Agent, text)
		} else {
			a.add(protocol.Duo, text)
		}
	}
}

func (a *App) add(agent protocol.AgentID, text string) {
	list := &a.duo
	if agent == protocol.Austin {
		list = &a.austin
	}
	if agent == protocol.Tony {
		list = &a.tony
	}
	*list = append(*list, entry{at: time.Now(), text: text})
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
		a.status = err.Error()
		a.add(protocol.Duo, "ERROR: "+err.Error())
	} else {
		a.status = "sent to Austin"
	}
}
