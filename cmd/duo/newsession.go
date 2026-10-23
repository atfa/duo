package main

import (
	"maps"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
)

// newSessionRequest is what runWith returns when the human used /new: the
// current session was closed and preserved, and run starts a fresh one in this
// same process. It is an error so it travels the same path as every other way a
// session ends, and it carries the settings that were in force when the human
// asked, because "fresh" means no plan, queue, approvals or history — not the
// launch defaults the human has since changed.
//
// It is a read-only projection of the closed session's view. Nothing of the old
// project state (plan, steps, signatures, task history) is copied: the new
// session builds its own store, worktrees and an empty project from its new id.
type newSessionRequest struct {
	Mode      project.Mode
	Language  string
	Drivers   map[protocol.AgentID]string
	Models    map[protocol.AgentID]string
	Thinkings map[protocol.AgentID]string
}

func (*newSessionRequest) Error() string { return "duo new session requested" }

// newSessionFromView records the settings in force in a closed session.
func newSessionFromView(view session.View) *newSessionRequest {
	req := &newSessionRequest{
		Mode:      view.EffectiveMode(),
		Language:  view.Language,
		Drivers:   map[protocol.AgentID]string{},
		Models:    map[protocol.AgentID]string{},
		Thinkings: map[protocol.AgentID]string{},
	}
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		agent, ok := view.Agents[id]
		if !ok || !agent.Present {
			continue
		}
		req.Drivers[id] = agent.Driver
		req.Models[id] = agent.Model
		req.Thinkings[id] = agent.Thinking
	}
	return req
}

// freshSession is the configuration of the session /new starts: the launch
// configuration with the settings the human has changed since (mode, language,
// each agent's driver, model and thinking) taken from req, everything that
// pointed at an existing session cleared — a `duo resume <id>` launch must not
// reopen the session /new just preserved — and a new session id. The new
// session starts idle: nothing here submits a task or resumes work.
func (c config) freshSession(req *newSessionRequest) config {
	c.resume = false
	c.resumeSession = ""
	c.session = defaultSession()
	if req == nil {
		return c
	}
	if req.Mode != "" {
		c.mode = req.Mode
		c.modeRaw = req.Mode.String()
		c.modeSource = "previous session"
		c.modeExplicit = true
	}
	if req.Language != "" {
		c.language = req.Language
	}
	// The maps are cloned: the old session's service shares the launch config's
	// maps, and the new config must not be edited through it.
	c.agentDrivers = maps.Clone(c.agentDrivers)
	c.agentModels = maps.Clone(c.agentModels)
	c.agentThinkings = maps.Clone(c.agentThinkings)
	c.agentDriverExplicit = maps.Clone(c.agentDriverExplicit)
	if c.agentDrivers == nil {
		c.agentDrivers = map[protocol.AgentID]string{}
	}
	if c.agentModels == nil {
		c.agentModels = map[protocol.AgentID]string{}
	}
	if c.agentThinkings == nil {
		c.agentThinkings = map[protocol.AgentID]string{}
	}
	if c.agentDriverExplicit == nil {
		c.agentDriverExplicit = map[protocol.AgentID]bool{}
	}
	for id, drv := range req.Drivers {
		if drv == "" {
			continue
		}
		c.agentDrivers[id] = drv
		c.agentDriverExplicit[id] = true
		// Empty means "let the driver decide": it is not written down as a choice.
		if model := req.Models[id]; model != "" {
			c.agentModels[id] = model
		}
		if effort := req.Thinkings[id]; effort != "" {
			c.agentThinkings[id] = effort
		}
	}
	return c
}
