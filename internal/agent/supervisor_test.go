package agent

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/protocol"
)

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for !condition() {
		select {
		case <-deadline:
			t.Fatal("timed out")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}
func TestPTYInputResizeAndRestart(t *testing.T) {
	ctx := context.Background()
	m := NewManager()
	a := NewSession(Config{Agent: protocol.Austin, Dir: t.TempDir(), Session: "same", Command: `sh -c 'read line; printf "got:%s:%s\\n" "$line" "$DUO_AGENT"'`})
	b := NewSession(Config{Agent: protocol.Tony, Dir: t.TempDir(), Command: "sleep 30"})
	m.Add(a)
	m.Add(b)
	if err := m.ResizeAll(101, 35); err != nil {
		t.Fatal(err)
	}
	if err := m.StartAll(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.StopAll()
	if err := m.ResizeAll(120, 40); err != nil {
		t.Fatal(err)
	}
	if a.size.Cols != 120 || b.size.Cols != 120 {
		t.Fatal("resize not propagated")
	}
	if err := a.Write([]byte("hello\r")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return !a.Running() })
	if !bytes.Contains(a.Attach(&bytes.Buffer{}), []byte("got:hello:Austin")) {
		t.Fatalf("output: %q", a.recent)
	}
	a.Detach()
	if a.State() != ProcessExited {
		t.Fatal("not exited")
	}
	if err := m.Restart(ctx, protocol.Austin); err != nil {
		t.Fatal(err)
	}
	if !a.Running() || a.cfg.Session != "same" {
		t.Fatal("restart lost identity")
	}
	if err := m.Restart(ctx, protocol.Tony); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("running restart: %v", err)
	}
	a.Stop()
	b.Stop()
	waitFor(t, func() bool { return !a.Running() && !b.Running() })
}
