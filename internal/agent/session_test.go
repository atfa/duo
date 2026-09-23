package agent

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/protocol"
)

func TestSessionRunsInsidePTY(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s := NewSession(Config{
		Agent: protocol.Austin, Dir: t.TempDir(), Host: "127.0.0.1", Port: "1",
		Session: "session-1", Token: "secret",
		Command: `printf 'duo-pty-ok:%s:%s:%s\n' "$DUO_ACTIVE" "$DUO_SESSION" "$DUO_TOKEN"`,
	})
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.stopped:
	case <-ctx.Done():
		t.Fatal("session did not exit")
	}
	if s.Running() {
		t.Fatal("session still reported running after normal exit")
	}
	s.mu.RLock()
	got := append([]byte(nil), s.recent...)
	s.mu.RUnlock()
	if !bytes.Contains(got, []byte("duo-pty-ok:1:session-1:secret")) {
		t.Fatalf("PTY output missing marker: %q", strings.TrimSpace(string(got)))
	}
}
