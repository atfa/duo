package session

import (
	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

// AgentView is one agent's projection: everything a frontend may render about it.
//
// It is a value copy taken at one moment, and the slices and pointers inside it are
// copied rather than aliased, so a frontend cannot reach back through an AgentView
// and change the session another frontend is reading.
type AgentView struct {
	// Present reports that the session has a driver for this agent at all, which is
	// how a frontend tells "not configured" from "starting".
	Present      bool
	Connected    bool
	Process      agent.ProcessState
	Driver       string
	Command      string
	Model        string
	Thinking     string
	Manifest     *driver.Manifest
	Capabilities driver.Capabilities
	Runtime      harness.AgentRuntime
}

// View is the complete projection of a session at one moment.
//
// Everything a frontend needs to draw a session is here, and no handle to the
// workflow, the worktrees or an agent process is: consuming a View cannot start,
// stop, steer or sign anything. The struct is a shallow copy of some sub-values,
// so the slices a frontend iterates (`AgentView.Runtime.Recent`, `Notices`) are
// copied by construction where they could otherwise alias live state; the remaining
// shared values are immutable in practice once recorded.
type View struct {
	Version   string
	SessionID string
	Mode      project.Mode
	Project   project.Snapshot
	Worktrees workspace.Set

	Delivery    sessionstore.Delivery
	TestCommand string
	Notices     []string

	Agents map[protocol.AgentID]AgentView
}

// EffectiveMode reports the mode actually in force, which for a resumed session
// is the persisted one rather than whatever the operator asked for.
func (v View) EffectiveMode() project.Mode { return v.Project.EffectiveMode() }

// Phase reports the current workflow phase.
func (v View) Phase() project.Phase { return v.Project.Phase }

// Agent returns one agent's projection. A missing agent is the zero value, which
// reads as "not connected, not running, nothing known" — never as a fabricated
// agent.
func (v View) Agent(id protocol.AgentID) AgentView { return v.Agents[id] }

// Runtime returns one agent's runtime activity.
func (v View) Runtime(id protocol.AgentID) harness.AgentRuntime { return v.Agents[id].Runtime }

// Connected reports whether an agent currently holds the bridge endpoint.
func (v View) Connected(id protocol.AgentID) bool { return v.Agents[id].Connected }

// ProcessState returns one agent's process state.
func (v View) ProcessState(id protocol.AgentID) agent.ProcessState { return v.Agents[id].Process }
