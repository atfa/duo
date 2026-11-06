package coordinator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/recovery"
)

func TestLoopDurableEventsAndFinishWithDelivery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := startE2EWithMode(t, ctx, project.ModeLoop)
	a := r.dialAgent(t, protocol.Austin)
	b := r.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool { return r.server.IsConnected(protocol.Austin) && r.server.IsConnected(protocol.Tony) }, "agents")
	r.state.MarkStarted()
	go r.coord.RunLoop(ctx)
	// A peer who never registered a wait still receives a wake.
	request(t, a, protocol.Message{Version: 1, Type: protocol.MsgPeerMessage, Text: "hello"})
	b.awaitMessage(t, "LOOP event", func(m protocol.Message) bool {
		return m.Type == protocol.MsgDuoNotice && strings.Contains(m.Text, "LOOP event")
	})
	reply := request(t, b, protocol.Message{Version: 1, Type: protocol.MsgLoopEvents})
	if !strings.Contains(reply.Text, "hello") {
		t.Fatalf("event response: %s", reply.Text)
	}
	deadline := time.Now().Add(2 * time.Second).UTC().Truncate(time.Second)
	request(t, a, protocol.Message{Version: 1, Type: protocol.MsgLoopWait, Text: deadline.Format(time.RFC3339), Note: "timer"})
	waitFor(t, func() bool {
		snap, err := r.store.Load()
		return err == nil && len(snap.Loop.Events[protocol.Austin]) > 0
	}, "persisted timer")
	saved, err := r.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	restored := project.NewStateFor(project.ModeGoal)
	if err := restored.Restore(recovery.ProjectSnapshot(saved)); err != nil {
		t.Fatal(err)
	}
	if len(restored.Snapshot().Loop.Events[protocol.Austin]) != 1 {
		t.Fatal("timer missing after restore")
	}
	// Uncommitted work prevents completion and leaves the target untouched.
	if err := os.WriteFile(filepath.Join(r.set.Austin.Path, "uncommitted"), []byte("work"), 0600); err != nil {
		t.Fatal(err)
	}
	head := gitHead(t, r.repo)
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		for _, e := range r.state.Snapshot().Loop.Events[id] {
			agent := a
			if id == protocol.Tony {
				agent = b
			}
			request(t, agent, protocol.Message{Version: 1, Type: protocol.MsgLoopEvents, EventID: e.ID})
		}
	}
	request(t, a, protocol.Message{Version: 1, Type: protocol.MsgLoopFinish, Note: "complete"})
	if r.state.Snapshot().Phase == project.PhaseDone {
		t.Fatal("finished before owner settled")
	}
	r.coord.handleActivity(protocol.Austin, protocol.Message{Activity: protocol.ActivityAgentSettled})
	saved, err = r.store.Load()
	if err != nil || !saved.NeedsDelivery() || saved.Delivery.Reason == "" {
		t.Fatalf("LOOP should await delivery: %+v, %v", saved, err)
	}
	if r.state.Snapshot().Phase == project.PhaseDone || gitHead(t, r.repo) != head {
		t.Fatal("dirty worktree completed or moved target")
	}
	if err := os.Remove(filepath.Join(r.set.Austin.Path, "uncommitted")); err != nil {
		t.Fatal(err)
	}
	r.coord.deliverFinal(ctx, r.state.Snapshot())
	if r.state.Snapshot().Phase != project.PhaseDone || gitHead(t, r.repo) != head {
		t.Fatal("clean no-op delivery did not complete")
	}
}

func TestLoopResumeHoldKeepsPendingEventSilent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := startE2EWithMode(t, ctx, project.ModeLoop)
	a := r.dialAgent(t, protocol.Austin)
	b := r.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool { return r.server.IsConnected(protocol.Austin) && r.server.IsConnected(protocol.Tony) }, "agents")
	r.state.MarkStarted()
	r.coord.HoldResume()
	go r.coord.RunLoop(ctx)
	request(t, a, protocol.Message{Version: 1, Type: protocol.MsgPeerMessage, Text: "pending during resume"})
	if err := b.conn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	defer b.conn.SetReadDeadline(time.Time{})
	var msg protocol.Message
	if err := b.dec.Decode(&msg); err == nil {
		t.Fatalf("resume hold delivered %+v", msg)
	}
	if len(r.state.Snapshot().Loop.Events[protocol.Tony]) != 1 {
		t.Fatal("event not retained")
	}
}

func TestLoopWaitRequiresSettledTurnAndRejectsWrongAck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := startE2EWithMode(t, ctx, project.ModeLoop)
	a := r.dialAgent(t, protocol.Austin)
	b := r.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool { return r.server.IsConnected(protocol.Austin) && r.server.IsConnected(protocol.Tony) }, "agents")
	r.coord.handleActivity(protocol.Austin, protocol.Message{Activity: protocol.ActivityAgentStart})
	reply := request(t, a, protocol.Message{Version: 1, Type: protocol.MsgLoopWait, Note: "observe"})
	if !strings.Contains(reply.Text, "intent registered") || !r.state.Snapshot().Loop.Waiting[protocol.Austin].Pending {
		t.Fatal("wait became effective before settled")
	}
	r.coord.handleActivity(protocol.Austin, protocol.Message{Activity: protocol.ActivityAgentSettled})
	if r.state.Snapshot().Loop.Waiting[protocol.Austin].Pending {
		t.Fatal("settled not observed")
	}
	r.coord.handleActivity(protocol.Austin, protocol.Message{Activity: protocol.ActivityAgentStart})
	request(t, a, protocol.Message{Version: 1, Type: protocol.MsgPeerMessage, Text: "observe this"})
	id := r.state.Snapshot().Loop.Events[protocol.Tony][0].ID
	// request helper expects successful replies; read an explicit error directly.
	if err := a.enc.Encode(protocol.Message{Version: 1, Type: protocol.MsgLoopEvents, EventID: id, RequestID: "wrong-ack"}); err != nil {
		t.Fatal(err)
	}
	bad := a.awaitMessage(t, "wrong ack", func(m protocol.Message) bool { return m.RequestID == "wrong-ack" })
	if bad.OK || !strings.Contains(bad.Text, "inbox") {
		t.Fatalf("wrong ack accepted: %+v", bad)
	}
	reply = request(t, b, protocol.Message{Version: 1, Type: protocol.MsgLoopWait})
	if !strings.Contains(reply.Text, "Not waiting") {
		t.Fatalf("wait with pending events: %s", reply.Text)
	}
	if _, ok := r.state.Snapshot().Loop.Waiting[protocol.Tony]; ok {
		t.Fatal("pending inbox registered wait")
	}
	request(t, b, protocol.Message{Version: 1, Type: protocol.MsgLoopEvents, EventID: id})
	reply = request(t, b, protocol.Message{Version: 1, Type: protocol.MsgLoopWait})
	if !strings.Contains(reply.Text, "intent registered") {
		t.Fatal(reply.Text)
	}
	r.coord.handleActivity(protocol.Tony, protocol.Message{Activity: protocol.ActivityToolStart, Tool: "bash"})
	r.coord.handleActivity(protocol.Tony, protocol.Message{Activity: protocol.ActivityAgentSettled})
	if _, ok := r.state.Snapshot().Loop.Waiting[protocol.Tony]; ok {
		t.Fatal("work after wait hidden as waiting")
	}
}

func TestLoopWakeBudgetPausesUntilHumanContinues(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := startE2EWithMode(t, ctx, project.ModeLoop)
	r.dialAgent(t, protocol.Austin)
	r.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool { return r.server.IsConnected(protocol.Austin) && r.server.IsConnected(protocol.Tony) }, "agents")
	r.state.MarkStarted()
	r.coord.SetLoopWakeLimit(1)
	if !r.coord.allowLoopWake() || r.coord.allowLoopWake() {
		t.Fatal("wake budget not enforced")
	}
	if r.coord.PauseSnapshot().State != "paused" {
		t.Fatalf("not paused: %+v", r.coord.PauseSnapshot())
	}
	if err := r.coord.ContinuePause(ctx); err != nil {
		t.Fatal(err)
	}
	if !r.coord.allowLoopWake() {
		t.Fatal("explicit continuation did not renew budget")
	}
}

func TestLoopConsultationWaitsForBusyAdvisorAndMatchingReply(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := startE2EWithMode(t, ctx, project.ModeLoop)
	a := r.dialAgent(t, protocol.Austin)
	b := r.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool { return r.server.IsConnected(protocol.Austin) && r.server.IsConnected(protocol.Tony) }, "agents")
	r.state.MarkStarted()
	r.coord.handleActivity(protocol.Tony, protocol.Message{Activity: protocol.ActivityAgentStart})
	r.coord.handleActivity(protocol.Austin, protocol.Message{Activity: protocol.ActivityToolStart, Tool: "bash", Detail: "observe experiment"})
	r.coord.handleActivity(protocol.Austin, protocol.Message{Activity: protocol.ActivityToolEnd, Tool: "bash"})
	events := r.state.Snapshot().Loop.Events[protocol.Tony]
	if len(events) != 1 || events[0].Kind != "step" || !strings.Contains(events[0].Text, "observe experiment") {
		t.Fatalf("step notification: %+v", events)
	}
	request(t, a, protocol.Message{Version: 1, Type: protocol.MsgLoopAsk, Text: "which strategy?"})
	id := r.state.Snapshot().Loop.Consultation.ID
	if err := a.enc.Encode(protocol.Message{Version: 1, Type: protocol.MsgLoopGuard, RequestID: "blocked"}); err != nil {
		t.Fatal(err)
	}
	blocked := a.awaitMessage(t, "consultation gate", func(m protocol.Message) bool { return m.RequestID == "blocked" })
	if blocked.OK {
		t.Fatal("continued while waiting")
	}
	saved, err := r.store.Load()
	if err != nil || saved.Loop.Consultation == nil || saved.Loop.Consultation.ID != id {
		t.Fatal("consultation not durable")
	}
	r.coord.handleActivity(protocol.Austin, protocol.Message{Activity: protocol.ActivityAgentSettled})
	go r.coord.RunLoop(ctx)
	// A slow model is neither interrupted nor retried while its turn is busy.
	if err := b.conn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var msg protocol.Message
	if err := b.dec.Decode(&msg); err == nil {
		t.Fatalf("interrupted busy advisor: %+v", msg)
	}
	_ = b.conn.SetReadDeadline(time.Time{})
	b.dec = json.NewDecoder(b.conn) // Reset the decoder after the deliberate read timeout.
	r.coord.handleActivity(protocol.Tony, protocol.Message{Activity: protocol.ActivityAgentSettled})
	b.awaitMessage(t, "advisor batch", func(m protocol.Message) bool { return m.Type == protocol.MsgDuoNotice })
	inbox := request(t, b, protocol.Message{Version: 1, Type: protocol.MsgLoopEvents})
	if !strings.Contains(inbox.Text, "which strategy?") || !strings.Contains(inbox.Text, "observe experiment") {
		t.Fatal("batch lost events")
	}
	request(t, b, protocol.Message{Version: 1, Type: protocol.MsgPeerMessage, Text: "unsolicited comment"})
	if r.state.LoopCanAct(protocol.Austin) == nil {
		t.Fatal("ordinary comment released owner")
	}
	request(t, b, protocol.Message{Version: 1, Type: protocol.MsgLoopReply, QuestionID: id, Text: "try B"})
	a.awaitMessage(t, "owner reply wake", func(m protocol.Message) bool { return m.Type == protocol.MsgDuoNotice })
	q := r.state.Snapshot().Loop.Consultation
	request(t, a, protocol.Message{Version: 1, Type: protocol.MsgLoopEvents, EventID: q.ReplyID})
	request(t, a, protocol.Message{Version: 1, Type: protocol.MsgLoopGuard})
	request(t, a, protocol.Message{Version: 1, Type: protocol.MsgLoopFinish, Note: "choose strategy A instead; stopping"})
	r.coord.handleActivity(protocol.Austin, protocol.Message{Activity: protocol.ActivityAgentSettled})
	if r.state.Snapshot().Phase != project.PhaseDone {
		t.Fatal("required advisor approval")
	}
}

func TestLoopNotificationBatchAndAdvisorSilence(t *testing.T) {
	s := project.NewStateFor(project.ModeLoop)
	now := time.Now()
	s.LoopObserve("read", "metrics")
	snap := s.Snapshot()
	if _, key := loopWake(snap, protocol.Tony, now, now, false); key != "" {
		t.Fatal("notification not batched")
	}
	if _, key := loopWake(snap, protocol.Tony, now.Add(6*time.Second), now, false); key == "" {
		t.Fatal("batch never wakes")
	}
	_, _ = s.LoopAsk(protocol.Austin, "need advice")
	if _, key := loopWake(s.Snapshot(), protocol.Tony, now, now, false); !strings.HasPrefix(key, "question:") {
		t.Fatal("question reused an old notification retry key")
	}
	q := s.Snapshot().Loop.Consultation
	_ = s.LoopSend(protocol.Tony, "older unrelated advice")
	_ = s.LoopReply(protocol.Tony, q.ID, "try B")
	if _, key := loopWake(s.Snapshot(), protocol.Austin, now, now, false); !strings.HasPrefix(key, "reply:") {
		t.Fatal("reply reused an old notification retry key")
	}
	if _, key := loopWake(project.Snapshot{}, protocol.Tony, now, now.Add(-time.Hour), false); key != "" {
		t.Fatal("idle advisor nudged to invent work")
	}
}

func TestLoopOwnerStartsWithoutConnectedAdvisor(t *testing.T) {
	ctx := context.Background()
	r := startE2EWithMode(t, ctx, project.ModeLoop)
	a := r.dialAgent(t, protocol.Austin)
	waitFor(t, func() bool { return r.server.IsConnected(protocol.Austin) }, "owner")
	if err := r.coord.SubmitUserTask(ctx, "observe the experiment"); err != nil {
		t.Fatal(err)
	}
	prompt := a.awaitMessage(t, "owner task", func(m protocol.Message) bool { return m.Type == protocol.MsgHumanPrompt })
	if !strings.Contains(prompt.Text, "Austin owns") || strings.Contains(prompt.Text, "resolve disagreements") {
		t.Fatal("old dual-owner guidance leaked")
	}
	snap := r.state.Snapshot()
	if snap.Loop.Goal != "observe the experiment" || len(snap.Loop.Events[protocol.Tony]) != 1 {
		t.Fatal("goal or advisor notification not persisted")
	}
	request(t, a, protocol.Message{Version: 1, Type: protocol.MsgLoopAsk, Text: "need advice now"})
	if r.state.LoopCanAct(protocol.Austin) == nil {
		t.Fatal("offline advisor bypassed requested consultation")
	}
}
