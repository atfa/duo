package session

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/agent/agenttest"
	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

// TestComposeSnapshotCarriesDriverStateVerbatim is the durability guarantee: what a
// plugin stored must come back out of the snapshot byte for byte, for any driver.
//
// Core cannot round-trip this itself — it does not know the blob's shape — so the
// test asserts equality rather than meaning. A driver that stores a conversation id
// and a driver that stores nothing at all are both correct here. The blob lives in
// DriverStates only: PiSessions is the bare downgrade mirror a v0.9.0 binary reads.
func TestComposeSnapshotCarriesDriverStateVerbatim(t *testing.T) {
	agyBlob := `{"conversationId":"conv-1","logFile":"/tmp/duo-agy-austin.log"}`
	piBlob := `{"sessionId":"0f2b7c1e-1111-4222-8333-444455556666"}`

	s := &Service{
		opts:      Options{AgentDrivers: map[protocol.AgentID]string{protocol.Austin: "agy", protocol.Tony: "pi"}},
		repoID:    "repo-1",
		sessionID: "session-1",
		createdAt: time.Unix(0, 0).UTC(),
		state:     project.NewStateFor(project.ModeGoal),
		set:       workspace.Set{},
		driverState: map[protocol.AgentID]sessionstore.DriverState{
			protocol.Austin: {Driver: "agy", State: json.RawMessage(agyBlob)},
			protocol.Tony:   {Driver: "pi", State: json.RawMessage(piBlob)},
		},
		piSessions: map[protocol.AgentID]string{
			protocol.Austin: "conv-1",
			protocol.Tony:   "0f2b7c1e-1111-4222-8333-444455556666",
		},
	}

	snap := s.composeSnapshot(nil)
	if got := snap.DriverStates[protocol.Austin]; got.Driver != "agy" || string(got.State) != agyBlob {
		t.Errorf("Austin state = %+v, want driver agy and the blob verbatim", got)
	}
	if got := snap.DriverStates[protocol.Tony]; got.Driver != "pi" || string(got.State) != piBlob {
		t.Errorf("Tony state = %+v, want driver pi and the blob verbatim", got)
	}
	// The mirror stays bare: a `{`-prefixed value here would be read by a v0.9.0
	// binary as an identity string and resume nothing.
	if got := snap.PiSessions[protocol.Austin]; got != "conv-1" {
		t.Errorf("Austin mirror = %q, want the bare identity", got)
	}
	if got := snap.PiSessions[protocol.Tony]; got != "0f2b7c1e-1111-4222-8333-444455556666" {
		t.Errorf("Tony mirror = %q, want the bare identity", got)
	}
	if snap.AgentDrivers[protocol.Austin] != "agy" || snap.AgentDrivers[protocol.Tony] != "pi" {
		t.Errorf("drivers = %v, want each agent's plugin recorded", snap.AgentDrivers)
	}
}

// TestComposeSnapshotPrefersTheLiveDriverState covers a restart: the state a plugin
// returned for the run that just happened must win over what was loaded, or a
// driver that learned its identity would keep replaying the previous one. The same
// run refills the downgrade mirror from the identity the driver reports.
func TestComposeSnapshotPrefersTheLiveDriverState(t *testing.T) {
	mgr := agent.NewManager()
	stub := agenttest.New("stub").With(func(spec *agenttest.Spec) {
		spec.Command = "sh -c 'exit 0'"
		spec.State = []byte(`{"sessionId":"learned"}`)
	})
	sess, err := agent.NewSession(context.Background(), agent.Config{
		Agent:       protocol.Austin,
		Dir:         t.TempDir(),
		Plugin:      stub.Caller(),
		PluginState: []byte(`{"sessionId":""}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer sess.Stop()
	mgr.Add(sess)

	s := &Service{
		opts:      Options{AgentDrivers: map[protocol.AgentID]string{protocol.Austin: "stub"}},
		repoID:    "repo-1",
		sessionID: "session-1",
		createdAt: time.Unix(0, 0).UTC(),
		state:     project.NewStateFor(project.ModeGoal),
		set:       workspace.Set{},
		agents:    mgr,
		driverState: map[protocol.AgentID]sessionstore.DriverState{
			protocol.Austin: {Driver: "stub", State: json.RawMessage(`{"sessionId":""}`)},
		},
		piSessions: map[protocol.AgentID]string{},
	}
	snap := s.composeSnapshot(nil)
	if got := snap.DriverStates[protocol.Austin]; string(got.State) != `{"sessionId":"learned"}` || got.Driver != "stub" {
		t.Errorf("snapshot state = %+v, want what the driver learned this run", got)
	}
	// The stub reports "test-session" as its identity; the mirror must carry that
	// bare value, never the blob.
	if got := snap.PiSessions[protocol.Austin]; got != "test-session" {
		t.Errorf("mirror = %q, want the driver's reported identity", got)
	}
}

// TestDriverSeedsKeepsBlobsAndBareIdentitiesApart pins the resume wiring: the
// authoritative blobs come from DriverStates, and only bare values enter the
// downgrade mirror. An M1-era blob sitting in PiSessions must not become a
// "v0.9.0 identity" — it is skipped until the driver reports one live.
func TestDriverSeedsKeepsBlobsAndBareIdentitiesApart(t *testing.T) {
	snap := sessionstore.Snapshot{
		DriverStates: map[protocol.AgentID]sessionstore.DriverState{
			protocol.Austin: {Driver: "agy", State: json.RawMessage(`{"conversationId":"conv-1"}`)},
			protocol.Tony:   {Driver: "pi", State: json.RawMessage(`{"sessionId":"pi-id"}`)},
		},
		PiSessions: map[protocol.AgentID]string{
			protocol.Austin: "conv-1",
			protocol.Tony:   `{"sessionId":"blob-era"}`,
		},
	}

	driverState, piSessions := DriverSeeds(snap)

	if got := driverState[protocol.Austin]; got.Driver != "agy" || string(got.State) != `{"conversationId":"conv-1"}` {
		t.Errorf("Austin blob = %+v, want the stored driver state", got)
	}
	if got := driverState[protocol.Tony]; got.Driver != "pi" || string(got.State) != `{"sessionId":"pi-id"}` {
		t.Errorf("Tony blob = %+v, want the stored driver state", got)
	}
	if got := piSessions[protocol.Austin]; got != "conv-1" {
		t.Errorf("Austin mirror = %q, want the bare identity", got)
	}
	if _, ok := piSessions[protocol.Tony]; ok {
		t.Errorf("Tony mirror must skip the blob: %q", piSessions[protocol.Tony])
	}
}

// TestPluginEnvCarriesTheOperatorEnvironment pins what a plugin process is handed.
//
// A plugin is Core's own code running somewhere else, so it needs the same world
// the agent gets: the operator's HOME is where every one of these CLIs keeps its
// configuration and credentials, and PATH is how a plugin finds the CLI it wraps.
// Handing a plugin only Duo's own variables took both away, and the failure is
// silent and late: pi fell back to a hardcoded default model it has no credentials
// for, and probe reported a CLI missing that was sitting on PATH.
func TestPluginEnvCarriesTheOperatorEnvironment(t *testing.T) {
	s := &Service{
		mode:      project.ModeFast,
		sessionID: "session-1",
		set:       workspace.Set{Repository: "/repo", ScopePath: "/repo"},
	}
	env := s.pluginEnv(protocol.Austin, "127.0.0.1", "1234", "tok")

	have := make(map[string]string, len(env))
	for _, entry := range env {
		if name, value, ok := strings.Cut(entry, "="); ok {
			have[name] = value
		}
	}

	for _, name := range []string{"HOME", "PATH"} {
		if have[name] == "" {
			t.Errorf("plugin environment has no %s; a plugin cannot read the operator's config or find its CLI", name)
		}
	}
	if want := os.Getenv("HOME"); have["HOME"] != "" && have["HOME"] != want {
		t.Errorf("HOME = %q, want the operator's %q", have["HOME"], want)
	}

	// Duo's own variables still win over anything inherited.
	if have["DUO_AGENT"] != "Austin" || have["DUO_PORT"] != "1234" {
		t.Errorf("Duo variables are not set: %v", have)
	}
}

// TestPluginEnvOverridesAnInheritedDuoVariable makes sure inheriting the
// environment does not let a stale value survive from the parent process.
func TestPluginEnvOverridesAnInheritedDuoVariable(t *testing.T) {
	t.Setenv("DUO_AGENT", "Tony")
	s := &Service{
		mode:      project.ModeFast,
		sessionID: "session-1",
		set:       workspace.Set{Repository: "/repo"},
	}
	for _, entry := range s.pluginEnv(protocol.Austin, "h", "1", "t") {
		if entry == "DUO_AGENT=Austin" {
			return
		}
	}
	t.Fatal("an inherited DUO_AGENT survived; the plugin would think it is the wrong agent")
}

// TestMirrorCarriesTheIdentityALiveDriverReported pins the downgrade guarantee at
// the only link that can break it. Every other test seeds the mirror by hand; this
// one lets a session report an identity the way a resume: server driver does after
// launch, and checks that it reaches the snapshot a v0.9.0 binary would read.
//
// A driver whose learned identity never reached the mirror looks fine everywhere
// else: its own resume works, DriverStates is authoritative, and the only visible
// symptom is that downgrading forks every conversation.
func TestMirrorCarriesTheIdentityALiveDriverReported(t *testing.T) {
	stub := agenttest.New("stub").With(func(s *agenttest.Spec) {
		s.Command = "sh -c 'exit 0'"
		s.Capabilities.Resume = driver.ResumeServer
		s.Capabilities.Bridge = driver.BridgeAgent
		s.State = []byte(`{"conversationId":"conv-learned-after-launch"}`)
		s.SessionIdentity = "conv-learned-after-launch"
	})
	sess, err := agent.NewSession(context.Background(), agent.Config{
		Agent:  protocol.Austin,
		Dir:    t.TempDir(),
		Plugin: stub.Caller(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer sess.Stop()

	manager := agent.NewManager()
	manager.Add(sess)
	s := &Service{
		opts:        Options{AgentDrivers: map[protocol.AgentID]string{protocol.Austin: "stub"}},
		repoID:      "repo-1",
		sessionID:   "session-1",
		createdAt:   time.Unix(0, 0).UTC(),
		state:       project.NewStateFor(project.ModeGoal),
		set:         workspace.Set{},
		agents:      manager,
		driverState: map[protocol.AgentID]sessionstore.DriverState{},
	}

	// The identity has to be the plugin's, which is what a server-assigned one is.
	if got := sess.SessionID(); got != "conv-learned-after-launch" {
		t.Fatalf("session identity = %q, want the one the plugin reported", got)
	}
	snap := s.composeSnapshot(nil)
	if got := snap.PiSessions[protocol.Austin]; got != "conv-learned-after-launch" {
		t.Fatalf("mirror = %q; a v0.9.0 binary would resume nothing", got)
	}
}
