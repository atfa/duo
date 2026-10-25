package session

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// observerReportsKept is how many reports the panel can look back on. The
// observer runs only when a human asks for one, so a short history is enough and
// a growing log is exactly what the old timer-driven observer got wrong.
const observerReportsKept = 3

// RequestObserverReport asks the observer for a fresh status report.
//
// It returns immediately. A model call may take its whole timeout, and no
// frontend may be blocked behind one, so the request is handed to a worker. A
// request that arrives while another is in flight is queued once: pressing the
// key repeatedly cannot fan out requests.
func (s *Service) RequestObserverReport() {
	s.observerReportMu.RLock()
	trigger := s.observerTrigger
	s.observerReportMu.RUnlock()
	if trigger == nil {
		return
	}
	select {
	case trigger <- struct{}{}:
	default:
	}
}

// startObserverWorker runs the request queue. Its context is derived from the
// session's own, so closing the session cancels an in-flight model call.
func (s *Service) startObserverWorker() {
	if s.observerCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	trigger := make(chan struct{}, 1)
	s.observerReportMu.Lock()
	s.observerTrigger = trigger
	s.observerReportMu.Unlock()
	s.observerCancel = cancel
	go s.runObserverReports(ctx, trigger)
}

func (s *Service) runObserverReports(ctx context.Context, trigger <-chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-trigger:
			s.analyzeObserverReport(ctx)
		}
	}
}

// analyzeObserverReport performs one request and files the result.
//
// The live block is rendered here, at request time, so a queued re-run reports
// what is true when it runs rather than what was true when the key was pressed.
func (s *Service) analyzeObserverReport(ctx context.Context) {
	s.observerReportMu.Lock()
	s.observerAnalyzing = true
	s.observerRequestedAt = time.Now()
	s.observerReportMu.Unlock()
	defer func() {
		s.observerReportMu.Lock()
		s.observerAnalyzing = false
		s.observerReportMu.Unlock()
	}()

	s.mu.RLock()
	obs := s.observer
	s.mu.RUnlock()
	if obs == nil {
		return
	}

	text, err := obs.Report(ctx)
	if strings.TrimSpace(text) != "" {
		s.setObserverReport(text)
		// A successful report clears a stale notice; a truncation notice is set
		// again immediately below.
		s.clearObserverNotice()
	}
	if err != nil {
		s.observerLogf()("observer: %v", err)
	}
}

// setObserverReport files one report, newest first, keeping a short history.
func (s *Service) setObserverReport(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	s.observerReportMu.Lock()
	defer s.observerReportMu.Unlock()
	s.observerReports = append([]ObserverReport{{At: time.Now(), Text: text}}, s.observerReports...)
	if len(s.observerReports) > observerReportsKept {
		s.observerReports = s.observerReports[:observerReportsKept]
	}
}

// observerReportSnapshot projects the report panel state.
func (s *Service) observerReportSnapshot() ([]ObserverReport, bool, time.Time) {
	s.observerReportMu.RLock()
	defer s.observerReportMu.RUnlock()
	return append([]ObserverReport(nil), s.observerReports...), s.observerAnalyzing, s.observerRequestedAt
}

// observerLogf is the one place an observer failure becomes a log line and a
// panel notice. Missing-key state has a dedicated, actionable rendering in
// ObserverView, so that template is logged without becoming a notice.
func (s *Service) observerLogf() func(string, ...any) {
	return func(format string, args ...any) {
		message := fmt.Sprintf(format, args...)
		if !strings.Contains(message, "no API key in DUO_OBSERVER_API_KEY") {
			s.setObserverNotice(message)
		}
		s.logf()("%s", message)
	}
}

// observerLiveContext renders the runtime state a report's "Doing now" section
// is written from: mode, phase, the current step, and per agent what it is doing
// at this instant. It is the only input that can answer "what are they doing
// right now", because a running tool never becomes a message.
func (s *Service) observerLiveContext() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := s.view()
	now := time.Now()

	var b strings.Builder
	snap := v.Project
	fmt.Fprintf(&b, "mode=%s phase=%s", snap.EffectiveMode().Display(), snap.Phase)
	if snap.PlanVersion > 0 {
		fmt.Fprintf(&b, " planVersion=%d", snap.PlanVersion)
	}
	if snap.EffectiveMode() == project.ModeStep && len(snap.Steps) > 0 {
		fmt.Fprintf(&b, " step=%d/%d", snap.Step, len(snap.Steps))
		if i := snap.Step - 1; i >= 0 && i < len(snap.Steps) {
			fmt.Fprintf(&b, " currentStep=%q", clipRunes(snap.Steps[i].Title, 120))
		}
	}
	b.WriteString("\n")
	// The shared plan text and the step queue are the only source for how the
	// task was decomposed and how far it has come, so they are rendered here
	// (bounded), not left for the report model to guess from the log.
	if plan := strings.TrimSpace(snap.Plan); plan != "" {
		fmt.Fprintf(&b, "\nShared plan (version %d):\n%s\n", snap.PlanVersion, clipRunes(plan, 1200))
	}
	if len(snap.Steps) > 0 {
		fmt.Fprintf(&b, "\nStep queue (%d steps, %d landed, current step %d of %d):\n%s",
			len(snap.Steps), landedSteps(snap.Steps), snap.Step, len(snap.Steps), snap.Steps.Render())
	}
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		fmt.Fprintf(&b, "%s: %s\n", id, agentActivity(v.Agents[id], now))
	}
	return strings.TrimRight(b.String(), "\n")
}

// landedSteps counts the queue's done steps for the live block's one-line tally.
func landedSteps(steps project.Steps) int {
	n := 0
	for _, st := range steps {
		if st.Done {
			n++
		}
	}
	return n
}

// agentActivity describes one agent from its live runtime state.
//
// The distinction that matters is running versus finished: the tracker keeps the
// last tool after it ends, so "tool_started set and depth above zero" is what
// makes a tool current.
func agentActivity(av AgentView, now time.Time) string {
	rt := av.Runtime
	var parts []string
	running := rt.Tool != "" && rt.ToolDepth > 0
	switch {
	case running:
		parts = append(parts, "busy running tool "+rt.Tool+toolDetail(rt.ToolDetail)+
			" for "+shortDuration(now.Sub(rt.ToolStarted)))
	case rt.Busy && rt.ProviderActive:
		parts = append(parts, "busy waiting on the model")
	case rt.Busy:
		parts = append(parts, "busy")
	default:
		parts = append(parts, "idle for "+shortDuration(now.Sub(rt.LastActivity)))
	}
	if rt.Busy && !rt.TurnStarted.IsZero() {
		parts = append(parts, fmt.Sprintf("turn started %s ago with %d tool call(s)", shortDuration(now.Sub(rt.TurnStarted)), rt.Tools))
	}
	if tail := strings.TrimSpace(rt.StreamTail); tail != "" {
		parts = append(parts, "currently writing: "+clipRunes(tail, 240))
	}
	if len(rt.Recent) > 0 {
		parts = append(parts, "last tools: "+recentTools(rt.Recent))
	}
	if rt.ContextWindow > 0 {
		parts = append(parts, fmt.Sprintf("context %dk/%dk", rt.ContextTokens/1000, rt.ContextWindow/1000))
	}
	if err := strings.TrimSpace(rt.LastError); err != "" {
		parts = append(parts, "last error: "+clipRunes(err, 200))
	}
	return strings.Join(parts, "; ")
}

// toolDetail renders a tool's arguments, clipped so one long command cannot
// dominate the block.
func toolDetail(detail string) string {
	detail = strings.Join(strings.Fields(strings.TrimSpace(detail)), " ")
	if detail == "" {
		return ""
	}
	return "(" + clipRunes(detail, 200) + ")"
}

// recentTools renders the finished tool trail of the current turn.
func recentTools(notes []harness.ToolNote) string {
	out := make([]string, 0, len(notes))
	for _, note := range notes {
		mark := "ok"
		if !note.OK {
			mark = "failed"
		}
		out = append(out, fmt.Sprintf("%s %s %s", note.Name, mark, shortDuration(note.Duration)))
	}
	return strings.Join(out, ", ")
}

// shortDuration renders a duration the way a status line reads.
func shortDuration(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// clipRunes shortens text to at most limit runes, marking the cut.
func clipRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
