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

// SubmitTask sends human text to Austin.
type SubmitTask struct {
	Text string `json:"text"`
}

// EscalateToGoal asks the coordinator to move the session into Goal mode.
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

func (SubmitTask) Kind() string     { return "submit_task" }
func (EscalateToGoal) Kind() string { return "escalate_to_goal" }
func (SetModel) Kind() string       { return "set_model" }
func (CycleThinking) Kind() string  { return "cycle_thinking" }
func (RestartAgent) Kind() string   { return "restart_agent" }

func (SubmitTask) command()     {}
func (EscalateToGoal) command() {}
func (SetModel) command()       {}
func (CycleThinking) command()  {}
func (RestartAgent) command()   {}
