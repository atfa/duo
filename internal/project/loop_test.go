package project

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/protocol"
)

func TestLoopInboxAndWaitBoundaries(t *testing.T) {
	s := NewStateFor(ModeLoop)
	deadline := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if ok, err := s.LoopWait(protocol.Tony, deadline, "observe"); err != nil || !ok {
		t.Fatalf("wait=%t %v", ok, err)
	}
	if hasLoopWait(s.Snapshot().Loop, protocol.Tony) {
		t.Fatal("tool response counted as settled")
	}
	s.LoopActivity(protocol.Tony, protocol.ActivityAgentSettled, "")
	if !hasLoopWait(s.Snapshot().Loop, protocol.Tony) {
		t.Fatal("settled wait missing")
	}
	if err := s.LoopSend(protocol.Austin, "hello"); err != nil {
		t.Fatal(err)
	}
	restored := NewStateFor(ModeGoal)
	if err := restored.Restore(s.Snapshot()); err != nil {
		t.Fatal(err)
	}
	events, err := restored.LoopEvents(protocol.Tony, "")
	if err != nil || len(events) != 1 || events[0].Text != "hello" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	if got := restored.Snapshot().Loop.Waiting[protocol.Tony].Until; !got.Equal(deadline) {
		t.Fatal("reading inbox changed deadline")
	}
	// Retrying a read after a lost response preserves the original ID.
	replay, _ := restored.LoopEvents(protocol.Tony, "")
	if replay[0].ID != events[0].ID {
		t.Fatal("event lost on read")
	}
	if _, err := restored.LoopEvents(protocol.Austin, events[0].ID); err == nil {
		t.Fatal("accepted another agent's acknowledgement")
	}
	if _, err := restored.LoopEvents(protocol.Tony, "99999"); err == nil {
		t.Fatal("accepted unknown acknowledgement")
	}
	if ok, err := restored.LoopWait(protocol.Tony, deadline, "wait"); err != nil || ok {
		t.Fatal("unread event allowed waiting")
	}
	if _, err := restored.LoopEvents(protocol.Tony, events[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.LoopEvents(protocol.Tony, events[0].ID); err != nil {
		t.Fatalf("lost ack response cannot retry: %v", err)
	}
	// More work after registering intent cancels it, instead of hiding activity.
	restored.LoopActivity(protocol.Tony, protocol.ActivityAgentStart, "")
	if ok, err := restored.LoopWait(protocol.Tony, deadline, "wait"); err != nil || !ok {
		t.Fatal(err)
	}
	restored.LoopActivity(protocol.Tony, protocol.ActivityToolStart, "bash")
	restored.LoopActivity(protocol.Tony, protocol.ActivityAgentSettled, "")
	if _, ok := restored.Snapshot().Loop.Waiting[protocol.Tony]; ok {
		t.Fatal("work did not cancel wait")
	}
	// An event between deciding to wait and registering cannot be overwritten.
	_ = restored.LoopSend(protocol.Austin, "new observation")
	if ok, _ := restored.LoopWait(protocol.Tony, deadline, "wait"); ok {
		t.Fatal("registration raced past inbox")
	}
}

func TestLoopTimerSurvivesRestoreAndFiresOnce(t *testing.T) {
	s := NewStateFor(ModeLoop)
	deadline := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	_, _ = s.LoopWait(protocol.Austin, deadline, "observe")
	s.LoopActivity(protocol.Austin, protocol.ActivityAgentSettled, "")
	restored := NewStateFor(ModeLoop)
	if err := restored.Restore(s.Snapshot()); err != nil {
		t.Fatal(err)
	}
	restored.LoopTimers(deadline)
	restored.LoopTimers(deadline.Add(time.Hour))
	events, _ := restored.LoopEvents(protocol.Austin, "")
	if len(events) != 1 || events[0].From != protocol.Duo {
		t.Fatalf("timers=%+v", events)
	}
}

func ackLoopInbox(t *testing.T, s *State, id protocol.AgentID) {
	t.Helper()
	events, err := s.LoopEvents(id, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if _, err := s.LoopEvents(id, event.ID); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoopConsultationBlocksUntilMatchingReplyIsProcessed(t *testing.T) {
	s := NewStateFor(ModeLoop)
	if _, err := s.LoopAsk(protocol.Tony, "question"); err == nil {
		t.Fatal("advisor asked")
	}
	id, err := s.LoopAsk(protocol.Austin, "which strategy?")
	if err != nil {
		t.Fatal(err)
	}
	if retry, err := s.LoopAsk(protocol.Austin, "which strategy?"); err != nil || retry != id {
		t.Fatal("ask retry not idempotent")
	}
	if _, err := s.LoopAsk(protocol.Austin, "another question"); err == nil {
		t.Fatal("overlapping ask")
	}
	if s.LoopCanAct(protocol.Austin) == nil {
		t.Fatal("owner continued before reply")
	}
	_ = s.LoopSend(protocol.Tony, "ordinary advice")
	ackLoopInbox(t, s, protocol.Austin)
	if s.LoopCanAct(protocol.Austin) == nil {
		t.Fatal("ordinary advice released consultation")
	}
	deadline := time.Now().Add(time.Hour)
	_, _ = s.LoopWait(protocol.Austin, deadline, "waiting for advisor")
	s.LoopTimers(deadline)
	ackLoopInbox(t, s, protocol.Austin)
	_ = s.LoopHuman("continue")
	ackLoopInbox(t, s, protocol.Austin)
	restored := NewStateFor(ModeLoop)
	if err := restored.Restore(s.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if restored.LoopCanAct(protocol.Austin) == nil {
		t.Fatal("timer, human or restart bypassed consultation")
	}
	if err := restored.LoopReply(protocol.Austin, id, "answer"); err == nil {
		t.Fatal("owner fabricated answer")
	}
	if err := restored.LoopReply(protocol.Tony, "stale", "answer"); err == nil {
		t.Fatal("wrong reply ID")
	}
	if err := restored.LoopFinish(protocol.Austin, "finished", "", ""); err == nil {
		t.Fatal("finished while waiting")
	}
	if err := restored.LoopReply(protocol.Tony, id, "use strategy B"); err != nil {
		t.Fatal(err)
	}
	if restored.LoopCanAct(protocol.Austin) == nil {
		t.Fatal("unprocessed reply released owner")
	}
	before := len(restored.Snapshot().Loop.Events[protocol.Austin])
	_ = restored.LoopReply(protocol.Tony, id, "use strategy B")
	if len(restored.Snapshot().Loop.Events[protocol.Austin]) != before {
		t.Fatal("duplicate reply event")
	}
	snap := restored.Snapshot()
	snap.Loop.Consultation.Waiting = false
	if !restored.Snapshot().Loop.Consultation.Waiting {
		t.Fatal("aliased consultation snapshot")
	}
	ackLoopInbox(t, restored, protocol.Austin)
	if err := restored.LoopCanAct(protocol.Austin); err != nil {
		t.Fatal(err)
	}
	next, _ := restored.LoopAsk(protocol.Austin, "next question")
	if next == id {
		t.Fatal("reused question ID")
	}
	if err := restored.LoopReply(protocol.Tony, id, "late answer"); err == nil {
		t.Fatal("late reply released new question")
	}
}

func TestLoopOwnerFinishesWithoutAdvisorApproval(t *testing.T) {
	s := NewStateFor(ModeLoop)
	_ = s.LoopSend(protocol.Tony, "optional advice")
	if err := s.LoopFinish(protocol.Tony, "done", "", ""); err == nil {
		t.Fatal("advisor finished task")
	}
	if err := s.LoopFinish(protocol.Austin, "", "legacy-id", ""); err == nil {
		t.Fatal("legacy approval accepted")
	}
	if err := s.LoopFinish(protocol.Austin, "stopped at target", "", ""); err != nil {
		t.Fatal("expected pending owner boundary")
	}
	s.LoopActivity(protocol.Tony, protocol.ActivityAgentSettled, "")
	if s.Snapshot().Phase == PhaseDone {
		t.Fatal("advisor ended owner task")
	}
	if s.LoopCanAct(protocol.Austin) == nil {
		t.Fatal("work after finish allowed")
	}
	s.LoopActivity(protocol.Austin, protocol.ActivityToolStart, "duo_status")
	s.LoopActivity(protocol.Austin, protocol.ActivityAgentSettled, "")
	if s.Snapshot().Phase != PhaseIntegrate || s.Snapshot().Loop.Summary != "stopped at target" {
		t.Fatal("owner did not reach delivery")
	}
	if _, err := s.Complete(); err != nil {
		t.Fatal(err)
	}
	previous := s.Snapshot().Loop.NextID
	if _, err := s.Reopen(); err != nil {
		t.Fatal(err)
	}
	id, _ := s.LoopAsk(protocol.Austin, "new round")
	if id == "" || s.Snapshot().Loop.NextID <= previous {
		t.Fatal("IDs reset on reopening")
	}
}

// TestLoopHasNoSignOffPath pins LOOP's one completion rule: the generic
// goalWorkflow sign-off is not reachable in LOOP, so two duo_set_status calls can
// never move RUNNING to DONE behind the owner's settled finish and the delivery
// that DONE claims. Completion itself must still work through LoopFinish.
func TestLoopHasNoSignOffPath(t *testing.T) {
	s := NewStateFor(ModeLoop)
	s.MarkStarted()
	if _, _, err := s.SetReady(protocol.Austin, true, "", "head"); err == nil {
		t.Fatal("LOOP accepted an owner signature")
	}
	if _, _, err := s.SetReady(protocol.Tony, true, "", "head"); err == nil {
		t.Fatal("LOOP accepted an advisor signature")
	}
	if snap := s.Snapshot(); snap.Phase != PhaseRunning || snap.Ready[protocol.Austin] || snap.Ready[protocol.Tony] {
		t.Fatalf("sign-off changed LOOP state: phase=%s ready=%v", snap.Phase, snap.Ready)
	}
	if _, err := s.SetPlan(protocol.Austin, "a plan"); err == nil {
		t.Fatal("LOOP accepted a shared plan")
	}
	if next, ok := ModeLoop.Next(PhaseRunning); ok {
		t.Fatalf("LOOP exposes a linear edge to %s", next)
	}
	if snap, err := s.Complete(); err == nil || snap.Phase != PhaseRunning {
		t.Fatalf("completed without a settled finish: phase=%s err=%v", snap.Phase, err)
	}

	if err := s.LoopFinish(protocol.Austin, "done", "", ""); err != nil {
		t.Fatal(err)
	}
	s.LoopActivity(protocol.Austin, protocol.ActivityAgentSettled, "")
	if snap := s.Snapshot(); snap.Phase != PhaseIntegrate {
		t.Fatalf("owner settle did not reach delivery: %s", snap.Phase)
	}
	if snap, err := s.Complete(); err != nil || snap.Phase != PhaseDone {
		t.Fatalf("completion after delivery: phase=%s err=%v", snap.Phase, err)
	}
}

func TestLoopWakeBudgetSurvivesRestart(t *testing.T) {
	s := NewStateFor(ModeLoop)
	if !s.LoopWake(1) {
		t.Fatal("first wake denied")
	}
	restored := NewStateFor(ModeLoop)
	if err := restored.Restore(s.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if restored.LoopWake(1) {
		t.Fatal("restart reset budget")
	}
	restored.ResetLoopWakes()
	if !restored.LoopWake(1) {
		t.Fatal("human continuation did not renew budget")
	}
}

func TestLoopBatchAcknowledgementConsumesOnlyProcessedPrefix(t *testing.T) {
	s := NewStateFor(ModeLoop)
	s.LoopObserve("read", "first")
	s.LoopObserve("bash", "second")
	s.LoopObserve("read", "third")
	events, _ := s.LoopEvents(protocol.Tony, "")
	if _, err := s.LoopEvents(protocol.Austin, events[1].ID, true); err == nil {
		t.Fatal("wrong-inbox batch accepted")
	}
	pending, err := s.LoopEvents(protocol.Tony, events[1].ID, true)
	if err != nil || len(pending) != 1 || pending[0].ID != events[2].ID {
		t.Fatalf("prefix ack: %+v %v", pending, err)
	}
	pending, err = s.LoopEvents(protocol.Tony, events[1].ID, true)
	if err != nil || len(pending) != 1 {
		t.Fatal("lost response retry consumed new event")
	}
	id, _ := s.LoopAsk(protocol.Austin, "which strategy?")
	_ = s.LoopSend(protocol.Tony, "unrelated observation")
	first := s.Snapshot().Loop.Events[protocol.Austin][0].ID
	_ = s.LoopReply(protocol.Tony, id, "try B")
	_, _ = s.LoopEvents(protocol.Austin, first, true)
	if s.LoopCanAct(protocol.Austin) == nil {
		t.Fatal("prefix excluding reply released owner")
	}
	reply := s.Snapshot().Loop.Consultation.ReplyID
	_, _ = s.LoopEvents(protocol.Austin, reply, true)
	if err := s.LoopCanAct(protocol.Austin); err != nil {
		t.Fatal(err)
	}
}

func TestLoopNewHumanInputSupersedesOwnerFinish(t *testing.T) {
	s := NewStateFor(ModeLoop)
	_ = s.LoopFinish(protocol.Austin, "previous goal done", "", "")
	_ = s.LoopHuman("also observe the next run")
	s.LoopActivity(protocol.Austin, protocol.ActivityAgentSettled, "")
	if s.Snapshot().Phase == PhaseDone {
		t.Fatal("ignored new human input")
	}
	if err := s.LoopFinish(protocol.Austin, "old conclusion", "", ""); err == nil {
		t.Fatal("finished before processing new user instruction")
	}
	ackLoopInbox(t, s, protocol.Austin)
	if err := s.LoopFinish(protocol.Austin, "new instruction considered", "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestLoopObserveInboxIsBoundedAndKeepsCriticalEvents(t *testing.T) {
	s := NewStateFor(ModeLoop)

	// An advisor that never reads must not let the owner's observation stream grow
	// the durable inbox without bound.
	for i := 0; i < maxLoopInbox+50; i++ {
		s.LoopObserve("bash", strconv.Itoa(i))
	}
	events := s.Snapshot().Loop.Events[protocol.Tony]
	if len(events) != maxLoopInbox {
		t.Fatalf("observation inbox = %d events, want bounded at %d", len(events), maxLoopInbox)
	}
	// The oldest step events were shed; the newest observation survives.
	if got := events[len(events)-1].Text; !strings.HasSuffix(got, strconv.Itoa(maxLoopInbox+49)) {
		t.Fatalf("newest observation not retained: %q", got)
	}

	// A consultation question is never shed by the observation trim.
	id, err := s.LoopAsk(protocol.Austin, "which strategy?")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range s.Snapshot().Loop.Events[protocol.Tony] {
		if e.Kind == "question" && e.QuestionID == id {
			found = true
		}
	}
	if !found {
		t.Fatal("consultation question was shed by observation trimming")
	}
}

func TestLoopObserveDefersPersistence(t *testing.T) {
	s := NewStateFor(ModeLoop)
	var persisted int
	s.SetPersistence(func(Snapshot) { persisted++ })

	// A burst of observations records without a snapshot write per tool; the
	// coordinator flushes them on its one-second tick.
	if !s.LoopObserve("bash", "first") {
		t.Fatal("first observation not recorded")
	}
	if !s.LoopObserve("bash", "second") {
		t.Fatal("second observation not recorded")
	}
	if persisted != 0 {
		t.Fatalf("observations persisted %d times, want 0 (coalesced)", persisted)
	}

	// A critical mutation still persists through onChange.
	if err := s.LoopSend(protocol.Tony, "advice"); err != nil {
		t.Fatal(err)
	}
	if persisted != 1 {
		t.Fatalf("persisted %d times after critical mutation, want 1", persisted)
	}
}
