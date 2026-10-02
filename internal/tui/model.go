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
	// label is the conversation header for this entry, a directed pair such as
	// "Austin → Tony" or "Duo → Human". It is empty only for plain notices,
	// which fall back to the speaker and the human in the timeline.
	label string
	// seq is a global append order. The conversation timeline merges three
	// per-speaker lists and must reproduce true arrival order even when two
	// entries share the same clock value.
	seq int
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
	// logs mirrors the journal into Markdown transcripts under the main
	// repository's .duo/logs, so a finished session is readable without
	// hunting for its journal or its worktrees.
	logs *sessionstore.LogWriter

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

	// Slash command autocomplete menu.
	slashCursor    int
	slashDismissed bool

	// Model picker (Ctrl+M). The catalog is read from the agent's driver CLI;
	// the active model and thinking level are reported by each agent's bridge, so
	// the picker only displays what the driver confirms.
	modelTarget         protocol.AgentID
	modelFilter         []byte
	modelCursor         int
	models              []models.Model
	modelLoading        bool
	modelLoaded         bool
	modelErr            string
	modelCh             chan modelsResult
	modelsByAgent       map[protocol.AgentID][]models.Model
	modelLoadedByAgent  map[protocol.AgentID]bool
	modelLoadingByAgent map[protocol.AgentID]bool
	modelErrByAgent     map[protocol.AgentID]string
	currentModel        map[protocol.AgentID]string
	currentThinking     map[protocol.AgentID]string

	showTimestamps bool
	detailOffset   int
	cachedChanges  []string

	// hidePreview collapses the native work preview band (Ctrl+P). The band is
	// shown whenever the terminal is tall enough to keep usable panes.
	hidePreview bool

	warnedSqlite3 bool

	austin []entry
	tony   []entry
	duo    []entry

	austinRev int
	tonyRev   int
	duoRev    int

	austinCache paneCache
	tonyCache   paneCache
	duoCache    paneCache

	// timeline is the main-view layout: one chronological conversation instead
	// of two side-by-side panes. New() enables it; every pane helper branches on
	// it so the split layout stays available as a fallback.
	timeline      bool
	nextSeq       int
	timelineRev   int
	timelineCache paneCache

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
	logs *sessionstore.LogWriter,
) *App {
	a := &App{coord: coord, state: state, tracker: tracker, ws: ws, server: server, agents: agents, bus: bus, version: version, journal: journal, logs: logs, historyIdx: -1,
		history:        loadComposerHistory(),
		modelTarget:    protocol.Austin, modelCh: make(chan modelsResult, 1),
		modelsByAgent:       map[protocol.AgentID][]models.Model{},
		modelLoadedByAgent:  map[protocol.AgentID]bool{},
		modelLoadingByAgent: map[protocol.AgentID]bool{},
		modelErrByAgent:     map[protocol.AgentID]string{},
		timeline:            true,
		showTimestamps:      true,
		currentModel:        map[protocol.AgentID]string{}, currentThinking: map[protocol.AgentID]string{}}
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
	if !a.warnedSqlite3 && a.driverName(event.Agent) == "agy" {
		a.warnSqlite3Once()
	}
	// Model and thinking reports carry no free text; they update picker state
	// rather than adding a pane entry.
	if event.Kind == events.KindModel && event.Model != "" {
		if a.currentModel == nil {
			a.currentModel = map[protocol.AgentID]string{}
		}
		ref := event.Model
		if event.Provider != "" && event.Provider != "agy" && !strings.HasPrefix(ref, event.Provider+"/") {
			ref = event.Provider + "/" + ref
		}
		a.currentModel[event.Agent] = ref
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
		a.addLabeled(event.Agent, directionLabel(event.Agent, ""), text, false, false)
	case events.KindPeer:
		// One entry per message, recorded on the sender's side and labeled
		// "Sender → Receiver". The speaker's own block carries the message, so
		// neither layout needs the old "sent"/"From" hint lines that restated
		// the same exchange on both sides.
		a.addLabeled(event.Agent, directionLabel(event.Agent, event.Peer), text, false, false)
	case events.KindUser:
		a.addLabeled(protocol.Duo, "Human → Austin", text, false, false)
	case events.KindHarness:
		a.addLabeled(protocol.Duo, directionLabel(protocol.Duo, ""), "Harness: "+text, false, false)
	case events.KindError:
		if event.Agent == protocol.Austin || event.Agent == protocol.Tony {
			a.addLabeled(event.Agent, directionLabel(event.Agent, ""), "ERROR: "+text, true, false)
		}
		label := "ERROR"
		if event.Agent != "" && event.Agent != protocol.Duo {
			label += " " + string(event.Agent)
		}
		a.addLabeled(protocol.Duo, directionLabel(protocol.Duo, ""), fmt.Sprintf("%s: %s", label, text), true, false)
	case events.KindVerdict:
		// A verdict is a workflow outcome, not a failure: warn instead of error,
		// and keep the full report in the reporting agent's pane only. The Duo
		// pane gets a one-line summary rather than a second copy of the report.
		if event.Agent == protocol.Austin || event.Agent == protocol.Tony {
			a.addLabeled(event.Agent, directionLabel(event.Agent, event.Peer), "VERIFY — issue found:\n"+text, false, true)
			a.addLabeled(protocol.Duo, directionLabel(protocol.Duo, ""), fmt.Sprintf("VERIFY — %s reported issue_found; session returned to RUNNING", event.Agent), false, false)
		} else {
			a.addLabeled(protocol.Duo, directionLabel(protocol.Duo, ""), text, false, true)
		}
	default:
		if event.Agent == protocol.Austin || event.Agent == protocol.Tony {
			a.addLabeled(event.Agent, directionLabel(event.Agent, event.Peer), text, false, false)
		} else {
			a.addLabeled(protocol.Duo, directionLabel(protocol.Duo, event.Peer), text, false, false)
		}
	}
}

// directionLabel renders a timeline header as a directed message: the speaker
// and its target. A peer names the target; without one the message is addressed
// to the human, so a header is never a bare agent name.
func directionLabel(from, to protocol.AgentID) string {
	if to == "" {
		to = "Human"
	}
	return fmt.Sprintf("%s → %s", from, to)
}

func (a *App) add(agent protocol.AgentID, text string) {
	a.addEntry(agent, text, false, false)
}

func (a *App) addError(agent protocol.AgentID, text string) {
	a.addEntry(agent, text, true, false)
}

func (a *App) addEntry(agent protocol.AgentID, text string, isError, isWarning bool) {
	a.addLabeled(agent, "", text, isError, isWarning)
}

// addLabeled records an entry with an optional timeline header, so the main
// view can attribute a message without parsing its text.
func (a *App) addLabeled(agent protocol.AgentID, label, text string, isError, isWarning bool) {
	item := entry{at: time.Now(), text: text, error: isError, warning: isWarning, label: label}
	a.appendEntry(agent, item)
	// One funnel for everything the interface shows, so the journal and the
	// Markdown transcripts cannot drift apart.
	if a.journal != nil || a.logs != nil {
		recorded := sessionstore.TUIEntry{Time: item.at, Pane: string(agent), Text: text, Error: isError, Warning: isWarning, Label: label}
		if a.journal != nil {
			a.journal.RecordTUIEntry(recorded)
		}
		a.logs.Append(recorded)
	}
}

func (a *App) restoreEntry(item sessionstore.TUIEntry) {
	agent := protocol.AgentID(item.Pane)
	if agent != protocol.Austin && agent != protocol.Tony && agent != protocol.Duo {
		return
	}
	label := item.Label
	switch protocol.AgentID(label) {
	case protocol.Austin, protocol.Tony, protocol.Duo:
		// Older journals recorded the bare speaker; headers are directed pairs
		// now, so a resumed session reads like a new one.
		label = directionLabel(protocol.AgentID(label), "")
	}
	a.appendEntry(agent, entry{at: item.Time, text: item.Text, error: item.Error, warning: item.Warning, label: label})
}

func (a *App) appendEntry(agent protocol.AgentID, item entry) {
	a.nextSeq++
	item.seq = a.nextSeq
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
	a.timelineRev++
	*list = append(*list, item)
	if len(*list) > 200 {
		*list = append([]entry(nil), (*list)[len(*list)-200:]...)
	}
}

func (a *App) submit(ctx context.Context) bool {
	text := composerText(a.input)
	if text == "" {
		return false
	}
	a.clearInput()
	a.pushHistory(text)
	if a.runSlashCommand(ctx, text) {
		return text == "/quit" || text == "/exit"
	}
	if a.coord == nil {
		return false
	}
	if err := a.coord.SubmitUserTask(ctx, text); err != nil {
		a.setStatus(err.Error(), true)
		a.add(protocol.Duo, "ERROR: "+err.Error())
	} else {
		a.setStatus("sent to Austin", false)
	}
	return false
}

func composerText(input []byte) string { return strings.TrimSpace(string(input)) }

func (a *App) clearInput() {
	a.input = a.input[:0]
	a.inputPos = 0
	a.slashCursor = 0
	a.slashDismissed = false
}
