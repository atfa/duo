package coordinator

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/transport"
)

func (c *Coordinator) handleLoopEvents(ctx context.Context, client *transport.Client, msg protocol.Message) {
	if msg.EventID != "" && msg.Through != "" {
		_ = c.respond(ctx, client, msg.RequestID, false, "supply ack or through, not both", "")
		return
	}
	ack := msg.EventID
	if msg.Through != "" {
		ack = msg.Through
	}
	pending, err := c.project.LoopEvents(client.Identity(), ack, msg.Through != "")
	if err == nil {
		err = c.persistStrict("loop events")
	}
	if err != nil {
		_ = c.respond(ctx, client, msg.RequestID, false, err.Error(), "")
		return
	}
	data, _ := json.Marshal(pending)
	c.recordEvent("loop_events", map[string]any{"agent": client.Identity(), "ack": ack, "through": msg.Through != "", "pending": len(pending)})
	_ = c.respond(ctx, client, msg.RequestID, true, "Pending events (acknowledge after processing): "+string(data), "")
}
func (c *Coordinator) handleLoopWait(ctx context.Context, client *transport.Client, msg protocol.Message) {
	if msg.EventID != "" {
		_ = c.respond(ctx, client, msg.RequestID, false, "use duo_events to acknowledge events before waiting", "")
		return
	}
	var until time.Time
	var err error
	if msg.Text != "" {
		until, err = time.Parse(time.RFC3339, msg.Text)
		if err != nil {
			_ = c.respond(ctx, client, msg.RequestID, false, "until must be RFC3339", "")
			return
		}
	}
	registered, err := c.project.LoopWait(client.Identity(), until, msg.Note)
	if err == nil {
		err = c.persistStrict("loop wait")
	}
	if err != nil {
		_ = c.respond(ctx, client, msg.RequestID, false, err.Error(), "")
		return
	}
	text := "Wait intent registered. End this turn now without more tools. Waiting becomes effective when the turn settles."
	if !registered {
		text = "Not waiting: unread events exist. Use duo_events to read and acknowledge them, then decide whether to wait."
	}
	c.recordEvent("loop_wait", map[string]any{"agent": client.Identity(), "registered": registered, "until": until, "reason": msg.Note})
	_ = c.respond(ctx, client, msg.RequestID, true, text, "")
}
func (c *Coordinator) handleLoopFinish(ctx context.Context, client *transport.Client, msg protocol.Message) {
	err := c.project.LoopFinish(client.Identity(), msg.Note, msg.Approve, msg.Reject)
	if err == nil {
		err = c.persistStrict("loop finish")
	}
	if err != nil {
		_ = c.respond(ctx, client, msg.RequestID, false, err.Error(), "")
		return
	}
	c.recordEvent("loop_finish", map[string]any{"agent": client.Identity(), "summary": msg.Note})
	_ = c.respond(ctx, client, msg.RequestID, true, "Completion registered by Austin. End this turn without further work; no advisor approval is required.", "")
}

func (c *Coordinator) handleLoopAsk(ctx context.Context, client *transport.Client, msg protocol.Message) {
	id, err := c.project.LoopAsk(client.Identity(), msg.Text)
	if err == nil {
		err = c.persistStrict("loop consultation")
	}
	if err != nil {
		_ = c.respond(ctx, client, msg.RequestID, false, err.Error(), "")
		return
	}
	c.recordEvent("loop_ask", map[string]any{"agent": client.Identity(), "questionId": id})
	_ = c.respond(ctx, client, msg.RequestID, true, "Consultation "+id+" queued for Tony. End your turn now. You must wait for its matching duo_reply and process that event before any further work. Advice is not approval; decide after reading it.", "")
}
func (c *Coordinator) handleLoopReply(ctx context.Context, client *transport.Client, msg protocol.Message) {
	err := c.project.LoopReply(client.Identity(), msg.QuestionID, msg.Text)
	if err == nil {
		err = c.persistStrict("loop consultation reply")
	}
	if err != nil {
		_ = c.respond(ctx, client, msg.RequestID, false, err.Error(), "")
		return
	}
	c.recordEvent("loop_reply", map[string]any{"agent": client.Identity(), "questionId": msg.QuestionID})
	_ = c.respond(ctx, client, msg.RequestID, true, "Reply queued for Austin. Austin decides whether to use it. End silently unless another question needs attention.", "")
}

// SetLoopWakeLimit configures the number of automatic wakes between explicit
// human continuations. Zero selects the default; negative disables the budget.
func (c *Coordinator) SetLoopWakeLimit(limit int) { c.loopWakeLimit = limit }
func (c *Coordinator) allowLoopWake() bool {
	limit := c.loopWakeLimit
	if limit == 0 {
		limit = 100
	}
	if limit < 0 || c.project.LoopWake(limit) {
		return true
	}
	if err := c.SetPause(); err == nil {
		c.emit(events.KindSystem, protocol.Duo, "", "LOOP automatic wake budget exhausted; inspect progress and /continue to grant another budget.")
		_ = c.persistStrict("loop wake budget exhausted")
	}
	return false
}

// attempt is the per-agent retry/cooldown state for automatic LOOP wakes.
type attempt struct {
	key   string
	count int
	last  time.Time
}

// wakeThrottled decides whether a wake for key should be sent now and returns the
// updated attempt state. A key change resets both the retry budget and the
// cooldown, so a new target — a fresh human instruction or a newly answered
// consultation — wakes immediately instead of inheriting the previous key's
// 30-second cooldown. The key is stable for a stream of ordinary observations
// (it names the oldest unread event), so those batches stay throttled.
func wakeThrottled(a attempt, key string, now time.Time) (attempt, bool) {
	if a.key != key {
		a = attempt{key: key}
	}
	if a.count >= 3 || (!a.last.IsZero() && now.Sub(a.last) < 30*time.Second) {
		return a, false
	}
	return a, true
}

// RunLoop persists elapsed timers before asking an agent to fetch events. Wakes
// are retried because a successful transport write is not an acknowledgement.
func (c *Coordinator) RunLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	attempts := map[protocol.AgentID]attempt{}
	wasHeld := false
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if c.project.Mode() != project.ModeLoop {
				continue
			}
			c.project.LoopTimers(now)
			// LoopObserve does not persist per tool; coalesce a burst of completed
			// tools into one snapshot write per tick.
			if c.loopObserveDirty.Swap(false) {
				c.persistNow("loop observations")
			}
			snap := c.project.Snapshot()
			held := c.HarnessHeld() || c.PauseSnapshot().State != "running"
			if held {
				wasHeld = true
				continue
			}
			if wasHeld {
				attempts = map[protocol.AgentID]attempt{}
				wasHeld = false
			}
			if !snap.Started || snap.Phase == project.PhaseDone {
				continue
			}
			for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
				if !c.IsAgentConnected(id) {
					continue
				}
				var busy bool
				var activity time.Time
				if c.tracker != nil {
					rt := c.tracker.Snapshot(id)
					busy, activity = rt.Busy, rt.LastActivity
				}
				if busy {
					continue
				}
				wait, hasWait := snap.Loop.Waiting[id]
				waiting := hasWait && !wait.Pending
				if snap.Loop.Finishing {
					continue
				}
				// A pending consultation is not idle. Ordinary advice, timers
				// and human continuation do not release the owner early.
				if q := snap.Loop.Consultation; id == protocol.Austin && q != nil && q.Waiting && q.Answer == "" {
					continue
				}
				text, key := loopWake(snap, id, now, activity, waiting)
				if key == "" {
					continue
				}
				a := attempts[id]
				var ok bool
				a, ok = wakeThrottled(a, key, now)
				if !ok {
					continue
				}
				if !c.allowLoopWake() {
					break
				}
				if err := c.persistStrict("loop event before wake"); err != nil {
					c.logf("%v", err)
					continue
				}
				c.recordEvent("loop_wake", map[string]any{"agent": id, "key": key, "attempt": a.count + 1})
				err := c.sendToAgent(ctx, id, protocol.Message{Version: protocol.Version, Type: protocol.MsgDuoNotice, From: protocol.Duo, To: id, Text: text, Timestamp: now.UnixMilli()})
				a.last = now
				a.count++
				attempts[id] = a
				if err != nil {
					c.logf("loop wake %s: %v", id, err)
				}
				if a.count == 3 {
					c.emit(events.KindSystem, protocol.Duo, id, "LOOP wake retry limit reached; inspect the agent, then pause and continue to retry.")
					// A consultation is the one LOOP block no agent can clear by
					// itself: the guard keeps Austin from working until he processes
					// the exact reply. Three undelivered/unread nudges are therefore a
					// human problem, not a scheduling detail.
					if strings.HasPrefix(key, "question:") || strings.HasPrefix(key, "reply:") {
						c.recordEvent("loop_consultation_unanswered", map[string]any{"agent": id, "key": key})
						c.attention(events.AttentionConsultation)
					} else if strings.HasPrefix(key, "human:") {
						c.attention(events.AttentionLoopStalled)
					}
				}
			}
		}
	}
}

// loopWake keeps scheduling independent of model speed: notifications are
// batched, busy agents are skipped by RunLoop, and only the owner gets idle nudges.
func loopWake(snap project.Snapshot, id protocol.AgentID, now, activity time.Time, waiting bool) (string, string) {
	// Consultation delivery has its own retry key: an old unread notification
	// must not consume the retry budget of a later question or answer.
	if q := snap.Loop.Consultation; q != nil && q.Waiting {
		if id == protocol.Tony && q.Answer == "" {
			return "Austin is waiting for consultation " + q.ID + ". Read duo_events/duo_status and answer with duo_reply(questionId,message). Do not repeat unrelated work or wait without answering.", "question:" + q.ID
		}
		if id == protocol.Austin && q.Answer != "" {
			return "The reply to consultation " + q.ID + " is ready. Read duo_events, process and acknowledge its reply event before continuing. You decide whether to accept the advice.", "reply:" + q.ReplyID
		}
	}
	inbox := snap.Loop.Events[id]
	if len(inbox) > 0 {
		if id == protocol.Tony {
			urgent := false
			for _, e := range inbox {
				if e.Kind == "question" {
					urgent = true
					break
				}
			}
			if !urgent && !inbox[0].At.IsZero() && now.Sub(inbox[0].At) < 5*time.Second {
				return "", ""
			}
		}
		key := "event:" + inbox[0].ID
		if id == protocol.Austin {
			for _, event := range inbox {
				if event.Kind == "human" {
					key = "human:" + event.ID
				}
			}
		}
		return "A LOOP event batch is ready. Call duo_events; process relevant events and acknowledge them. Tony: observe and advise only; answer a question with duo_reply using its questionId. Austin: notifications do not require replies; only a matching consultation reply releases your wait.", key
	}
	if id == protocol.Austin && !waiting && !activity.IsZero() && now.Sub(activity) >= 2*time.Minute {
		return "LOOP owner is idle without a wait. Continue, use duo_ask if advice is needed, duo_wait for external change, or duo_finish(summary) when stopping. Tony approval is not required.", "idle:" + activity.String()
	}
	return "", ""
}

// loopResumePrompt is the authoritative LOOP instruction for both the resume path
// and a mode switch. It is language-aware so a Chinese session does not hand the
// agent an English-only brief.
func loopResumePrompt(language string) string {
	if language == "zh" {
		return "[Duo LOOP 已恢复] Austin 负责任务与用户汇报；Tony 只观察并提供建议，不执行任务。先读 duo_status 和 duo_events。若有未决的咨询，即使重启后 Austin 也必须等待对应的回复。用 duo_ask 发起阻塞式咨询，用 duo_reply(questionId,message) 作答，用 duo_send 发送异步通知/建议。Austin 自行决定是否采纳建议，并用 duo_finish(summary) 结束，无需 Tony 批准。提交工作并保持 worktree 干净：当结束回合稳定后，Duo 会把 Austin 的 HEAD 交付到会话起始分支，只有交付成功后才进入 DONE。"
	}
	return "[Duo LOOP resumed] Austin owns the task and user report; Tony observes and advises without executing the task. Read duo_status and duo_events. If a consultation is pending, Austin must wait for its matching reply, even after restart. Use duo_ask for blocking consultation, duo_reply(questionId,message) for its answer, and duo_send for asynchronous notifications/advice. Austin decides whether to accept advice and finishes with duo_finish(summary), without Tony approval. Commit your work and keep your worktree clean: when the finished turn settles, Duo delivers Austin's HEAD to the branch the session started on, and DONE follows only after that delivery succeeds."
}
