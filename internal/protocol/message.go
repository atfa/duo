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

	Ready *bool `json:"ready,omitempty"`

	Activity  ActivityType `json:"activity,omitempty"`
	Timestamp int64        `json:"timestamp,omitempty"`

	OK    bool   `json:"ok,omitempty"`
	State string `json:"state,omitempty"`
}
