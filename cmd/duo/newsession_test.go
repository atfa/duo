package main

import (
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
)

func TestFreshSessionDropsTheResumedSession(t *testing.T) {
	cfg := config{resume: true, resumeSession: "20261023-132844-4ae2523f", session: "old", repository: "/repo", testCommand: "make check"}
	got := cfg.freshSession(nil)
	if got.resume || got.resumeSession != "" {
		t.Fatalf("fresh session still resumes: %+v", got)
	}
	if got.session == "" || got.session == "old" {
		t.Fatalf("fresh session id = %q, want a new one", got.session)
	}
	if got.repository != "/repo" || got.testCommand != "make check" {
		t.Fatalf("fresh session lost launch settings: %+v", got)
	}
	if !cfg.resume || cfg.session != "old" {
		t.Fatal("freshSession mutated the receiver")
	}
}

// /new keeps what the human changed during the session — mode, language, each
// agent's driver, model and thinking — and not the launch defaults.
func TestFreshSessionCarriesTheSettingsInForce(t *testing.T) {
	launchMaps := func() (map[protocol.AgentID]string, map[protocol.AgentID]string, map[protocol.AgentID]string) {
		return map[protocol.AgentID]string{protocol.Austin: "pi", protocol.Tony: "pi"},
			map[protocol.AgentID]string{protocol.Austin: "m-old"},
			map[protocol.AgentID]string{protocol.Austin: "low"}
	}
	drivers, models, thinkings := launchMaps()
	cfg := config{
		mode: project.ModeFast, language: "en", modeSource: "default",
		agentDrivers: drivers, agentModels: models, agentThinkings: thinkings,
		agentDriverExplicit: map[protocol.AgentID]bool{},
	}
	view := session.View{
		Language: "zh",
		Project:  project.Snapshot{Mode: project.ModeStep, Phase: project.PhaseExecute, Started: true, Plan: "old plan"},
		Agents: map[protocol.AgentID]session.AgentView{
			protocol.Austin: {Present: true, Driver: "codex", Model: "gpt-5", Thinking: "high"},
			protocol.Tony:   {Present: true, Driver: "pi"}, // no model or effort: the driver decides
		},
	}
	view.Mode = project.ModeStep

	got := cfg.freshSession(newSessionFromView(view))

	if got.mode != project.ModeStep || got.modeRaw != "step" || !got.modeExplicit {
		t.Fatalf("mode = %q raw=%q explicit=%v, want step carried", got.mode, got.modeRaw, got.modeExplicit)
	}
	if got.language != "zh" {
		t.Fatalf("language = %q, want zh", got.language)
	}
	if got.agentDrivers[protocol.Austin] != "codex" || !got.agentDriverExplicit[protocol.Austin] {
		t.Fatalf("austin driver = %q explicit=%v", got.agentDrivers[protocol.Austin], got.agentDriverExplicit[protocol.Austin])
	}
	if got.agentModels[protocol.Austin] != "gpt-5" || got.agentThinkings[protocol.Austin] != "high" {
		t.Fatalf("austin model/thinking = %q/%q", got.agentModels[protocol.Austin], got.agentThinkings[protocol.Austin])
	}
	if _, set := got.agentModels[protocol.Tony]; set {
		t.Fatal("an empty model must not be written down as a choice")
	}
	if cfg.agentDrivers[protocol.Austin] != "pi" || cfg.mode != project.ModeFast {
		t.Fatal("freshSession edited the launch configuration")
	}
	if got.session == "" || got.resume {
		t.Fatalf("session=%q resume=%v: a new idle session needs a new id and no resume", got.session, got.resume)
	}
}
