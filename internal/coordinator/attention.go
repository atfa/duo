package coordinator

import (
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/protocol"
)

// attention says out loud that the session is waiting on a human.
//
// The four reasons are the situations where Duo cannot continue by itself: a merge
// conflict, a result that could not be applied, a Fast verification nobody answers,
// and the final report Austin writes after delivery. Each of them already produces
// the prose an agent reads; this event is the projection a frontend turns into a
// desktop notification, so the workflow state stays exactly where it was.
func (c *Coordinator) attention(reason string) {
	if c.bus == nil {
		return
	}
	c.bus.Emit(events.Event{Kind: events.KindAttention, Agent: protocol.Duo, Attention: reason})
}
