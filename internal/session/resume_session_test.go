package session

import (
	"context"
	"testing"

	"github.com/atfa/duo/internal/coordinator"
	"github.com/atfa/duo/internal/events"
)

// TestResumeSessionRequestReachesTheView pins the /sessions seam: the command only
// records the id (the outer CLI owns the relaunch), the view is how a frontend
// learns about it, and the pulse is what makes an idle frontend look. It also
// refuses the session that is already open, because switching to it would close
// it for nothing.
func TestResumeSessionRequestReachesTheView(t *testing.T) {
	svc := &Service{coord: &coordinator.Coordinator{}, bus: events.NewBus(), sessionID: "open-session"}
	ch, cancel := svc.bus.Subscribe(8)
	defer cancel()
	if svc.View().ResumeSession != "" {
		t.Fatal("a fresh session must not ask to switch sessions")
	}
	if err := svc.Do(context.Background(), ResumeSession{SessionID: "target-session"}); err != nil {
		t.Fatalf("Do(ResumeSession): %v", err)
	}
	if got := svc.View().ResumeSession; got != "target-session" {
		t.Fatalf("view ResumeSession = %q, want target-session", got)
	}
	select {
	case ev := <-ch:
		if ev.Kind != events.KindActivity || ev.Text != "" {
			t.Fatalf("pulse = %+v; want an empty-text activity event", ev)
		}
	default:
		t.Fatal("Do(ResumeSession) published no event; an idle frontend would never see the request")
	}

	if err := svc.Do(context.Background(), ResumeSession{SessionID: "open-session"}); err == nil {
		t.Fatal("switching to the open session must be refused")
	}
	if err := svc.Do(context.Background(), ResumeSession{}); err == nil {
		t.Fatal("switching without a session id must be refused")
	}
}
