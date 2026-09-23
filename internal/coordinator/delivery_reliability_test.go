package coordinator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

func TestDeliveryAppliedDoesNotRegressToPending(t *testing.T) {
	c := &Coordinator{}
	c.SetDelivery(sessionstore.Delivery{Status: sessionstore.DeliveryApplied, FinalHead: "final", AppliedHead: "final"})
	c.SetDelivery(sessionstore.Delivery{Status: sessionstore.DeliveryPending, FinalHead: "final"})
	if got := c.CurrentDelivery(); !got.Applied() || got.AppliedHead != "final" {
		t.Fatalf("delivery regressed: %+v", got)
	}
}

func TestDeliveryDoesNotTouchGitWhenPendingCheckpointFails(t *testing.T) {
	repo := initE2ERepo(t)
	baseHead := gitHead(t, repo)
	state := project.NewState()
	advanceToIntegrate(t, state)
	ws := workspace.NewGitManager(workspace.GitConfig{
		Repository: repo,
		Root:       filepath.Join(t.TempDir(), "worktrees"),
		Session:    "failing-checkpoint",
	})
	set, err := ws.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	writeAndCommit(t, set.Austin.Path, "result.md", "hello Duo\n", "add result")
	finalHead := gitHead(t, set.Austin.Path)
	c := New(nil, state, harness.NewTracker(), ws, events.NewBus())
	c.EnableDurability(Durability{
		Store: failingSnapshotStore{},
		Compose: func(p project.Snapshot) sessionstore.Snapshot {
			return sessionstore.Snapshot{SessionID: "test", Phase: string(p.Phase), Ready: p.Ready, Evidence: p.Evidence}
		},
	})
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		if _, _, err := state.SetReady(agent, true, "ok", finalHead); err != nil {
			t.Fatal(err)
		}
	}
	c.deliverFinal(context.Background(), state.Snapshot())
	if got := state.Snapshot().Phase; got != project.PhaseIntegrate {
		t.Fatalf("phase = %s, want INTEGRATE", got)
	}
	if got := gitHead(t, repo); got != baseHead {
		t.Fatalf("original HEAD changed to %s after checkpoint failure", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "result.md")); !os.IsNotExist(err) {
		t.Fatalf("result.md reached original repo despite checkpoint failure: %v", err)
	}
}

func advanceToIntegrate(t *testing.T, state *project.State) {
	t.Helper()
	if _, err := state.SetPlan(protocol.Austin, "plan"); err != nil {
		t.Fatal(err)
	}
	for state.Snapshot().Phase != project.PhaseIntegrate {
		for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
			if _, _, err := state.SetReady(agent, true, "ok", "final"); err != nil {
				t.Fatal(err)
			}
		}
	}
}

type failingSnapshotStore struct{}

func (failingSnapshotStore) Save(sessionstore.Snapshot) error { return errors.New("disk full") }
