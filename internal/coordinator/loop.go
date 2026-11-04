package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/transport"
)

func (c *Coordinator) handleLoopWait(ctx context.Context, client *transport.Client, msg protocol.Message) {
	var until time.Time
	var err error
	if msg.Text != "" {
		until, err = time.Parse(time.RFC3339, msg.Text)
		if err != nil {
			_ = c.respond(ctx, client, msg.RequestID, false, "until must be RFC3339", "")
			return
		}
	}
	pending, err := c.project.LoopWait(client.Identity(), until, msg.Note, msg.EventID)
	if err == nil {
		err = c.persistStrict("loop wait")
	}
	if err != nil {
		_ = c.respond(ctx, client, msg.RequestID, false, err.Error(), "")
		return
	}
	data, _ := json.Marshal(pending)
	text := "Waiting. End your turn; Duo will wake you when an event is ready."
	if len(pending) > 0 {
		text = "Pending events (acknowledge each event ID only after processing): " + string(data)
	}
	_ = c.respond(ctx, client, msg.RequestID, true, text, "")
}
func (c *Coordinator) handleLoopFinish(ctx context.Context, client *transport.Client, msg protocol.Message) {
	done, err := c.project.LoopFinish(client.Identity(), msg.Note)
	if err == nil {
		err = c.persistStrict("loop finish")
	}
	if err != nil {
		_ = c.respond(ctx, client, msg.RequestID, false, err.Error(), "")
		return
	}
	text := "Finish proposed; waiting for your peer to approve the same summary."
	if done {
		text = "LOOP complete: " + strings.TrimSpace(msg.Note)
		c.emit(events.KindSystem, protocol.Duo, "", text)
	}
	_ = c.respond(ctx, client, msg.RequestID, true, text, "")
}

// RunLoop persists elapsed timers before asking an agent to fetch events. Wakes
// are retried because a successful transport write is not an acknowledgement.
func (c *Coordinator) RunLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	type attempt struct {
		key   string
		count int
		last  time.Time
	}
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
				_, waiting := snap.Loop.Waiting[id]
				text, key := "", ""
				if len(snap.Loop.Events[id]) > 0 {
					key = "event:" + snap.Loop.Events[id][0].ID
					text = "A LOOP event is ready. Call duo_wait to read it; acknowledge its ID only after processing."
				} else if !waiting && !activity.IsZero() && now.Sub(activity) >= 2*time.Minute {
					key = "idle:" + activity.String()
					text = "LOOP is idle without a registered wait. Continue the task, call duo_wait, or agree a finish summary with your peer."
				}
				if key == "" {
					continue
				}
				a := attempts[id]
				if a.key != key {
					a = attempt{key: key}
				}
				if a.count >= 3 || (!a.last.IsZero() && now.Sub(a.last) < 30*time.Second) {
					continue
				}
				if err := c.persistStrict("loop event before wake"); err != nil {
					c.logf("%v", err)
					continue
				}
				err := c.sendToAgent(ctx, id, protocol.Message{Version: protocol.Version, Type: protocol.MsgDuoNotice, From: protocol.Duo, To: id, Text: text, Timestamp: now.UnixMilli()})
				a.last = now
				a.count++
				attempts[id] = a
				if err != nil {
					c.logf("loop wake %s: %v", id, err)
				}
				if a.count == 3 {
					c.emit(events.KindSystem, protocol.Duo, id, "LOOP wake retry limit reached; inspect the agent, then pause and continue to retry.")
				}
			}
		}
	}
}

func loopResumePrompt() string {
	return fmt.Sprint("[Duo LOOP resumed] Read duo_status and continue the collaboration. Call duo_wait for pending events and acknowledge each event ID after processing. Call duo_finish with the same summary as your peer to finish.")
}
