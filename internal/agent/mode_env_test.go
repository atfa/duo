package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/protocol"
)

// TestSessionExportsModeForThePiExtension keeps the Go/TypeScript contract: the
// bridge must always observe DUO_MODE as exactly fast or goal. An absent mode is
// the legacy Goal rule, and an unknown value must never be forwarded as-is.
func TestSessionExportsModeForThePiExtension(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want string
	}{
		{"fast", "fast"},
		{"goal", "goal"},
		{"", "goal"},
		{"bogus", "goal"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		s := mustSession(t, "stub", Config{
			Agent: protocol.Austin, Mode: tc.mode, Dir: t.TempDir(), Host: "127.0.0.1", Port: "1",
			Session: "session-mode", Token: "token",
			BaseCommand: `printf 'duo-mode=%s\n' "$DUO_MODE"`,
		})
		if err := s.Start(ctx); err != nil {
			cancel()
			t.Fatal(err)
		}
		select {
		case <-s.stopped:
		case <-ctx.Done():
			cancel()
			t.Fatalf("mode %q: session did not exit", tc.mode)
		}
		s.mu.RLock()
		got := string(s.recent)
		s.mu.RUnlock()
		cancel()

		if !strings.Contains(got, "duo-mode="+tc.want) {
			t.Fatalf("Mode %q exported %q, want DUO_MODE=%s", tc.mode, strings.TrimSpace(got), tc.want)
		}
	}
}
