package session

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atfa/duo/internal/agent/agenttest"
	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
)

// These tests drive a whole round through session.Service: the composition root
// (drivers, bridge, coordinator, harness, delivery) is the real one, and only the
// agents are fakes. A fake driver hands the test the bridge credentials its agent
// process was launched with, so the test speaks the same protocol a real agent
// does.

// e2eRound is one started session plus the fake agents that drive it.
type e2eRound struct {
	svc     *Service
	repo    string
	baseDir string
	envDir  string
	session string
	agents  []*bridgeAgent
}

// startSessionRound starts a session in the requested mode with a stub driver
// whose command is a fake agent that keeps its process open.
func startSessionRound(t *testing.T, ctx context.Context, mode project.Mode) *e2eRound {
	t.Helper()
	repo := gitRepo(t)
	baseDir := t.TempDir()
	envDir := t.TempDir()
	script := writeFakeBridgeAgent(t, envDir)
	sessionID := "e2e-" + mode.String()

	opts := Options{
		LaunchDir:          repo,
		BaseDir:            baseDir,
		Session:            sessionID,
		Listen:             "127.0.0.1:0",
		Mode:               mode,
		DefaultAgentDriver: "stub",
		HarnessEnabled:     false,
		RegisterDrivers: func() {
			driver.Register("stub", func() driver.Handler {
				return agenttest.New("stub").With(func(spec *agenttest.Spec) {
					spec.Command = script
				})
			})
		},
	}

	svc := New(opts)
	if _, err := svc.Start(ctx); err != nil {
		t.Fatalf("start session: %v", err)
	}
	round := &e2eRound{svc: svc, repo: repo, baseDir: baseDir, envDir: envDir, session: sessionID}
	t.Cleanup(func() {
		for _, agent := range round.agents {
			_ = agent.Close()
		}
		svc.Close()
	})
	return round
}

// writeFakeBridgeAgent writes the agent process: it records the environment Duo
// gave it (which is how the test learns the bridge credentials) and then holds the
// process open while draining its terminal, exactly as a real agent CLI would.
func writeFakeBridgeAgent(t *testing.T, envDir string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-bridge-agent.sh")
	script := fmt.Sprintf("#!/bin/sh\nenv > %s/\"$DUO_AGENT\".env\nexec cat >/dev/null\n", envDir)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// bridgeAgent is a test agent on the real bridge: the same JSON messages a driver
// adapter sends, over the same transport.
type bridgeAgent struct {
	conn net.Conn
	enc  *json.Encoder
	dec  *json.Decoder
	mu   sync.Mutex
	seq  atomic.Uint64
}

// dial connects this agent the way a real agent does, using the environment the
// session launched its process with.
func (r *e2eRound) dial(t *testing.T, agent protocol.AgentID) *bridgeAgent {
	t.Helper()
	env := readAgentEnv(t, r.envDir, agent)
	addr := net.JoinHostPort(env["DUO_HOST"], env["DUO_PORT"])
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial bridge for %s: %v", agent, err)
	}
	client := &bridgeAgent{conn: conn, enc: json.NewEncoder(conn), dec: json.NewDecoder(conn)}
	if err := client.enc.Encode(protocol.Message{
		Version:   protocol.Version,
		Type:      protocol.MsgHello,
		Agent:     agent,
		SessionID: env["DUO_SESSION"],
		Token:     env["DUO_TOKEN"],
	}); err != nil {
		t.Fatalf("hello for %s: %v", agent, err)
	}
	r.agents = append(r.agents, client)
	return client
}

// readAgentEnv waits for the fake agent process to record its environment.
func readAgentEnv(t *testing.T, envDir string, agent protocol.AgentID) map[string]string {
	t.Helper()
	path := filepath.Join(envDir, string(agent)+".env")
	waitUntil(t, func() bool {
		_, err := os.Stat(path)
		return err == nil
	}, "the fake agent to record its environment")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			env[key] = value
		}
	}
	return env
}

func (c *bridgeAgent) Close() error { return c.conn.Close() }

// request sends one protocol message and waits for its own response.
func (c *bridgeAgent) request(message protocol.Message) (protocol.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	message.RequestID = fmt.Sprintf("e2e-%d", c.seq.Add(1))
	if err := c.enc.Encode(message); err != nil {
		return protocol.Message{}, err
	}
	if err := c.conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return protocol.Message{}, err
	}
	defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }()
	for {
		var response protocol.Message
		if err := c.dec.Decode(&response); err != nil {
			return protocol.Message{}, err
		}
		if response.Type != protocol.MsgResponse || response.RequestID != message.RequestID {
			continue
		}
		if !response.OK {
			return response, fmt.Errorf("%s refused: %s", message.Type, response.Text)
		}
		return response, nil
	}
}

func (c *bridgeAgent) mustRequest(t *testing.T, message protocol.Message) {
	t.Helper()
	if response, err := c.request(message); err != nil {
		t.Fatalf("%s: %v", message.Type, err)
	} else if response.Type != protocol.MsgResponse {
		t.Fatalf("%s answered %+v", message.Type, response)
	}
}

func (c *bridgeAgent) sign(t *testing.T) {
	t.Helper()
	ready := true
	c.mustRequest(t, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetStatus, Ready: &ready, Note: "ok"})
}

// waitUntil polls a condition until it holds or the deadline passes, and names
// what was awaited when it does not.
func waitUntil(t *testing.T, condition func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// waitPhase waits until the session's own projection reports a phase.
func (r *e2eRound) waitPhase(t *testing.T, phase project.Phase) {
	t.Helper()
	waitUntil(t, func() bool { return r.svc.View().Project.Phase == phase }, "phase "+string(phase))
}

// waitConnected waits for both agents' bridge connections.
func (r *e2eRound) waitConnected(t *testing.T) {
	t.Helper()
	waitUntil(t, func() bool {
		view := r.svc.View()
		return view.Connected(protocol.Austin) && view.Connected(protocol.Tony)
	}, "both agents to connect")
}

// snapshot reads the durable record the session wrote.
func (r *e2eRound) snapshot(t *testing.T) sessionstore.Snapshot {
	t.Helper()
	store := openSessionStore(t, r.baseDir, r.repo, r.session)
	snap, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// commitInWorktree writes and commits a file in one of the session's worktrees.
func commitInWorktree(t *testing.T, dir, name, content, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "-q", "-m", message)
	return headOf(t, dir)
}

func headOf(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD in %s: %v", dir, err)
	}
	return strings.TrimSpace(string(out))
}

// TestSessionServiceFastRoundDeliversWithFakeDriver is a whole Fast round through
// the composition root: Austin's verified HEAD is fast-forwarded into the user's
// repository and the durable record says so, without any frontend driving it.
func TestSessionServiceFastRoundDeliversWithFakeDriver(t *testing.T) {
	ctx := context.Background()
	round := startSessionRound(t, ctx, project.ModeFast)
	austin := round.dial(t, protocol.Austin)
	tony := round.dial(t, protocol.Tony)
	round.waitConnected(t)

	if view := round.svc.View(); view.Project.Phase != project.PhaseRunning {
		t.Fatalf("fast session starts in %s, want RUNNING", view.Project.Phase)
	}
	austinHead := commitInWorktree(t, round.svc.View().Worktrees.Austin.Path, "result.md", "hello Duo\n", "add result.md")

	// Austin requests verification; the request is bound to his exact HEAD.
	austin.sign(t)
	round.waitPhase(t, project.PhaseVerify)
	if got := round.svc.View().Project.Verification.Head; got != austinHead {
		t.Fatalf("verification target = %s, want Austin's HEAD %s", got, austinHead)
	}

	tony.mustRequest(t, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetVerification, Verification: "passed", Note: "looks right"})
	round.waitPhase(t, project.PhaseDone)

	view := round.svc.View()
	if !view.Delivery.Applied() {
		t.Fatalf("delivery = %+v, want applied", view.Delivery)
	}
	if got := view.Delivery.AppliedHead; got != austinHead {
		t.Fatalf("applied head = %s, want %s", got, austinHead)
	}
	if got := headOf(t, round.repo); got != austinHead {
		t.Fatalf("the user's repository is at %s, want the delivered HEAD %s", got, austinHead)
	}

	snap := round.snapshot(t)
	if snap.Phase != string(project.PhaseDone) || !snap.Delivery.Applied() {
		t.Fatalf("durable record = phase %s delivery %+v", snap.Phase, snap.Delivery)
	}
	if snap.SchemaVersion != sessionstore.SchemaVersion {
		t.Fatalf("snapshot schema = %d, want %d", snap.SchemaVersion, sessionstore.SchemaVersion)
	}
}

// TestSessionServiceGoalRoundDelivers drives a Goal round: shared plan, dual
// sign-off at every gate, Tony's work integrated into Austin's, then delivery of
// the integrated HEAD.
func TestSessionServiceGoalRoundDelivers(t *testing.T) {
	ctx := context.Background()
	round := startSessionRound(t, ctx, project.ModeGoal)
	austin := round.dial(t, protocol.Austin)
	tony := round.dial(t, protocol.Tony)
	round.waitConnected(t)

	austin.mustRequest(t, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetPlan, Plan: "Create result.md containing hello Duo"})
	austin.sign(t)
	tony.sign(t)
	round.waitPhase(t, project.PhaseExecute)

	worktrees := round.svc.View().Worktrees
	commitInWorktree(t, worktrees.Austin.Path, "result.md", "hello Duo\n", "add result.md")
	commitInWorktree(t, worktrees.Tony.Path, "tony-draft.md", "scratch\n", "tony draft")
	austin.sign(t)
	tony.sign(t)
	round.waitPhase(t, project.PhaseReview)

	austin.sign(t)
	tony.sign(t)
	round.waitPhase(t, project.PhaseIntegrate)

	austin.sign(t)
	tony.sign(t)
	round.waitPhase(t, project.PhaseDone)

	view := round.svc.View()
	if !view.Delivery.Applied() {
		t.Fatalf("delivery = %+v, want applied", view.Delivery)
	}
	head := headOf(t, round.repo)
	if head != view.Delivery.AppliedHead {
		t.Fatalf("the user's repository is at %s, want the delivered HEAD %s", head, view.Delivery.AppliedHead)
	}
	// The integrated artifact contains both agents' work.
	for _, file := range []string{"result.md", "tony-draft.md"} {
		if _, err := os.Stat(filepath.Join(round.repo, file)); err != nil {
			t.Fatalf("delivered repository is missing %s: %v", file, err)
		}
	}
	if snap := round.snapshot(t); snap.Phase != string(project.PhaseDone) || !snap.Delivery.Applied() {
		t.Fatalf("durable record = phase %s delivery %+v", snap.Phase, snap.Delivery)
	}
}

// TestSessionServiceStepQueueLandsAllSteps drives a two-step queue: each step
// lands on its own, the cursor advances, and the finished queue ends in DONE.
func TestSessionServiceStepQueueLandsAllSteps(t *testing.T) {
	ctx := context.Background()
	round := startSessionRound(t, ctx, project.ModeStep)
	austin := round.dial(t, protocol.Austin)
	tony := round.dial(t, protocol.Tony)
	round.waitConnected(t)

	austin.mustRequest(t, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetPlan, Plan: "Add two files, one step each"})
	austin.mustRequest(t, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetSteps, Steps: "- [ ] add first.md\n- [ ] add second.md"})
	austin.sign(t)
	tony.sign(t)
	round.waitPhase(t, project.PhaseExecute)

	austinPath := round.svc.View().Worktrees.Austin.Path
	steps := []struct{ file, content, message string }{
		{"first.md", "first\n", "add first.md"},
		{"second.md", "second\n", "add second.md"},
	}

	var landed []string
	for i, step := range steps {
		// Each step is committed, submitted and verified on its own: the queue
		// lands one reviewed HEAD at a time, never the whole branch at once.
		head := commitInWorktree(t, austinPath, step.file, step.content, step.message)
		landed = append(landed, head)

		austin.sign(t)
		round.waitPhase(t, project.PhaseReview)
		tony.mustRequest(t, protocol.Message{Version: protocol.Version, Type: protocol.MsgSetVerification, Verification: "passed", Note: "ok"})

		// The final step of a queue is gated by the dual INTEGRATE sign-off even
		// when its policy is fast, which is the queue rule this pins.
		if i == len(steps)-1 {
			round.waitPhase(t, project.PhaseIntegrate)
			austin.sign(t)
			tony.sign(t)
			round.waitPhase(t, project.PhaseDone)
		}

		waitUntil(t, func() bool {
			snap := round.svc.View().Project
			return len(snap.Steps) > i && snap.Steps[i].Done
		}, fmt.Sprintf("step %d to land", i+1))

		snap := round.svc.View().Project
		if got := snap.Steps[i].DeliveryHead; got != head {
			t.Fatalf("step %d delivery head = %q, want %q", i+1, got, head)
		}
		if got := headOf(t, round.repo); got != head {
			t.Fatalf("after step %d the user's repository is at %s, want %s", i+1, got, head)
		}
		if i < len(steps)-1 {
			round.waitPhase(t, project.PhaseExecute)
			if got := round.svc.View().Project.Step; got != i+2 {
				t.Fatalf("cursor after step %d = %d, want %d", i+1, got, i+2)
			}
		}
	}

	round.waitPhase(t, project.PhaseDone)
	view := round.svc.View()
	if !project.QueueFinished(view.Project.Steps) {
		t.Fatalf("queue is not finished: %+v", view.Project.Steps)
	}
	if !view.Delivery.Applied() {
		t.Fatalf("delivery = %+v, want applied", view.Delivery)
	}
	if got := headOf(t, round.repo); got != landed[len(landed)-1] {
		t.Fatalf("the user's repository is at %s, want the last landed step %s", got, landed[len(landed)-1])
	}
	if snap := round.snapshot(t); snap.Phase != string(project.PhaseDone) {
		t.Fatalf("durable record phase = %s, want DONE", snap.Phase)
	}
}
