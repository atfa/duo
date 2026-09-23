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
)

type Event struct {
	Time  time.Time
	Kind  Kind
	Agent protocol.AgentID
	Peer  protocol.AgentID
	Text  string
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
