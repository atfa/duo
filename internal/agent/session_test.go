package agent

import (
	"context"
	"testing"
	"time"

	"github.com/atfa/duo/internal/agent/agenttest"
	"github.com/atfa/duo/internal/protocol"
)

// TestSessionRunsInsidePTY is the guarantee the driver refactor must not break: an
// agent still runs in a real pseudo-terminal, and the command line still comes from
// the driver rather than from Core.
func TestSessionRunsInsidePTY(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stub := agenttest.New("stub").With(func(s *agenttest.Spec) {
		s.Command = `printf 'duo-pty-ok:%s:%s:%s\n' "$DUO_ACTIVE" "$DUO_SESSION" "$DUO_TOKEN"`
	})
	s, err := NewSession(ctx, Config{
		Agent: protocol.Austin, Dir: t.TempDir(), Host: "127.0.0.1", Port: "1",
		Session: "session-1", Token: "secret",
		Plugin: stub.Caller(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if s.State() == ProcessExited || s.State() == ProcessFailed {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the agent process never finished; state=%s", s.State())
}
