package harness

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

type Sender interface {
	Send(context.Context, protocol.AgentID, protocol.Message) error
	IsConnected(protocol.AgentID) bool
}

type Config struct {
	IdleThreshold  time.Duration
	StallThreshold time.Duration
	Cooldown       time.Duration
	TickInterval   time.Duration
}

type Monitor struct {
	cfg     Config
	project *project.State
	tracker *Tracker
	sender  Sender

	mu       sync.Mutex
	lastWake time.Time
}

func NewMonitor(cfg Config, state *project.State, tracker *Tracker, sender Sender) *Monitor {
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = 2 * time.Second
	}
	return &Monitor{cfg: cfg, project: state, tracker: tracker, sender: sender}
}

func (m *Monitor) Run(ctx context.Context) {
	ticker := time.NewTicker(m.cfg.TickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.maybeWake(ctx)
		}
	}
}

func (m *Monitor) maybeWake(ctx context.Context) {
	if !m.sender.IsConnected(protocol.Austin) || !m.sender.IsConnected(protocol.Tony) {
		return
	}

	state := m.project.Snapshot()
	if !state.Started || state.Phase == project.PhaseDone {
		return
	}

	austin := m.tracker.Snapshot(protocol.Austin)
	tony := m.tracker.Snapshot(protocol.Tony)

	latest := austin.LastActivity
	if tony.LastActivity.After(latest) {
		latest = tony.LastActivity
	}
	if state.LastMutation.After(latest) {
		latest = state.LastMutation
	}

	quietFor := time.Since(latest)
	bothIdle := !austin.Busy && !tony.Busy
	activeWork := austin.ProviderActive || tony.ProviderActive || austin.ToolDepth > 0 || tony.ToolDepth > 0
	normalIdle := bothIdle && !activeWork && quietFor >= m.cfg.IdleThreshold
	hardStalled := quietFor >= m.cfg.StallThreshold

	if !normalIdle && !hardStalled {
		return
	}

	m.mu.Lock()
	if !m.lastWake.IsZero() && time.Since(m.lastWake) < m.cfg.Cooldown {
		m.mu.Unlock()
		return
	}
	m.lastWake = time.Now()
	m.mu.Unlock()

	m.tracker.Touch(protocol.Austin)

	reason := "Both agents are idle while the Duo project is unfinished."
	if hardStalled && !normalIdle {
		reason = "The Duo project is unfinished and no activity has been observed for an unusually long time. This may be a stalled run."
	}

	prompt := fmt.Sprintf(
		"[Duo Harness]\n%s\n\n%s\n\nYou are the coordinator for recovery. Inspect the current phase and take the next concrete action. If your part is complete, call duo_set_status with ready=true. If Tony is the blocker, use duo_send to give Tony the exact information or nudge needed. In PLAN, create or revise the shared plan if necessary. Do not merely acknowledge this message.",
		reason,
		state.String(),
	)

	fmt.Printf("[HARNESS] nudging Austin after %s without useful activity\n", quietFor.Round(time.Second))
	if err := m.sender.Send(ctx, protocol.Austin, protocol.Message{
		Version:   1,
		Type:      protocol.MsgHarnessPrompt,
		From:      protocol.Duo,
		To:        protocol.Austin,
		Text:      prompt,
		Timestamp: time.Now().UnixMilli(),
	}); err != nil {
		log.Printf("harness send failed: %v", err)
	}
}
