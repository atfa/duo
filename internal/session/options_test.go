package session

import (
	"testing"

	"github.com/atfa/duo/internal/protocol"
)

func TestRestoreDriverDropsCommandInheritedFromDefaultDriver(t *testing.T) {
	opts := Options{
		DefaultAgentDriver:  "pi",
		AgentDrivers:        map[protocol.AgentID]string{protocol.Austin: "pi"},
		AgentCommands:       map[protocol.AgentID]string{protocol.Austin: "pi-custom"},
		AgentCommandDefault: map[protocol.AgentID]bool{protocol.Austin: true},
	}
	opts.restoreAgentDriver(protocol.Austin, "codex")
	if got := opts.AgentCommands[protocol.Austin]; got != "" {
		t.Fatalf("inherited command after restoring Codex = %q", got)
	}
	if got := opts.AgentDriver(protocol.Austin); got != "codex" {
		t.Fatalf("restored driver = %q, want codex", got)
	}
}

func TestRestoreDriverKeepsExplicitCommand(t *testing.T) {
	opts := Options{
		DefaultAgentDriver: "pi",
		AgentDrivers:       map[protocol.AgentID]string{protocol.Austin: "pi"},
		AgentCommands:      map[protocol.AgentID]string{protocol.Austin: "codex-wrapper"},
	}
	opts.restoreAgentDriver(protocol.Austin, "codex")
	if got := opts.AgentCommands[protocol.Austin]; got != "codex-wrapper" {
		t.Fatalf("explicit command after restore = %q", got)
	}
}

func TestDriverSwitchUsesNewDriverDefaultCommand(t *testing.T) {
	opts := Options{
		AgentCommands:  map[protocol.AgentID]string{protocol.Austin: "old-driver-wrapper"},
		AgentModels:    map[protocol.AgentID]string{protocol.Austin: "old/model"},
		AgentThinkings: map[protocol.AgentID]string{protocol.Austin: "old-effort"},
	}
	commands, models, thinkings := driverSwitchOptions(opts, protocol.Austin)
	if commands[protocol.Austin] != "" || models[protocol.Austin] != "" || thinkings[protocol.Austin] != "" {
		t.Fatalf("switch retained old driver settings: command=%q model=%q thinking=%q", commands[protocol.Austin], models[protocol.Austin], thinkings[protocol.Austin])
	}
	if opts.AgentCommands[protocol.Austin] != "old-driver-wrapper" {
		t.Fatal("preparing a switch mutated the current session options")
	}
}
