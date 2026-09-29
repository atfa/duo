package protocol

type Message struct {
	Version int         `json:"version"`
	Type    MessageType `json:"type"`

	RequestID string `json:"requestId,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Token     string `json:"token,omitempty"`

	Agent AgentID `json:"agent,omitempty"`
	From  AgentID `json:"from,omitempty"`
	To    AgentID `json:"to,omitempty"`

	Text string `json:"text,omitempty"`
	Plan string `json:"plan,omitempty"`
	Note string `json:"note,omitempty"`

	// Provider and Model carry the target of a MsgSetModel request and the
	// reported model of a MsgModelState. They are separate because a model ID
	// may itself contain a slash.
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`

	// Thinking carries a Pi thinking level (off…max) on a MsgThinkingState.
	Thinking string `json:"thinking,omitempty"`

	// Tool and Detail carry work detail on an activity message: the tool that is
	// running with a compact argument or error summary, or the tail of the
	// assistant text currently streaming. They let Duo show what an agent is doing
	// without parsing the agent's full-screen PTY output.
	Tool   string `json:"tool,omitempty"`
	Detail string `json:"detail,omitempty"`

	Ready *bool `json:"ready,omitempty"`

	// Verification carries a structured Fast-mode verdict: "passed" or
	// "issue_found". The verifier never sends free text as a verdict; an
	// issue_found verdict must also carry a concrete Note.
	Verification string `json:"verification,omitempty"`

	Activity  ActivityType `json:"activity,omitempty"`
	Timestamp int64        `json:"timestamp,omitempty"`

	OK    bool   `json:"ok,omitempty"`
	State string `json:"state,omitempty"`
}
