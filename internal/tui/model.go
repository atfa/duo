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
	"github.com/atfa/duo/internal/models"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/terminal"
	"github.com/atfa/duo/internal/transport"
	"github.com/atfa/duo/internal/workspace"
)

type entry struct {
	at      time.Time
	text    string
	error   bool
	warning bool
}

// paneCache memoizes the wrapped rendering of one pane. Rebuilding it parses
// every markdown block, so a frame that only advances a spinner or moves a
// scroll offset must reuse the previous result. `rev` is bumped on every
// append, so a truncated 200-entry history is still invalidated correctly.
type paneCache struct {
	lines []paneLine
	width int
	rev   int
	ts    bool
}

type App struct {
	coord   *coordinator.Coordinator
	state   *project.State
	tracker *harness.Tracker
	ws      workspace.Manager
	server  *transport.Server
	agents  *agent.Manager
	bus     *events.Bus
	journal *sessionstore.EventLog

	tty *terminal.TTY

	renderer *renderer

	width       int
	height      int
	input       []byte
	inputPos    int // byte offset at a UTF-8 rune boundary
	status      string
	statusError bool
	version     string
	view        viewMode
	helpOffset  int

	// Pane scroll state. An offset is the number of wrapped lines above the
	// pane bottom; `seen` is the wrapped line count observed last time, so the
	// offset can grow with new output and keep the view on the same content.
	austinOffset int
	tonyOffset   int
	duoOffset    int
	austinSeen   int
	tonySeen     int
	duoSeen      int
	austinNew    bool
	tonyNew      bool
	duoNew       bool

	// Composer history, recalled with Up/Down when the composer is a single
	// line. historyDraft keeps whatever was being typed before recall started.
	history      []string
	historyIdx   int
	historyDraft []byte

	// Model picker (Ctrl+M). The catalog is read once from `pi --list-models`;
	// the active model and thinking level are reported by each agent's bridge, so
	// the picker only displays what Pi confirms.
	modelTarget     protocol.AgentID
	modelFilter     []byte
	modelCursor     int
	models          []models.Model
	modelLoading    bool
	modelLoaded     bool
	modelErr        string
	modelCh         chan modelsResult
	currentModel    map[protocol.AgentID]string
	currentThinking map[protocol.AgentID]string

	showTimestamps bool
	detailOffset   int

	// hidePreview collapses the native work preview band (Ctrl+P). The band is
	// shown whenever the terminal is tall enough to keep usable panes.
	hidePreview bool

	austin []entry
	tony   []entry
	duo    []entry

	austinRev int
	tonyRev   int
	duoRev    int

	austinCache paneCache
	tonyCache   paneCache
	duoCache    paneCache

	frame int

	native          protocol.AgentID
	escBuf          []byte
	nativeDetachBuf []byte
	selection       paneSelection
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
	history []sessionstore.TUIEntry,
	journal *sessionstore.EventLog,
) *App {
	a := &App{coord: coord, state: state, tracker: tracker, ws: ws, server: server, agents: agents, bus: bus, version: version, journal: journal, historyIdx: -1,
		modelTarget: protocol.Austin, modelCh: make(chan modelsResult, 1),
		currentModel: map[protocol.AgentID]string{}, currentThinking: map[protocol.AgentID]string{}}
	for _, item := range history {
		a.restoreEntry(item)
	}
	return a
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
	// Model and thinking reports carry no free text; they update picker state
	// rather than adding a pane entry.
	if event.Kind == events.KindModel && event.Provider != "" && event.Model != "" {
		if a.currentModel == nil {
			a.currentModel = map[protocol.AgentID]string{}
		}
		a.currentModel[event.Agent] = event.Provider + "/" + event.Model
		return
	}
	if event.Kind == events.KindThinking && event.Thinking != "" {
		if a.currentThinking == nil {
			a.currentThinking = map[protocol.AgentID]string{}
		}
		a.currentThinking[event.Agent] = event.Thinking
		// Pi clamps the level to what the model supports, so report what it
		// settled on rather than what was requested.
		a.setStatus(fmt.Sprintf("%s thinking: %s", event.Agent, event.Thinking), false)
		return
	}
	text := strings.TrimSpace(event.Text)
	if text == "" {
		return
	}
	switch event.Kind {
	case events.KindAssistant:
		a.add(event.Agent, text)
	case events.KindPeer:
		sentDirection := "→"
		if event.Agent == protocol.Tony {
			sentDirection = "←"
		}
		a.add(event.Agent, fmt.Sprintf("%s %s: sent", sentDirection, event.Peer))
		direction := "←"
		if event.Peer == protocol.Tony {
			direction = "→"
		}
		a.add(event.Peer, fmt.Sprintf("%s From %s:\n%s", direction, event.Agent, text))
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
	case events.KindVerdict:
		// A verdict is a workflow outcome, not a failure: warn instead of error,
		// and keep the full report in the reporting agent's pane only. The Duo
		// pane gets a one-line summary rather than a second copy of the report.
		if event.Agent == protocol.Austin || event.Agent == protocol.Tony {
			a.addWarning(event.Agent, "VERIFY — issue found:\n"+text)
			a.add(protocol.Duo, fmt.Sprintf("VERIFY — %s reported issue_found; session returned to RUNNING", event.Agent))
		} else {
			a.addWarning(protocol.Duo, text)
		}
	default:
		if event.Agent == protocol.Austin || event.Agent == protocol.Tony {
			a.add(event.Agent, text)
		} else {
			a.add(protocol.Duo, text)
		}
	}
}

func (a *App) add(agent protocol.AgentID, text string) {
	a.addEntry(agent, text, false, false)
}

func (a *App) addError(agent protocol.AgentID, text string) {
	a.addEntry(agent, text, true, false)
}

// addWarning records a notable but non-failing entry, such as a verifier
// reporting issue_found. It renders in the status color, never as an error.
func (a *App) addWarning(agent protocol.AgentID, text string) {
	a.addEntry(agent, text, false, true)
}

func (a *App) addEntry(agent protocol.AgentID, text string, isError, isWarning bool) {
	item := entry{at: time.Now(), text: text, error: isError, warning: isWarning}
	a.appendEntry(agent, item)
	if a.journal != nil {
		a.journal.RecordTUIEntry(sessionstore.TUIEntry{Time: item.at, Pane: string(agent), Text: text, Error: isError, Warning: isWarning})
	}
}

func (a *App) restoreEntry(item sessionstore.TUIEntry) {
	agent := protocol.AgentID(item.Pane)
	if agent != protocol.Austin && agent != protocol.Tony && agent != protocol.Duo {
		return
	}
	a.appendEntry(agent, entry{at: item.Time, text: item.Text, error: item.Error, warning: item.Warning})
}

func (a *App) appendEntry(agent protocol.AgentID, item entry) {
	list := &a.duo
	switch agent {
	case protocol.Austin:
		list = &a.austin
		a.austinRev++
	case protocol.Tony:
		list = &a.tony
		a.tonyRev++
	default:
		a.duoRev++
	}
	*list = append(*list, item)
	if len(*list) > 200 {
		*list = append([]entry(nil), (*list)[len(*list)-200:]...)
	}
}

func (a *App) submit(ctx context.Context) {
	text := composerText(a.input)
	if text == "" {
		return
	}
	a.clearInput()
	a.pushHistory(text)
	if err := a.coord.SubmitUserTask(ctx, text); err != nil {
		a.setStatus(err.Error(), true)
		a.add(protocol.Duo, "ERROR: "+err.Error())
	} else {
		a.setStatus("sent to Austin", false)
	}
}

func composerText(input []byte) string { return strings.TrimSpace(string(input)) }

func (a *App) clearInput() {
	a.input = a.input[:0]
	a.inputPos = 0
}
