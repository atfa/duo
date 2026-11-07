package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// awaitDuo decodes what Duo pushed to this agent until one message satisfies
// match. It is the bridge-side twin of the coordinator's test helper: the same
// transport a real adapter reads.
func (c *bridgeAgent) awaitDuo(t *testing.T, what string, match func(protocol.Message) bool) protocol.Message {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.conn.SetReadDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }()
	for {
		var message protocol.Message
		if err := c.dec.Decode(&message); err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		if match(message) {
			return message
		}
	}
}

// TestQuietLoopSwitchStillDeliversTheNextTask drives the reported bug through the
// interface's own commands rather than the coordinator alone: `/mode loop` is a
// quiet switch, and the resume question that follows has "Not now" as its default,
// meaning the human's next task is the continuation. LOOP only queues that text as
// an inbox event and RunLoop sends the wake, so a task that left the quiet-switch
// hold in place reached nobody: the prompt looked accepted and did nothing.
func TestQuietLoopSwitchStillDeliversTheNextTask(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	round := startSessionRound(t, ctx, project.ModeGoal)
	austin := round.dial(t, protocol.Austin)
	round.dial(t, protocol.Tony)
	round.waitConnected(t)

	if err := round.svc.Do(ctx, SubmitTask{Text: "publish the release notes"}); err != nil {
		t.Fatalf("goal task: %v", err)
	}
	austin.awaitDuo(t, "the goal task prompt", func(m protocol.Message) bool {
		return m.Type == protocol.MsgHumanPrompt
	})

	if err := round.svc.Do(ctx, SwitchMode{Mode: project.ModeLoop.String(), Quiet: true}); err != nil {
		t.Fatalf("quiet switch to LOOP: %v", err)
	}
	if mode := round.svc.View().Mode; mode != project.ModeLoop {
		t.Fatalf("view mode = %s, want loop", mode)
	}

	if err := round.svc.Do(ctx, SubmitTask{Text: "also update the changelog"}); err != nil {
		t.Fatalf("LOOP task: %v", err)
	}
	austin.awaitDuo(t, "the LOOP wake for the human task", func(m protocol.Message) bool {
		return m.Type == protocol.MsgDuoNotice && strings.Contains(m.Text, "LOOP event")
	})

	view := round.svc.View()
	if view.ResumePending {
		t.Fatal("the task must clear the session's resume-pending state")
	}
	events := view.Project.Loop.Events[protocol.Austin]
	if len(events) != 1 {
		t.Fatalf("Austin's loop inbox has %d events, want the one human event", len(events))
	}
	if events[0].Kind != "human" || !strings.Contains(events[0].Text, "changelog") {
		t.Fatalf("queued event = %+v, want the human task", events[0])
	}
}
