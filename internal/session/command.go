package session

import (
	"github.com/atfa/duo/internal/protocol"
)

// Command is one human verb a frontend may ask a session to perform.
//
// Every command is a value with JSON-tagged fields and no behavior, so a future
// out-of-process frontend can carry the same command over a wire without the seam
// being redesigned. Commands are deliberately limited to what the human actually
// does: the gate verbs that let an agent sign a phase (SetPlan, SetReady,
// SetVerification) are NOT here, because they are authorized by an agent's live
// bridge connection and nothing else may speak with an agent's authority.
type Command interface {
	// Kind is the stable discriminator a wire adapter would dispatch on.
	Kind() string
	command()
}

// SubmitTask sends human text to the coordinator, which routes it to Austin or
// to both agents depending on the current mode.
type SubmitTask struct {
	Text string `json:"text"`
}

// SwitchMode changes the session workflow at any time. The coordinator resets
// the current round and applies the new mode to the next task.
type SwitchMode struct {
	Mode   string `json:"mode"`
	Reason string `json:"reason,omitempty"`
	// Quiet applies the mode change without injecting a resume notice into the
	// agents. The interactive frontend uses this so simply changing mode does not
	// wake both agents before the human has written their next prompt.
	Quiet bool `json:"quiet,omitempty"`
}

// ResumeModeWork sends the current mode's work notice to both agents. The
// frontend runs it only after the human confirms that they want the agents to
// resume.
type ResumeModeWork struct{}

// ResumeWork explicitly resumes a restored session after the human has reviewed
// its history.
type ResumeWork struct{}

// Pause asks Duo to hold new work after agents reach idle boundaries.
type Pause struct{}

// Unpause cancels a pause request that has not completed yet.
type Unpause struct{}

// Continue resumes a fully paused session.
type Continue struct{}

// EscalateToGoal is the historical Fast → Goal command. New frontends use
// SwitchMode; this stays so existing clients keep working.
type EscalateToGoal struct {
	Reason string `json:"reason"`
}

// SetModel switches an agent's model.
type SetModel struct {
	Agent    protocol.AgentID `json:"agent"`
	Provider string           `json:"provider"`
	Model    string           `json:"model"`
}

// CycleThinking advances an agent's reasoning effort to the next level.
type CycleThinking struct {
	Agent protocol.AgentID `json:"agent"`
}

// RestartAgent restarts an agent's process.
type RestartAgent struct {
	Agent protocol.AgentID `json:"agent"`
}

// SetDriver changes the executable behind one agent while preserving its Duo
// worktree and workflow identity.
type SetDriver struct {
	Agent  protocol.AgentID `json:"agent"`
	Driver string           `json:"driver"`
}

// SetLanguage selects the language Duo writes its own messages in, and records
// it in the project configuration so later sessions start in it.
type SetLanguage struct {
	Language string `json:"language"`
}

// SetObserver applies the human's advisory-observer settings and makes them
// live.
//
// The API key is deliberately absent: a frontend with a key saves it in the
// owner-only key file first, and the rebuilt observer reads it from there, so a
// secret never travels through this seam or a session log. A blank APIKeyEnv
// keeps the configured variable name.
type SetObserver struct {
	Enabled   bool   `json:"enabled"`
	Endpoint  string `json:"endpoint"`
	Model     string `json:"model"`
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`
}

// RestartDuo asks Duo to stop so the session can continue on a new Duo binary.
// It is the STEP-mode self-upgrade hook: the agent that asks for it is a child of
// the Duo it is upgrading, so it cannot do the relaunch itself.
type RestartDuo struct {
	Reason string `json:"reason"`
	// Install asks the launcher to build and install from the delivered base
	// branch before resuming. Without it the loop only restarts the same binary.
	Install bool `json:"install"`
}

// NewSession asks Duo to quit and be re-run as a fresh session. It is the /new
// command: Ctrl+Q semantics (the session is preserved) followed by running duo
// again, in one action. Like RestartDuo the request is in-memory: it is an
// instruction to the process that is about to exit, not durable state.
type NewSession struct{}

func (SubmitTask) Kind() string     { return "submit_task" }
func (SwitchMode) Kind() string     { return "switch_mode" }
func (ResumeModeWork) Kind() string { return "resume_mode_work" }
func (ResumeWork) Kind() string     { return "resume_work" }
func (Pause) Kind() string          { return "pause" }
func (Unpause) Kind() string        { return "unpause" }
func (Continue) Kind() string       { return "continue" }
func (EscalateToGoal) Kind() string { return "escalate_to_goal" }
func (SetModel) Kind() string       { return "set_model" }
func (CycleThinking) Kind() string  { return "cycle_thinking" }
func (RestartAgent) Kind() string   { return "restart_agent" }
func (SetDriver) Kind() string      { return "set_driver" }
func (SetLanguage) Kind() string    { return "set_language" }
func (SetObserver) Kind() string    { return "set_observer" }
func (RestartDuo) Kind() string     { return "restart_duo" }
func (NewSession) Kind() string     { return "new_session" }

func (SubmitTask) command()     {}
func (SwitchMode) command()     {}
func (ResumeModeWork) command() {}
func (ResumeWork) command()     {}
func (Pause) command()          {}
func (Unpause) command()        {}
func (Continue) command()       {}
func (EscalateToGoal) command() {}
func (SetModel) command()       {}
func (CycleThinking) command()  {}
func (RestartAgent) command()   {}
func (SetDriver) command()      {}
func (SetLanguage) command()    {}
func (SetObserver) command()    {}
func (RestartDuo) command()     {}
func (NewSession) command()     {}
