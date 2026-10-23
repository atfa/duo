package coordinator

import (
	"context"
	"fmt"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/protocol"
)

// contextWarnPercent is the context occupancy at which Duo tells an agent its
// window is nearly full. Zero disables the warning: a driver that reports no
// window, and an operator who set the key to 0, both mean "do not tell me".
func (c *Coordinator) SetContextWarnPercent(percent int) {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	c.contextWarnPercent = percent
}

// checkContextPressure watches the context figure a driver reports and tells the
// agent once, per crossing, that its window is nearly full. An agent that runs out
// of context mid-task loses the plan it was working from, and compaction arrives
// after the damage: the useful moment to say something is the crossing, not the
// overflow.
//
// The warning is one per crossing, not one per report — a driver reports context
// on every provider call, and an agent told the same thing twenty times learns
// nothing. Falling back below the threshold re-arms it, because a compaction that
// frees the window and a later refill is a genuinely new event.
//
// The flag lives on the runtime, and the interface projects it, so the sidebar's
// red and the agent's nudge are the same judgement made once.
func (c *Coordinator) checkContextPressure(agent protocol.AgentID) {
	if c.contextWarnPercent <= 0 || c.tracker == nil {
		return
	}
	rt := c.tracker.Snapshot(agent)
	if rt.ContextWindow <= 0 {
		return
	}
	percent := float64(rt.ContextTokens) / float64(rt.ContextWindow) * 100
	if percent < float64(c.contextWarnPercent) {
		if rt.ContextWarned {
			c.tracker.SetContextWarned(agent, false)
		}
		return
	}
	if rt.ContextWarned {
		return
	}
	c.tracker.SetContextWarned(agent, true)

	text := fmt.Sprintf(
		"Your context is at %.0f%% of its %s-token window. Wrap up what you are doing: finish the file you are in, record your state, and reach a clean point before the window forces a compaction.",
		percent, humanTokens(rt.ContextWindow),
	)
	if c.language == "zh" {
		text = fmt.Sprintf(
			"你的上下文已用到窗口的 %.0f%%（窗口 %s token）。请开始收尾：把手上的文件写完、记录当前状态，在窗口强制压缩前到达一个干净的点。",
			percent, humanTokens(rt.ContextWindow),
		)
	}
	c.emit(events.KindSystem, protocol.Duo, agent,
		fmt.Sprintf("warned %s: context at %.0f%% of the window", agent, percent))
	c.recordEvent("context_pressure", map[string]any{
		"agent":   string(agent),
		"percent": int(percent),
		"window":  rt.ContextWindow,
	})
	if err := c.sendToAgent(context.Background(), agent, protocol.Message{
		Version:   protocol.Version,
		Type:      protocol.MsgDuoNotice,
		From:      protocol.Duo,
		To:        agent,
		Text:      text,
		Timestamp: time.Now().UnixMilli(),
	}); err != nil {
		c.logf("context pressure notice to %s failed: %v", agent, err)
	}
}

// humanTokens renders a token count the way the interface does, so the number in
// the agent's notice matches the number on the screen.
func humanTokens(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}
