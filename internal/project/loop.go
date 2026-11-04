package project

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/atfa/duo/internal/protocol"
)

// LoopState is durable coordination state. Events remain until explicitly acknowledged.
type LoopState struct {
	NextID    uint64                           `json:"nextId,omitempty"`
	Waiting   map[protocol.AgentID]LoopWait    `json:"waiting,omitempty"`
	Events    map[protocol.AgentID][]LoopEvent `json:"events,omitempty"`
	Approvals map[protocol.AgentID]string      `json:"approvals,omitempty"`
	Summary   string                           `json:"summary,omitempty"`
}
type LoopWait struct {
	Until  time.Time `json:"until,omitempty"`
	Reason string    `json:"reason,omitempty"`
}
type LoopEvent struct {
	ID   string           `json:"id"`
	From protocol.AgentID `json:"from,omitempty"`
	Text string           `json:"text"`
}

func cloneLoop(in LoopState) LoopState {
	out := in
	out.Approvals = make(map[protocol.AgentID]string, len(in.Approvals))
	for k, v := range in.Approvals {
		out.Approvals[k] = v
	}
	out.Waiting = make(map[protocol.AgentID]LoopWait, len(in.Waiting))
	for k, v := range in.Waiting {
		out.Waiting[k] = v
	}
	out.Events = make(map[protocol.AgentID][]LoopEvent, len(in.Events))
	for k, v := range in.Events {
		out.Events[k] = append([]LoopEvent(nil), v...)
	}
	return out
}
func (s *State) loopCheck(id protocol.AgentID) error {
	if s.mode != ModeLoop {
		return ErrModeMismatch
	}
	if s.phase == PhaseDone {
		return ErrProjectDone
	}
	if id != protocol.Austin && id != protocol.Tony {
		return ErrUnknownAgent
	}
	return nil
}
func (s *State) loopEventLocked(to, from protocol.AgentID, text string) {
	if s.loop.Events == nil {
		s.loop.Events = make(map[protocol.AgentID][]LoopEvent)
	}
	s.loop.NextID++
	s.loop.Events[to] = append(s.loop.Events[to], LoopEvent{ID: strconv.FormatUint(s.loop.NextID, 10), From: from, Text: text})
}
func (s *State) LoopSend(from protocol.AgentID, text string) error {
	s.mu.Lock()
	if err := s.loopCheck(from); err != nil {
		s.mu.Unlock()
		return err
	}
	s.loop.Approvals = nil
	s.loopEventLocked(protocol.PeerOf(from), from, text)
	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return nil
}

// LoopWait atomically acknowledges an event and registers the next wait. Pending
// events are returned without being removed, making a lost response replayable.
func (s *State) LoopWait(id protocol.AgentID, until time.Time, reason string, ack string) ([]LoopEvent, error) {
	s.mu.Lock()
	if err := s.loopCheck(id); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if ack != "" {
		events := s.loop.Events[id]
		for i, e := range events {
			if e.ID == ack {
				s.loop.Events[id] = append(events[:i], events[i+1:]...)
				break
			}
		}
	}
	if s.loop.Waiting == nil {
		s.loop.Waiting = make(map[protocol.AgentID]LoopWait)
	}
	// Reading an already-pending event must not cancel a future deadline.
	if old, ok := s.loop.Waiting[id]; ok && until.IsZero() && len(s.loop.Events[id]) > 0 {
		until = old.Until
	}
	s.loop.Waiting[id] = LoopWait{Until: until, Reason: strings.TrimSpace(reason)}
	if !until.IsZero() && !until.After(time.Now()) {
		s.loop.Approvals = nil
		s.loopEventLocked(id, protocol.Duo, "Timer elapsed: "+reason)
		s.loop.Waiting[id] = LoopWait{Reason: strings.TrimSpace(reason)}
	}
	events := append([]LoopEvent(nil), s.loop.Events[id]...)
	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return events, nil
}

// LoopTimers turns elapsed deadlines into durable events before any wake is sent.
func (s *State) LoopTimers(now time.Time) {
	s.mu.Lock()
	changed := false
	if s.mode == ModeLoop && s.phase != PhaseDone {
		for id, w := range s.loop.Waiting {
			if !w.Until.IsZero() && !w.Until.After(now) {
				s.loop.Approvals = nil
				s.loopEventLocked(id, protocol.Duo, "Timer elapsed: "+w.Reason)
				w.Until = time.Time{}
				s.loop.Waiting[id] = w
				changed = true
			}
		}
	}
	if !changed {
		s.mu.Unlock()
		return
	}
	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
}
func (s *State) LoopFinish(id protocol.AgentID, summary string) (bool, error) {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return false, fmt.Errorf("summary is required")
	}
	s.mu.Lock()
	if err := s.loopCheck(id); err != nil {
		s.mu.Unlock()
		return false, err
	}
	if s.loop.Approvals == nil {
		s.loop.Approvals = make(map[protocol.AgentID]string)
	}
	s.loop.Approvals[id] = summary
	done := s.loop.Approvals[protocol.PeerOf(id)] == summary
	if done {
		s.loop.Summary = summary
		s.loop.Waiting = nil
		s.phase = PhaseDone
	} else {
		s.loopEventLocked(protocol.PeerOf(id), id, "Finish proposed; call duo_finish with this exact summary to agree: "+summary)
	}
	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return done, nil
}

// LoopHuman queues an external operator event for both agents.
func (s *State) LoopHuman(text string) error {
	s.mu.Lock()
	if err := s.loopCheck(protocol.Austin); err != nil {
		s.mu.Unlock()
		return err
	}
	s.loop.Approvals = nil
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		s.loopEventLocked(id, protocol.Duo, text)
	}
	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return nil
}

func hasLoopWait(s LoopState, id protocol.AgentID) bool { _, ok := s.Waiting[id]; return ok }
