package session

import (
	"context"
	"strings"

	"github.com/atfa/duo/internal/observer"
)

// nameSession asks the observer for a human-facing name for this session, once,
// from the first task the human submits.
//
// It is the narrowest form of the observer's advice: the model supplies a name
// and nothing else, the session decides where it lives, and a failure leaves the
// session exactly as it was — displayed by its id.
func (s *Service) nameSession(task string) {
	// Called with s.mu held by Do, so the decisions below are made once per
	// session: a title that is being fetched is not visible yet, but the first
	// answer to arrive is the one kept (see nameSessionInBackground).
	if s.observer == nil || strings.TrimSpace(s.title) != "" || strings.TrimSpace(task) == "" {
		return
	}
	obs := s.observer
	go s.nameSessionInBackground(obs, task)
}

// nameSessionInBackground performs the one naming call and persists its answer.
//
// The report panel's worker is deliberately not reused: a title must not replace a
// queued report or make Ctrl+E look like it is analyzing. A model call may take
// its whole timeout, so it runs here rather than in the command that submitted
// the task.
func (s *Service) nameSessionInBackground(obs *observer.Observer, task string) {
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	title, err := obs.Title(ctx, task)

	s.mu.Lock()
	defer s.mu.Unlock()
	// Nothing to name once the session is closed, and the first title wins: a
	// slower answer that arrives after one is already recorded is stale.
	if s.closed || s.store == nil || s.state == nil || strings.TrimSpace(s.title) != "" {
		return
	}
	if err != nil {
		// Silent by design: a session without a title is a normal session. The
		// failure is worth a log line, never a timeline entry or a notice.
		s.logf()("observer: session title: %v", err)
		return
	}
	s.title = title
	if err := s.store.Save(s.composeSnapshot(s.coord)); err != nil {
		s.logf()("observer: persist session title: %v", err)
		return
	}
	if s.journal != nil {
		s.journal.Record("session_title", map[string]any{"title": title})
	}
}
