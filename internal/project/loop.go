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
	Goal         string                           `json:"goal,omitempty"`
	NextID       uint64                           `json:"nextId,omitempty"`
	Waiting      map[protocol.AgentID]LoopWait    `json:"waiting,omitempty"`
	Events       map[protocol.AgentID][]LoopEvent `json:"events,omitempty"`
	Approvals    map[protocol.AgentID]string      `json:"approvals,omitempty"`
	Acknowledged map[protocol.AgentID]string      `json:"acknowledged,omitempty"`
	Proposal     *LoopProposal                    `json:"proposal,omitempty"`
	Consultation *LoopConsultation                `json:"consultation,omitempty"`
	Finishing    bool                             `json:"finishing,omitempty"`
	Wakes        int                              `json:"wakes,omitempty"`
	Summary      string                           `json:"summary,omitempty"`
}

// LoopConsultation is one correlated request. Waiting stays true until Austin
// acknowledges the reply event, not merely until Tony sends an unrelated message.
type LoopConsultation struct {
	ID       string    `json:"id"`
	Question string    `json:"question"`
	AskedAt  time.Time `json:"askedAt"`
	Answer   string    `json:"answer,omitempty"`
	ReplyID  string    `json:"replyId,omitempty"`
	Waiting  bool      `json:"waiting"`
}

// LoopProposal is retained only to decode pre-v5 snapshots for migration.
type LoopProposal struct {
	ID      string           `json:"id"`
	Author  protocol.AgentID `json:"author"`
	Summary string           `json:"summary"`
}

type LoopWait struct {
	Pending bool      `json:"pending,omitempty"`
	Until   time.Time `json:"until,omitempty"`
	Reason  string    `json:"reason,omitempty"`
}
type LoopEvent struct {
	ID         string           `json:"id"`
	From       protocol.AgentID `json:"from,omitempty"`
	Text       string           `json:"text"`
	Kind       string           `json:"kind,omitempty"`
	QuestionID string           `json:"questionId,omitempty"`
	At         time.Time        `json:"at,omitempty"`
}

func cloneLoop(in LoopState) LoopState {
	out := in
	if in.Proposal != nil {
		proposal := *in.Proposal
		out.Proposal = &proposal
	}
	if in.Consultation != nil {
		q := *in.Consultation
		out.Consultation = &q
	}
	out.Acknowledged = make(map[protocol.AgentID]string, len(in.Acknowledged))
	for k, v := range in.Acknowledged {
		out.Acknowledged[k] = v
	}
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
func (s *State) loopEventLocked(to, from protocol.AgentID, text string) string {
	if s.loop.Events == nil {
		s.loop.Events = make(map[protocol.AgentID][]LoopEvent)
	}
	s.loop.NextID++
	s.loop.Events[to] = append(s.loop.Events[to], LoopEvent{ID: strconv.FormatUint(s.loop.NextID, 10), From: from, Text: text, At: time.Now().UTC()})
	return strconv.FormatUint(s.loop.NextID, 10)
}
func (s *State) LoopSend(from protocol.AgentID, text string) error {
	s.mu.Lock()
	if err := s.loopCheck(from); err != nil {
		s.mu.Unlock()
		return err
	}
	s.loopEventLocked(protocol.PeerOf(from), from, text)
	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return nil
}

// LoopEvents reads or acknowledges events without registering a wait. The last
// acknowledgement is retained so retrying a lost response is idempotent.
func (s *State) LoopEvents(id protocol.AgentID, ack string, through ...bool) ([]LoopEvent, error) {
	s.mu.Lock()
	if err := s.loopCheck(id); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if ack != "" && s.loop.Acknowledged[id] != ack {
		found := -1
		for i, e := range s.loop.Events[id] {
			if e.ID == ack {
				found = i
				break
			}
		}
		if found < 0 {
			s.mu.Unlock()
			return nil, fmt.Errorf("event %q does not belong to your pending inbox (only the latest acknowledgement can be retried)", ack)
		}
		if len(through) > 0 && through[0] {
			if q := s.loop.Consultation; id == protocol.Austin && q != nil {
				for _, e := range s.loop.Events[id][:found+1] {
					if e.ID == q.ReplyID {
						q.Waiting = false
					}
				}
			}
			s.loop.Events[id] = append([]LoopEvent(nil), s.loop.Events[id][found+1:]...)
		} else {
			s.loop.Events[id] = append(s.loop.Events[id][:found], s.loop.Events[id][found+1:]...)
		}
		if s.loop.Acknowledged == nil {
			s.loop.Acknowledged = make(map[protocol.AgentID]string)
		}
		s.loop.Acknowledged[id] = ack
		if q := s.loop.Consultation; id == protocol.Austin && q != nil && q.ReplyID == ack {
			q.Waiting = false
		}
	}
	events := append([]LoopEvent(nil), s.loop.Events[id]...)
	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return events, nil
}

// LoopWait registers intent only when the inbox is empty. Pending remains true
// until the adapter reports a settled turn; further work cancels that intent.
func (s *State) LoopWait(id protocol.AgentID, until time.Time, reason string) (bool, error) {
	s.mu.Lock()
	if err := s.loopCheck(id); err != nil {
		s.mu.Unlock()
		return false, err
	}
	if len(s.loop.Events[id]) > 0 {
		s.mu.Unlock()
		return false, nil
	}
	if !until.IsZero() && !until.After(time.Now()) {
		s.mu.Unlock()
		return false, fmt.Errorf("until must be in the future")
	}
	if s.loop.Waiting == nil {
		s.loop.Waiting = make(map[protocol.AgentID]LoopWait)
	}
	s.loop.Waiting[id] = LoopWait{Until: until, Reason: strings.TrimSpace(reason), Pending: true}
	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return true, nil
}

// LoopActivity observes the real turn boundary rather than treating a tool
// response as proof that the model stopped. No business decision lives here.
func (s *State) LoopActivity(id protocol.AgentID, activity protocol.ActivityType, tool string) {
	s.mu.Lock()
	if s.mode != ModeLoop || s.phase == PhaseDone {
		s.mu.Unlock()
		return
	}
	changed := false
	if s.loop.Finishing && id == protocol.Austin && activity == protocol.ActivityAgentSettled {
		s.phase = PhaseIntegrate
		changed = true
	}
	if w, ok := s.loop.Waiting[id]; ok {
		switch {
		case activity == protocol.ActivityAgentSettled && w.Pending:
			w.Pending = false
			s.loop.Waiting[id] = w
			changed = true
		case activity == protocol.ActivityAgentStart || (activity == protocol.ActivityToolStart && tool != "duo_wait"):
			delete(s.loop.Waiting, id)
			changed = true
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

// LoopCanAct is the pre-tool gate for adapters. No deadline, human continuation,
// ordinary advice or model restart silently bypasses a requested consultation.
func (s *State) LoopCanAct(id protocol.AgentID) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.mode != ModeLoop {
		return nil
	}
	if err := s.loopCheck(id); err != nil {
		return err
	}
	if id == protocol.Austin {
		if q := s.loop.Consultation; q != nil && q.Waiting {
			return fmt.Errorf("waiting for consultation %s; end your turn and process its matching reply with duo_events before further work", q.ID)
		}
		if s.loop.Finishing {
			return fmt.Errorf("completion registered; end your turn without further work")
		}
	}
	return nil
}

func (s *State) LoopAsk(id protocol.AgentID, question string) (string, error) {
	s.mu.Lock()
	if err := s.loopCheck(id); err != nil {
		s.mu.Unlock()
		return "", err
	}
	question = strings.TrimSpace(question)
	if id != protocol.Austin || question == "" {
		s.mu.Unlock()
		return "", fmt.Errorf("only Austin may ask a non-empty consultation")
	}
	if s.loop.Finishing {
		s.mu.Unlock()
		return "", fmt.Errorf("completion already registered")
	}
	if q := s.loop.Consultation; q != nil && q.Waiting {
		same, id := q.Question == question, q.ID
		s.mu.Unlock()
		if same {
			return id, nil
		}
		return "", fmt.Errorf("consultation %s is still pending", id)
	}
	qid := s.loopEventLocked(protocol.Tony, id, question)
	inbox := s.loop.Events[protocol.Tony]
	inbox[len(inbox)-1].Kind = "question"
	inbox[len(inbox)-1].QuestionID = qid
	s.loop.Consultation = &LoopConsultation{ID: qid, Question: question, AskedAt: time.Now().UTC(), Waiting: true}
	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return qid, nil
}

func (s *State) LoopReply(id protocol.AgentID, questionID, answer string) error {
	s.mu.Lock()
	if err := s.loopCheck(id); err != nil {
		s.mu.Unlock()
		return err
	}
	answer = strings.TrimSpace(answer)
	q := s.loop.Consultation
	if id != protocol.Tony || answer == "" || q == nil || q.ID != questionID {
		s.mu.Unlock()
		return fmt.Errorf("only Tony may answer the current consultation ID with a non-empty reply")
	}
	if q.Answer != "" {
		same := q.Answer == answer
		s.mu.Unlock()
		if same {
			return nil
		}
		return fmt.Errorf("consultation already answered; use duo_send for additional advice")
	}
	q.Answer = answer
	q.ReplyID = s.loopEventLocked(protocol.Austin, id, answer)
	inbox := s.loop.Events[protocol.Austin]
	inbox[len(inbox)-1].Kind = "reply"
	inbox[len(inbox)-1].QuestionID = questionID
	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return nil
}

// maxLoopInbox bounds the pending events one agent can accumulate. Step
// observations are the only unbounded source: a long-running owner completes many
// tools while the advisor is busy or disconnected, and without a bound the
// durable snapshot grows without limit and every observation rewrites the whole
// checkpoint. The bound is generous enough that a normal batch is never
// truncated, and only the oldest informational step events are shed — never a
// question, reply, human instruction or timer.
const maxLoopInbox = 100

// LoopObserve records completed owner actions without another model call. The
// coordinator dispatches these as a batch only when the advisor is idle.
//
// It reports whether an event was recorded and deliberately does not persist:
// observation events are informational, and a long-running owner can complete
// many tools in quick succession, so the coordinator coalesces a burst into one
// snapshot write on its one-second tick instead of writing the whole snapshot
// once per tool.
func (s *State) LoopObserve(tool, detail string) bool {
	s.mu.Lock()
	if s.mode != ModeLoop || s.phase == PhaseDone || s.loop.Finishing {
		s.mu.Unlock()
		return false
	}
	s.loopEventLocked(protocol.Tony, protocol.Austin, tool+": "+detail)
	inbox := s.loop.Events[protocol.Tony]
	inbox[len(inbox)-1].Kind = "step"
	s.trimStepInboxLocked(protocol.Tony)
	s.mu.Unlock()
	return true
}

// trimStepInboxLocked drops the oldest step observations once the inbox passes
// maxLoopInbox. Step events are informational, so shedding history is safe;
// questions, replies, human instructions and timers are never evicted.
func (s *State) trimStepInboxLocked(id protocol.AgentID) {
	inbox := s.loop.Events[id]
	if len(inbox) <= maxLoopInbox {
		return
	}
	excess := len(inbox) - maxLoopInbox
	kept := make([]LoopEvent, 0, maxLoopInbox)
	for _, e := range inbox {
		if excess > 0 && e.Kind == "step" {
			excess--
			continue
		}
		kept = append(kept, e)
	}
	s.loop.Events[id] = kept
}

// LoopWake counts automatic dispatches durably, across Duo restarts.
func (s *State) LoopWake(limit int) bool {
	s.mu.Lock()
	if s.loop.Wakes >= limit {
		s.mu.Unlock()
		return false
	}
	s.loop.Wakes++
	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return true
}
func (s *State) ResetLoopWakes() {
	s.mu.Lock()
	s.loop.Wakes = 0
	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
}

// LoopTimers turns elapsed deadlines into durable events before any wake is sent.
func (s *State) LoopTimers(now time.Time) {
	s.mu.Lock()
	changed := false
	if s.mode == ModeLoop && s.phase != PhaseDone {
		for id, w := range s.loop.Waiting {
			if !w.Until.IsZero() && !w.Until.After(now) {
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

// LoopFinish is the owner's stopping decision, not an advisor approval gate.
// DONE waits only for Austin's real turn boundary; Tony need not sign or settle,
// so finishing is always pending until LoopActivity observes that boundary.
func (s *State) LoopFinish(id protocol.AgentID, summary, approve, reject string) error {
	s.mu.Lock()
	if err := s.loopCheck(id); err != nil {
		s.mu.Unlock()
		return err
	}
	summary = strings.TrimSpace(summary)
	if id != protocol.Austin || summary == "" || approve != "" || reject != "" {
		s.mu.Unlock()
		return fmt.Errorf("only Austin finishes LOOP with summary; approval/rejection is no longer used")
	}
	if q := s.loop.Consultation; q != nil && q.Waiting {
		s.mu.Unlock()
		return fmt.Errorf("consultation %s must be answered and processed before finishing", q.ID)
	}
	for _, event := range s.loop.Events[protocol.Austin] {
		if event.Kind == "human" {
			s.mu.Unlock()
			return fmt.Errorf("process pending human input before finishing")
		}
	}
	s.loop.Summary = summary
	s.loop.Finishing = true
	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return nil
}

// LoopHuman queues an external operator event for both agents.
func (s *State) LoopHuman(text string) error {
	s.mu.Lock()
	if err := s.loopCheck(protocol.Austin); err != nil {
		s.mu.Unlock()
		return err
	}
	if s.loop.Goal == "" {
		s.loop.Goal = text
	}
	s.loop.Approvals = nil
	s.loop.Proposal = nil
	s.loop.Finishing = false
	if s.phase == PhaseIntegrate {
		s.phase = PhaseRunning
	}
	s.loop.Summary = ""
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		s.loopEventLocked(id, protocol.Duo, text)
		inbox := s.loop.Events[id]
		inbox[len(inbox)-1].Kind = "human"
	}
	snap, hook := s.snapshotLocked(), s.onChange
	s.mu.Unlock()
	notify(hook, snap)
	return nil
}

func hasLoopWait(s LoopState, id protocol.AgentID) bool {
	if id == protocol.Austin && s.Consultation != nil && s.Consultation.Waiting {
		return true
	}
	w, ok := s.Waiting[id]
	return ok && !w.Pending && len(s.Events[id]) == 0
}
