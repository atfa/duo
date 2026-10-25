package session

import (
	"time"

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
	Language  string
	Project   project.Snapshot
	Worktrees workspace.Set

	Delivery      sessionstore.Delivery
	ResumePending bool
	TestCommand   string
	Notices       []string

	// Notify is the resolved answer to "may Duo interrupt the human with a desktop
	// notification when the session needs them". It is resolved here, not in the
	// frontend, so every frontend honours one operator setting.
	Notify bool

	// Usage is the session's cost ledger: measured token and cost totals per agent
	// and per phase. It is nil while nothing has been measured, so a frontend shows no
	// cost at all rather than a cost of zero.
	Usage *sessionstore.UsageLedger

	// ContextWarnPercent is the resolved context occupancy at which Duo tells an
	// agent its window is nearly full: the frontend reads the number it must draw
	// with, rather than guessing the same constant. 0 means the warning is off.
	ContextWarnPercent int

	// Restart is non-empty when the session asked to continue on a new Duo binary.
	// The interface stops and the CLI exits with the upgrade code so a launcher
	// outside Duo can install and resume.
	Restart RestartRequest

	// NewSession is set when the human asked /new: quit preserving this session,
	// then re-run duo as a fresh session.
	NewSession bool

	// ResumeSession is the id the human picked in /sessions: quit preserving this
	// session, then resume that one. It is empty unless a switch was requested.
	ResumeSession string

	Agents   map[protocol.AgentID]AgentView
	Pause    PauseView
	Observer ObserverView
}

// ObserverView is the advisory observer's configuration as a frontend may see
// it.
//
// It has no key field by construction: a secret never enters a View, so a
// frontend that renders this struct cannot leak one. HasKey reports only that a
// key resolves; the frontends use it to say "saved on this machine" without ever
// reading the value.
type ObserverView struct {
	Enabled   bool
	Endpoint  string
	Model     string
	APIKeyEnv string
	// LastNotice and LastNoticeAt expose the most recent observer diagnostic,
	// without placing observer failures in the session event stream.
	LastNotice   string
	LastNoticeAt time.Time
	// HasKey reports that a key resolves from the environment, the apiKeyEnv
	// variable, or the owner-only key file.
	HasKey bool
	// KeyFile reports that ~/.duo/observer.key exists.
	KeyFile bool
	// EnvOverride reports that DUO_OBSERVER decides Enabled, so the environment
	// wins over what a config file or the interface saved.
	EnvOverride bool
	// EnvOverrideEndpoint and EnvOverrideModel are the same for the endpoint and
	// model, so an interface can say which field its save cannot change.
	EnvOverrideEndpoint bool
	EnvOverrideModel    bool
	// Configured reports that the observer was set up at least once, so a
	// frontend does not offer first-run onboarding again.
	Configured bool
	// Reports are the status reports the observer produced for this session,
	// newest first. The observer runs only when a human asks for one, so this is
	// a short history rather than a growing log, and it is view state only: a
	// report is never a Timeline entry and never part of the transcript.
	Reports []ObserverReport
	// Analyzing reports that a request is in flight, so a panel can say so
	// instead of looking frozen while a slow model answers.
	Analyzing bool
	// RequestedAt is when the in-flight request started.
	RequestedAt time.Time
	// PendingQuestion is the question the in-flight request is answering, and is
	// empty when the in-flight request is a status report. A panel uses it to say
	// what it is waiting for, and to keep a typed question visible while the model
	// works.
	PendingQuestion string
}

// ObserverReport is one status report and when it was produced.
//
// Question is empty for a status report, and carries the human's question when
// the text is the observer's answer to it. The history is one list because the
// panel shows both the same way, newest first.
type ObserverReport struct {
	At       time.Time
	Question string
	Text     string
}

type PauseState string

const (
	PauseRunning PauseState = "running"
	PausePausing PauseState = "pausing"
	PausePaused  PauseState = "paused"
)

type PauseView struct {
	State  PauseState
	Paused map[protocol.AgentID]bool
}

type DriverChoice struct {
	Name    string
	Shipped bool
}

// RestartRequest is one request to stop this Duo process so the session can
// continue on a new binary.
type RestartRequest struct {
	Reason  string
	Install bool
}

// EffectiveMode reports the mode actually in force, which for a resumed session
// is the persisted one rather than whatever the operator asked for.
func (v View) EffectiveMode() project.Mode { return v.Project.EffectiveMode() }

// Phase reports the current workflow phase.
func (v View) Phase() project.Phase { return v.Project.Phase }

// DriverChangeAllowed reports whether replacing a driver is safe at this point
// in the workflow: before the first task, while fully paused, or after DONE.
func (v View) DriverChangeAllowed() bool {
	return !v.Project.Started || v.Project.Phase == project.PhaseDone || v.Pause.State == PausePaused
}

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
