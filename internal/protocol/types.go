package protocol

import "strings"

type AgentID string

const (
	Austin AgentID = "Austin"
	Tony   AgentID = "Tony"
	Duo    AgentID = "Duo"
)

func CanonicalAgent(value string) AgentID {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "austin":
		return Austin
	case "tony":
		return Tony
	case "duo":
		return Duo
	default:
		return AgentID(strings.TrimSpace(value))
	}
}

func PeerOf(agent AgentID) AgentID {
	switch agent {
	case Austin:
		return Tony
	case Tony:
		return Austin
	default:
		return ""
	}
}

type MessageType string

// Version is the Duo bridge wire-protocol version. It is independent of the Duo
// binary version and must match pi-extension/protocol.ts (PROTOCOL_VERSION).
// The server rejects any hello whose version does not match exactly.
const Version = 1

const (
	MsgHello            MessageType = "hello"
	MsgTest             MessageType = "test"
	MsgActivity         MessageType = "activity"
	MsgAssistantMessage MessageType = "assistant_message"
	MsgAgentError       MessageType = "agent_error"
	MsgPeerMessage      MessageType = "peer_message"
	MsgSteer            MessageType = "steer"
	MsgDuoNotice        MessageType = "duo_notice"
	MsgHarnessPrompt    MessageType = "harness_prompt"
	MsgHumanPrompt      MessageType = "human_prompt"
	MsgSetPlan          MessageType = "set_plan"
	MsgSetStatus        MessageType = "set_status"
	MsgGetStatus        MessageType = "get_status"
	MsgResponse         MessageType = "response"
)

type ActivityType string

const (
	ActivityAgentStart    ActivityType = "agent_start"
	ActivityAgentSettled  ActivityType = "agent_settled"
	ActivityProviderStart ActivityType = "provider_start"
	ActivityProviderEnd   ActivityType = "provider_end"
	ActivityToolStart     ActivityType = "tool_start"
	ActivityToolEnd       ActivityType = "tool_end"
	ActivityStream        ActivityType = "stream"
)
