package coordinator

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/recovery"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/transport"
	"github.com/atfa/duo/internal/workspace"
)

const e2eSession = "delivery-e2e"

// TestDeliveryEndToEndDeliversResultFile is the most important v0.4.1
// regression: a full PLAN → EXECUTE → REVIEW → INTEGRATE lifecycle that writes
// result.md must end with result.md present in the original repository, the
// project DONE, the delivery applied, and both final signatures preserved.
func TestDeliveryEndToEndDeliversResultFile(t *testing.T) {
	ctx := context.Background()

	repo := initE2ERepo(t)
	baseCommit := gitHead(t, repo)

	set, store, coord, state, server, addr := startE2E(t, ctx, repo)
	_ = coord
	_ = server

	austin := dialAgent(t, addr, protocol.Austin)
	defer austin.Close()
	tony := dialAgent(t, addr, protocol.Tony)
	defer tony.Close()
	go drain(austin)
	go drain(tony)
	waitFor(t, func() bool { return server.IsConnected(protocol.Austin) && server.IsConnected(protocol.Tony) }, "both agents to connect")

	// PLAN
	send(t, austin, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetPlan, Plan: "Create result.md containing hello Duo"})
	sign(t, austin)
	sign(t, tony)
	waitPhase(t, state, project.PhaseExecute)

	// EXECUTE: Austin produces the deliverable, Tony an independent commit.
	writeAndCommit(t, set.Austin.Path, "result.md", "hello Duo\n", "add result.md")
	writeAndCommit(t, set.Tony.Path, "tony-draft.md", "scratch\n", "tony draft")
	sign(t, austin)
	sign(t, tony)
	waitPhase(t, state, project.PhaseReview)

	// REVIEW
	beforeMerge := gitHead(t, set.Austin.Path)
	sign(t, austin)
	sign(t, tony)
	waitPhase(t, state, project.PhaseIntegrate)
	waitFor(t, func() bool { return gitHead(t, set.Austin.Path) != beforeMerge }, "Tony's work to be merged into Austin")
	finalHead := gitHead(t, set.Austin.Path)

	// INTEGRATE dual sign-off must deliver, then mark DONE.
	sign(t, austin)
	sign(t, tony)
	waitPhase(t, state, project.PhaseDone)

	if got := gitHead(t, repo); got != finalHead {
		t.Fatalf("original repository HEAD = %s, want the delivered %s", got, finalHead)
	}
	if got := gitBranch(t, repo); got != set.BaseBranch {
		t.Fatalf("original repository branch = %s, want %s", got, set.BaseBranch)
	}
	data, err := os.ReadFile(filepath.Join(repo, "result.md"))
	if err != nil {
		t.Fatalf("result.md is not in the directory the user launched Duo from: %v", err)
	}
	if string(data) != "hello Duo\n" {
		t.Fatalf("result.md = %q", data)
	}
	if baseCommit == finalHead {
		t.Fatal("test did not produce a new final HEAD")
	}

	snap := state.Snapshot()
	if !snap.Ready[protocol.Austin] || !snap.Ready[protocol.Tony] {
		t.Fatalf("DONE must keep both final signatures: %+v", snap.Ready)
	}
	if snap.Evidence[protocol.Austin] != finalHead || snap.Evidence[protocol.Tony] != finalHead {
		t.Fatalf("DONE evidence = %+v, want %s", snap.Evidence, finalHead)
	}

	persisted, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Phase != string(project.PhaseDone) {
		t.Fatalf("persisted phase = %s, want DONE", persisted.Phase)
	}
	if persisted.Delivery.Status != sessionstore.DeliveryApplied {
		t.Fatalf("persisted delivery = %+v, want applied", persisted.Delivery)
	}
	if persisted.Delivery.AppliedHead != finalHead {
		t.Fatalf("applied head = %s, want %s", persisted.Delivery.AppliedHead, finalHead)
	}
	if !persisted.Ready[protocol.Austin] || !persisted.Ready[protocol.Tony] {
		t.Fatalf("persisted DONE dropped final signatures: %+v", persisted.Ready)
	}
}

// TestDeliveryEndToEndStaysPendingWhenRepositoryChanged proves the safety half:
// if the user's repository changed while Duo worked, Duo preserves the user's
// work, stays in INTEGRATE with both signatures, records a pending delivery and
// never claims DONE.
func TestDeliveryEndToEndStaysPendingWhenRepositoryChanged(t *testing.T) {
	ctx := context.Background()

	repo := initE2ERepo(t)
	baseCommit := gitHead(t, repo)

	set, store, _, state, server, addr := startE2E(t, ctx, repo)

	austin := dialAgent(t, addr, protocol.Austin)
	defer austin.Close()
	tony := dialAgent(t, addr, protocol.Tony)
	defer tony.Close()
	go drain(austin)
	go drain(tony)
	waitFor(t, func() bool { return server.IsConnected(protocol.Austin) && server.IsConnected(protocol.Tony) }, "both agents to connect")

	send(t, austin, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetPlan, Plan: "Create result.md"})
	sign(t, austin)
	sign(t, tony)
	waitPhase(t, state, project.PhaseExecute)

	writeAndCommit(t, set.Austin.Path, "result.md", "hello Duo\n", "add result.md")
	writeAndCommit(t, set.Tony.Path, "tony-draft.md", "scratch\n", "tony draft")
	sign(t, austin)
	sign(t, tony)
	waitPhase(t, state, project.PhaseReview)

	beforeMerge := gitHead(t, set.Austin.Path)
	sign(t, austin)
	sign(t, tony)
	waitPhase(t, state, project.PhaseIntegrate)
	waitFor(t, func() bool { return gitHead(t, set.Austin.Path) != beforeMerge }, "Tony's work to be merged into Austin")

	// The user keeps working in their repository while Duo is in INTEGRATE.
	writeFile(t, repo, "user-work.md", "important uncommitted work\n")

	sign(t, austin)
	sign(t, tony)

	// The first delivery checkpoint is written as pending before the repository
	// is inspected; wait for the resolved reason so the assertions see the final
	// outcome rather than the in-flight checkpoint.
	waitFor(t, func() bool {
		delivery := coordDelivery(t, store)
		return delivery.Status == sessionstore.DeliveryPending && delivery.Reason != ""
	}, "a resolved pending delivery checkpoint")

	snap := state.Snapshot()
	if snap.Phase != project.PhaseIntegrate {
		t.Fatalf("phase = %s, want INTEGRATE while delivery is pending", snap.Phase)
	}
	if !snap.Ready[protocol.Austin] || !snap.Ready[protocol.Tony] {
		t.Fatalf("pending delivery must keep both signatures: %+v", snap.Ready)
	}
	if got := gitHead(t, repo); got != baseCommit {
		t.Fatalf("original HEAD moved to %s despite pending delivery", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "user-work.md")); err != nil {
		t.Fatalf("user's uncommitted work was disturbed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "result.md")); !os.IsNotExist(err) {
		t.Fatalf("result.md must not be delivered while pending: %v", err)
	}

	persisted, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Delivery.Status != sessionstore.DeliveryPending {
		t.Fatalf("persisted delivery = %+v, want pending", persisted.Delivery)
	}
	if !strings.Contains(persisted.Delivery.Reason, "uncommitted") {
		t.Fatalf("pending reason = %q", persisted.Delivery.Reason)
	}
}

// startE2E wires a real Coordinator, Store and transport server over a real Git
// repository with prepared worktrees.
func startE2E(t *testing.T, ctx context.Context, repo string) (
	workspace.Set,
	*sessionstore.Store,
	*Coordinator,
	*project.State,
	*transport.Server,
	string,
) {
	t.Helper()

	ws := workspace.NewGitManager(workspace.GitConfig{
		Repository: repo,
		Root:       filepath.Join(t.TempDir(), "worktrees"),
		Session:    e2eSession,
	})
	set, err := ws.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}

	store, err := sessionstore.New(t.TempDir(), "repo-e2e", e2eSession)
	if err != nil {
		t.Fatal(err)
	}

	token := "e2e-token"
	server := transport.NewServer("127.0.0.1:0", e2eSession, token)
	state := project.NewState()
	coord := New(server, state, harness.NewTracker(), ws, events.NewBus())
	coord.SetIntegration(workspace.IntegrationResult{
		AustinBranch: set.Austin.Branch,
		AustinPath:   set.Austin.Path,
		Head:         set.BaseCommit,
	})
	coord.EnableDurability(Durability{
		Store:  store,
		Events: store.OpenEvents(),
		Log:    store.OpenLog(),
		Compose: func(p project.Snapshot) sessionstore.Snapshot {
			return recovery.Compose(recovery.ComposeInput{
				DuoVersion:  "test",
				SessionID:   e2eSession,
				RepoID:      "repo-e2e",
				Repository:  set.Repository,
				BaseBranch:  set.BaseBranch,
				BaseCommit:  set.BaseCommit,
				CreatedAt:   time.Now().UTC(),
				Project:     p,
				Worktrees:   set,
				PiSessions:  map[protocol.AgentID]string{},
				Integration: coord.CurrentIntegration(),
				Delivery:    coord.CurrentDelivery(),
			})
		},
	})
	server.SetHandler(coord)

	go func() { _ = server.ListenAndServe(ctx) }()
	<-server.Ready()

	return set, store, coord, state, server, server.Addr()
}

func coordDelivery(t *testing.T, store *sessionstore.Store) sessionstore.Delivery {
	t.Helper()
	snap, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return snap.Delivery
}

func initE2ERepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	e2eRun(t, repo, "git", "init")
	e2eRun(t, repo, "git", "config", "user.email", "duo@example.invalid")
	e2eRun(t, repo, "git", "config", "user.name", "Duo Test")
	writeFile(t, repo, "README.md", "base\n")
	e2eRun(t, repo, "git", "add", "README.md")
	e2eRun(t, repo, "git", "commit", "-m", "base")
	return repo
}

func dialAgent(t *testing.T, addr string, agent protocol.AgentID) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(conn).Encode(protocol.Message{
		Version: protocol.Version, Type: protocol.MsgHello,
		Agent: agent, SessionID: e2eSession, Token: "e2e-token",
	}); err != nil {
		t.Fatal(err)
	}
	return conn
}

func drain(conn net.Conn) {
	buf := make([]byte, 4096)
	for {
		if _, err := conn.Read(buf); err != nil {
			return
		}
	}
}

func send(t *testing.T, conn net.Conn, message protocol.Message) {
	t.Helper()
	if err := json.NewEncoder(conn).Encode(message); err != nil {
		t.Fatal(err)
	}
}

func sign(t *testing.T, conn net.Conn) {
	t.Helper()
	ready := true
	send(t, conn, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetStatus, Ready: &ready, Note: "ok"})
}

func waitPhase(t *testing.T, state *project.State, phase project.Phase) {
	t.Helper()
	waitFor(t, func() bool { return state.Snapshot().Phase == phase }, "phase "+string(phase))
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeAndCommit(t *testing.T, dir, name, content, message string) {
	t.Helper()
	writeFile(t, dir, name, content)
	e2eRun(t, dir, "git", "add", name)
	e2eRun(t, dir, "git", "commit", "-m", message)
}

func gitHead(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(e2eRun(t, dir, "git", "rev-parse", "HEAD"))
}

func gitBranch(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(e2eRun(t, dir, "git", "symbolic-ref", "--short", "HEAD"))
}

// e2eRun runs a git command, retrying the transient `index.lock` contention
// that occurs when the coordinator refreshes worktree status while the test is
// also committing. This mirrors real agent/git contention and is not a Duo bug.
func e2eRun(t *testing.T, dir, command string, args ...string) string {
	t.Helper()
	var lastErr error
	var lastOut []byte
	for attempt := 0; attempt < 100; attempt++ {
		cmd := exec.Command(command, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err == nil {
			return string(out)
		}
		lastErr, lastOut = err, out
		if !strings.Contains(string(out), "index.lock") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s %v failed: %v\n%s", command, args, lastErr, lastOut)
	return ""
}
