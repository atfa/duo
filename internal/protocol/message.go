package protocol

type Message struct {
	Version int         `json:"version"`
	Type    MessageType `json:"type"`

	RequestID string `json:"requestId,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Token     string `json:"token,omitempty"`

	Agent      AgentID `json:"agent,omitempty"`
	From       AgentID `json:"from,omitempty"`
	To         AgentID `json:"to,omitempty"`
	ClientType string  `json:"clientType,omitempty"`

	Text string `json:"text,omitempty"`
	Plan string `json:"plan,omitempty"`
	Note string `json:"note,omitempty"`

	// Mode carries the target of a MsgModeChange. The adapters keep their
	// mode-dependent prompt current without restarting the agent process.
	Mode string `json:"mode,omitempty"`

	// Steps carries the raw Markdown checkbox list of a Step-mode queue on a
	// MsgSetSteps. It stays raw text on the wire: parsing lives in one place
	// (internal/project), so an adapter is a pipe and cannot invent its own syntax.
	Steps string `json:"steps,omitempty"`

	// Install asks a MsgRestartDuo launcher to build and install from the delivered
	// base branch before resuming, instead of restarting the same binary.
	Install bool `json:"install,omitempty"`

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

	// ContextTokens/ContextWindow are Pi's estimated context usage; TokensPerSecond
	// is an approximate rate computed from streamed output deltas.
	ContextTokens   int     `json:"contextTokens,omitempty"`
	ContextWindow   int     `json:"contextWindow,omitempty"`
	TokensPerSecond float64 `json:"tokensPerSecond,omitempty"`

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
