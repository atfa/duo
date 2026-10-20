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
}

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

// RestartDuo asks Duo to stop so the session can continue on a new Duo binary.
// It is the STEP-mode self-upgrade hook: the agent that asks for it is a child of
// the Duo it is upgrading, so it cannot do the relaunch itself.
type RestartDuo struct {
	Reason string `json:"reason"`
	// Install asks the launcher to build and install from the delivered base
	// branch before resuming. Without it the loop only restarts the same binary.
	Install bool `json:"install"`
}

func (SubmitTask) Kind() string     { return "submit_task" }
func (SwitchMode) Kind() string     { return "switch_mode" }
func (EscalateToGoal) Kind() string { return "escalate_to_goal" }
func (SetModel) Kind() string       { return "set_model" }
func (CycleThinking) Kind() string  { return "cycle_thinking" }
func (RestartAgent) Kind() string   { return "restart_agent" }
func (SetDriver) Kind() string      { return "set_driver" }
func (SetLanguage) Kind() string    { return "set_language" }
func (RestartDuo) Kind() string     { return "restart_duo" }

func (SubmitTask) command()     {}
func (SwitchMode) command()     {}
func (EscalateToGoal) command() {}
func (SetModel) command()       {}
func (CycleThinking) command()  {}
func (RestartAgent) command()   {}
func (SetDriver) command()      {}
func (SetLanguage) command()    {}
func (RestartDuo) command()     {}
