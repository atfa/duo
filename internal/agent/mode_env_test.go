package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/agent/agenttest"
	"github.com/atfa/duo/internal/protocol"
)

// TestSessionExportsModeForThePiExtension keeps the Go/TypeScript contract: the
// session's real mode reaches the adapter as DUO_MODE and the driver as the launch
// request's Mode. Every mode Core knows is exported as itself, because the
// extension registers its tools from this value and a folded mode is a tool set
// nobody can use — folding step into goal left a STEP session without
// duo_set_steps and deadlocked PLAN. An absent mode is still the legacy Goal rule,
// and an unknown value must never be forwarded as-is.
func TestSessionExportsModeForThePiExtension(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want string
	}{
		{"fast", "fast"},
		{"goal", "goal"},
		{"step", "step"},
		{"", "goal"},
		{"bogus", "goal"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		stub := agenttest.New("stub")
		s, err := NewSession(ctx, Config{
			Agent: protocol.Austin, Mode: tc.mode, Dir: t.TempDir(), Host: "127.0.0.1", Port: "1",
			Session: "session-mode", Token: "token",
			BaseCommand: `printf 'duo-mode=%s\n' "$DUO_MODE"`,
			Plugin:      stub.Caller(),
		})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
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
		// The driver must be told the same mode the adapter is. The two arriving as
		// different values is exactly what let a STEP session look like a Goal
		// session to the parts that decide what the agent may do.
		prepares := stub.Prepares()
		if len(prepares) == 0 {
			t.Fatalf("mode %q: the driver received no launch request", tc.mode)
		}
		if driverMode := prepares[len(prepares)-1].Mode; driverMode != tc.want {
			t.Errorf("Mode %q handed the driver %q, want %q", tc.mode, driverMode, tc.want)
		}
	}
}

func TestSessionExportsSelectedLanguage(t *testing.T) {
	s := mustSession(t, "stub", Config{Agent: protocol.Austin, Mode: "goal", Language: "zh", Dir: t.TempDir()})
	for _, entry := range coreEnv(s) {
		if entry == "DUO_LANGUAGE=zh" {
			return
		}
	}
	t.Fatal("agent environment does not contain DUO_LANGUAGE=zh")
}
