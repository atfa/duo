package main

import (
	"os"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/workspace"
)

// TestPluginEnvCarriesTheOperatorEnvironment pins what a plugin process is handed.
//
// A plugin is Core's own code running somewhere else, so it needs the same world
// the agent gets: the operator's HOME is where every one of these CLIs keeps its
// configuration and credentials, and PATH is how a plugin finds the CLI it wraps.
// Handing a plugin only Duo's own variables took both away, and the failure is
// silent and late: pi fell back to a hardcoded default model it has no
// credentials for, and probe reported a CLI missing that was sitting on PATH.
func TestPluginEnvCarriesTheOperatorEnvironment(t *testing.T) {
	r := &runtime{
		mode:      project.ModeFast,
		sessionID: "session-1",
		set:       workspace.Set{Repository: "/repo", ScopePath: "/repo"},
	}
	env := pluginEnv(r, protocol.Austin, "127.0.0.1", "1234", "tok")

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
	r := &runtime{
		mode:      project.ModeFast,
		sessionID: "session-1",
		set:       workspace.Set{Repository: "/repo"},
	}
	for _, entry := range pluginEnv(r, protocol.Austin, "h", "1", "t") {
		if entry == "DUO_AGENT=Austin" {
			return
		}
	}
	t.Fatal("an inherited DUO_AGENT survived; the plugin would think it is the wrong agent")
}
