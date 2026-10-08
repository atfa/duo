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
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

// TestComposeSnapshotCarriesDriverStateVerbatim checks that Core persists arbitrary plugin state unchanged.
func TestComposeSnapshotCarriesDriverStateVerbatim(t *testing.T) {
	agyBlob := `{"conversationId":"conv-1","logFile":"/tmp/duo-agy-austin.log"}`
	customBlob := `{"cursor":{"opaque":7}}`
	s := &Service{
		opts:   Options{AgentDrivers: map[protocol.AgentID]string{protocol.Austin: "agy", protocol.Tony: "future-driver"}},
		repoID: "repo-1", sessionID: "session-1", createdAt: time.Unix(0, 0).UTC(),
		state: project.NewStateFor(project.ModeGoal), set: workspace.Set{},
		driverState: map[protocol.AgentID]sessionstore.DriverState{
			protocol.Austin: {Driver: "agy", State: json.RawMessage(agyBlob)},
			protocol.Tony:   {Driver: "future-driver", State: json.RawMessage(customBlob)},
		},
	}
	snap := s.composeSnapshot(nil)
	for agent, want := range map[protocol.AgentID]string{protocol.Austin: agyBlob, protocol.Tony: customBlob} {
		if got := snap.DriverStates[agent]; string(got.State) != want {
			t.Errorf("%s state = %+v, want %s", agent, got, want)
		}
	}
	if snap.AgentDrivers[protocol.Tony] != "future-driver" {
		t.Fatalf("driver name not persisted: %v", snap.AgentDrivers)
	}
}

// TestComposeSnapshotPrefersTheLiveDriverState checks that newly learned opaque state replaces the loaded state.
func TestComposeSnapshotPrefersTheLiveDriverState(t *testing.T) {
	mgr := agent.NewManager()
	stub := agenttest.New("stub").With(func(spec *agenttest.Spec) {
		spec.Command = "sh -c 'exit 0'"
		spec.State = []byte(`{"cursor":"learned"}`)
	})
	sess, err := agent.NewSession(context.Background(), agent.Config{Agent: protocol.Austin, Dir: t.TempDir(), Plugin: stub.Caller(), PluginState: []byte(`{"cursor":"old"}`)})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer sess.Stop()
	mgr.Add(sess)
	s := &Service{opts: Options{}, repoID: "repo-1", sessionID: "session-1", createdAt: time.Unix(0, 0).UTC(), state: project.NewStateFor(project.ModeGoal), set: workspace.Set{}, agents: mgr, driverState: map[protocol.AgentID]sessionstore.DriverState{}}
	if got := s.composeSnapshot(nil).DriverStates[protocol.Austin]; string(got.State) != `{"cursor":"learned"}` || got.Driver != "stub" {
		t.Fatalf("snapshot state = %+v, want driver's new opaque state", got)
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
	if have["DUO_LANGUAGE"] != "en" {
		t.Errorf("default DUO_LANGUAGE = %q, want en", have["DUO_LANGUAGE"])
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

func TestOptionsUsesFrontendSelectedDriverDefault(t *testing.T) {
	opts := Options{DefaultAgentDriver: "custom-default"}
	if got := opts.AgentDriver(protocol.Austin); got != "custom-default" {
		t.Fatalf("agent driver = %q, want frontend-selected default", got)
	}
	if got := opts.AgentDriver(protocol.Tony); got != "custom-default" {
		t.Fatalf("agent driver = %q, want frontend-selected default", got)
	}
}

func TestDriverSeedsReturnsOpaqueStateForResume(t *testing.T) {
	want := sessionstore.DriverState{Driver: "future-driver", State: json.RawMessage(`{"conversation":"saved"}`)}
	got := DriverSeeds(sessionstore.Snapshot{DriverStates: map[protocol.AgentID]sessionstore.DriverState{protocol.Tony: want}})
	if state := got[protocol.Tony]; state.Driver != want.Driver || string(state.State) != string(want.State) {
		t.Fatalf("resume seed = %+v, want opaque state %+v", state, want)
	}
}
