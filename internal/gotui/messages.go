package gotui

import (
	"fmt"
	"strings"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
)

func (a *App) route(event events.Event) {
	// A timeline entry event names a line this interface has already drawn: it
	// records an entry when it draws one. Drawing the event again would duplicate
	// that entry. The event exists so a second frontend shows the same line.
	if event.Kind == events.KindTimeline {
		return
	}
	// An attention event carries a reason code and no prose: it exists to reach the
	// human over the terminal, not to join the conversation.
	if event.Kind == events.KindAttention {
		a.notifyHuman(event.Attention)
		return
	}
	if event.Kind == events.KindModel && event.Model != "" {
		ref := event.Model
		if event.Provider != "" && !a.live.Agent(event.Agent).Manifest.Bare() && !strings.HasPrefix(ref, event.Provider+"/") {
			ref = event.Provider + "/" + ref
		}
		a.currentModel[event.Agent] = ref
		return
	}
	if event.Kind == events.KindThinking && event.Thinking != "" {
		a.currentThink[event.Agent] = event.Thinking
		if a.overlay == overlayPicker {
			a.picker.thinking = event.Thinking
		}
		return
	}
	// A connection event and the session view describe the same fact. The view is
	// the one the startup announcement reads, so an agent already announced from it
	// must not be announced again by the event that preceded it.
	switch event.Connection {
	case events.ConnectionUp:
		if a.announcedConnected[event.Agent] {
			return
		}
		a.announcedConnected[event.Agent] = true
	case events.ConnectionDown:
		delete(a.announcedConnected, event.Agent)
	}
	text := strings.TrimSpace(event.Text)
	if text == "" {
		return
	}
	switch event.Kind {
	case events.KindAssistant:
		label := directionLabel(event.Agent, "")
		if event.Summary {
			a.addSummary(event.Agent, label, text)
			break
		}
		a.addLabeled(event.Agent, label, text, false, false)
	case events.KindPeer:
		a.addLabeled(event.Agent, directionLabel(event.Agent, event.Peer), text, false, false)
	case events.KindUser:
		target := event.Peer
		if target == "" {
			target = protocol.Austin
		}
		a.addLabeled(protocol.Duo, directionLabel("Human", target), text, false, false)
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

func (a *App) addLabeled(agent protocol.AgentID, label, text string, isError, isWarning bool) {
	a.addEntryItem(agent, label, text, isError, isWarning, false)
}

// addSummary adds the report Duo asked Austin for at DONE. Only the coordinator
// can say which message that is, so the timeline never guesses it from the text.
func (a *App) addSummary(agent protocol.AgentID, label, text string) {
	a.addEntryItem(agent, label, text, false, false, true)
}

func (a *App) addEntryItem(agent protocol.AgentID, label, text string, isError, isWarning, summary bool) {
	item := entry{at: time.Now(), pane: string(agent), text: text, err: isError, warn: isWarning, label: label, summary: summary}
	a.appendEntry(item)
	if a.svc != nil {
		a.svc.RecordEntry(sessionstore.TUIEntry{Time: item.at, Pane: item.pane, Text: item.text, Error: isError, Warning: isWarning, Label: label, Summary: summary})
	}
}

func (a *App) lastAustinToHuman() (string, bool) {
	human := directionLabel(protocol.Austin, "")
	for i := len(a.entries) - 1; i >= 0; i-- {
		e := a.entries[i]
		if e.pane != string(protocol.Austin) {
			continue
		}
		if e.label != human && e.label != "" {
			continue
		}
		if strings.TrimSpace(e.text) == "" {
			continue
		}
		return e.text, true
	}
	return "", false
}
