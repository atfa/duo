package coordinator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/recovery"
)

func TestLoopDurableEventsAndFinishWithoutDelivery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := startE2EWithMode(t, ctx, project.ModeLoop)
	a := r.dialAgent(t, protocol.Austin)
	b := r.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool { return r.server.IsConnected(protocol.Austin) && r.server.IsConnected(protocol.Tony) }, "agents")
	r.state.MarkStarted()
	go r.coord.RunLoop(ctx)
	// A peer who never registered a wait still receives a wake.
	request(t, a, protocol.Message{Version: 1, Type: protocol.MsgPeerMessage, Text: "hello"})
	b.awaitMessage(t, "LOOP event", func(m protocol.Message) bool {
		return m.Type == protocol.MsgDuoNotice && strings.Contains(m.Text, "LOOP event")
	})
	reply := request(t, b, protocol.Message{Version: 1, Type: protocol.MsgLoopWait})
	if !strings.Contains(reply.Text, "hello") {
		t.Fatalf("event response: %s", reply.Text)
	}
	deadline := time.Now().Add(2 * time.Second).UTC().Truncate(time.Second)
	request(t, a, protocol.Message{Version: 1, Type: protocol.MsgLoopWait, Text: deadline.Format(time.RFC3339), Note: "timer"})
	waitFor(t, func() bool {
		snap, err := r.store.Load()
		return err == nil && len(snap.Loop.Events[protocol.Austin]) > 0
	}, "persisted timer")
	saved, err := r.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	restored := project.NewStateFor(project.ModeGoal)
	if err := restored.Restore(recovery.ProjectSnapshot(saved)); err != nil {
		t.Fatal(err)
	}
	if len(restored.Snapshot().Loop.Events[protocol.Austin]) != 1 {
		t.Fatal("timer missing after restore")
	}
	// Worktree changes stay isolated and require neither a commit nor delivery.
	if err := os.WriteFile(filepath.Join(r.set.Austin.Path, "uncommitted"), []byte("work"), 0600); err != nil {
		t.Fatal(err)
	}
	head := gitHead(t, r.repo)
	request(t, a, protocol.Message{Version: 1, Type: protocol.MsgLoopFinish, Note: "complete"})
	request(t, b, protocol.Message{Version: 1, Type: protocol.MsgLoopFinish, Note: "complete"})
	saved, err = r.store.Load()
	if err != nil || saved.NeedsDelivery() {
		t.Fatalf("LOOP unexpectedly needs delivery: %+v, %v", saved, err)
	}
	if r.state.Snapshot().Phase != project.PhaseDone || gitHead(t, r.repo) != head {
		t.Fatal("finish delivered or failed")
	}
	// E2E fixture cleans its worktrees; remove only the file this test created.
	if err := os.Remove(filepath.Join(r.set.Austin.Path, "uncommitted")); err != nil {
		t.Fatal(err)
	}
}

func TestLoopResumeHoldKeepsPendingEventSilent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := startE2EWithMode(t, ctx, project.ModeLoop)
	a := r.dialAgent(t, protocol.Austin)
	b := r.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool { return r.server.IsConnected(protocol.Austin) && r.server.IsConnected(protocol.Tony) }, "agents")
	r.state.MarkStarted()
	r.coord.HoldResume()
	go r.coord.RunLoop(ctx)
	request(t, a, protocol.Message{Version: 1, Type: protocol.MsgPeerMessage, Text: "pending during resume"})
	if err := b.conn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	defer b.conn.SetReadDeadline(time.Time{})
	var msg protocol.Message
	if err := b.dec.Decode(&msg); err == nil {
		t.Fatalf("resume hold delivered %+v", msg)
	}
	if len(r.state.Snapshot().Loop.Events[protocol.Tony]) != 1 {
		t.Fatal("event not retained")
	}
}
