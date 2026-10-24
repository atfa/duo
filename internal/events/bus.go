package events

import (
	"sync"
	"time"

	"github.com/atfa/duo/internal/protocol"
)

type Kind string

const (
	KindSystem    Kind = "system"
	KindAssistant Kind = "assistant"
	KindPeer      Kind = "peer"
	KindActivity  Kind = "activity"
	KindHarness   Kind = "harness"
	KindUser      Kind = "user"
	KindError     Kind = "error"
	// KindVerdict is a peer's structured review outcome (for example a Fast
	// issue_found). It is a normal workflow result, not a failure, so the TUI
	// shows it as a warning instead of an error.
	KindVerdict Kind = "verdict"
	// KindModel reports which Pi model an agent is actually running. It carries
	// Provider/Model rather than free text so the picker can mark the current
	// choice without parsing a string.
	KindModel Kind = "model"
	// KindThinking reports an agent's active Pi thinking level.
	KindThinking Kind = "thinking"
	// KindAttention is the one event that says a human must act now: a merge
	// conflict, a result that could not be delivered, a Fast verification nobody
	// answers, Austin's finished report. It carries a reason code rather than prose
	// because a frontend localises the text and a desktop notification has to fit on
	// one line. The prose an agent reads is a separate notice, unchanged: this event
	// is an output projection, not a new source of workflow state.
	KindAttention Kind = "attention"
)

// Attention reasons are stable identifiers a frontend maps to localised text. They
// name the four situations where a Duo session stops until a human acts, which is
// exactly the list a notification is allowed to interrupt someone for.
const (
	// AttentionConflict: a merge produced conflicts, and the session cannot move
	// until they are resolved.
	AttentionConflict = "conflict"
	// AttentionDelivery: approval is complete but the result could not be applied
	// to the user's repository, so a human has to fix it and run `duo apply`.
	AttentionDelivery = "delivery"
	// AttentionVerification: Fast mode is waiting for Tony's verdict, which a human
	// has to unblock when Tony is not answering.
	AttentionVerification = "verification"
	// AttentionSummary: Austin's final report for the human has arrived, after
	// delivery, when nobody is watching the dashboard any more.
	AttentionSummary = "summary"
)

// ConnectionEvent marks the system event a bridge attach or detach produces. The
// zero value means "not a connection event", so every other event is unchanged.
// A frontend that attached after the event reads the same fact from the session
// view, and this marker is what lets it recognise the event it missed as the
// connection it already announced instead of printing it a second time.
type ConnectionEvent int

const (
	ConnectionNone ConnectionEvent = iota
	ConnectionUp
	ConnectionDown
)

type Event struct {
	Time  time.Time
	Kind  Kind
	Agent protocol.AgentID
	Peer  protocol.AgentID
	Text  string

	// Connection is set on the system line an agent's bridge attach or detach
	// produces, and is ConnectionNone everywhere else.
	Connection ConnectionEvent

	// Provider and Model are set for KindModel events.
	Provider string
	Model    string

	// Thinking is set for KindThinking events.
	Thinking string

	// Attention is the reason code of a KindAttention event: one of the
	// Attention* constants, never free text.
	Attention string
}

type Bus struct {
	mu   sync.RWMutex
	subs map[chan Event]struct{}
}

func NewBus() *Bus { return &Bus{subs: make(map[chan Event]struct{})} }

func (b *Bus) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer < 1 {
		buffer = 64
	}
	ch := make(chan Event, buffer)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	cancel := func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
	return ch, cancel
}

func (b *Bus) Emit(event Event) {
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- event:
		default:
		}
	}
}
