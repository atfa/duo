// Package driver defines Duo Driver Plugin Protocol v1: the contract between
// Duo Core and an Agent Driver Plugin.
//
// Core owns collaboration; a plugin owns one coding agent. Core never learns a
// driver's name in order to decide behaviour: it asks the plugin to describe
// itself and then works from the returned capabilities.
//
// A plugin is an executable that speaks newline-delimited JSON requests and
// responses on its stdin and stdout. Core keeps one plugin process per session
// alive, so the plugin may hold state and may attach an Agent Adapter to Duo's
// bridge on its own schedule.
package driver

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ProtocolVersion is the Duo Driver Plugin Protocol version Core speaks. It is
// independent of the Duo binary version. Core requires an exact match: a plugin
// built for another protocol version is refused loudly rather than driven with
// fields it may not understand.
const ProtocolVersion = 1

// Resume modes describe who owns an agent session's identity.
const (
	// ResumeClient means the plugin (or its agent) chooses the identity up
	// front and reattaches to it, so Core hands a stored identity back on the
	// next launch.
	ResumeClient = "client"
	// ResumeServer means the agent assigns the identity itself and reports it
	// later. Core must not invent one: the agent rejects an id it never issued.
	ResumeServer = "server"
	// ResumeNone means the agent cannot reattach to a previous session, so a
	// restart or resume starts a fresh conversation.
	ResumeNone = "none"
)

// Bridge values declare who owns the one bridge endpoint Duo Core keeps per
// agent. This is ownership, not transport flavour: Core never branches on
// socket vs MCP vs ACP vs native, because it cannot tell them apart and must not
// try. The transport a plugin happens to use is its own business.
const (
	// BridgeNone means the driver has no bridge endpoint at all.
	BridgeNone = "none"
	// BridgeAgent means an adapter running inside the agent process connects:
	// it can receive injected prompts, so Core delivers downstream messages
	// over the bridge.
	BridgeAgent = "agent"
	// BridgePlugin means the plugin process itself connects. It observes the
	// agent from outside and reports upstream, but it cannot write to the PTY
	// Duo Core owns, so downstream messages still go to the terminal.
	BridgePlugin = "plugin"
)

// Model reference formats describe how a driver spells a model.
const (
	// ModelQualified means a model is "provider/id" and the provider prefix is
	// part of its identity.
	ModelQualified = "qualified"
	// ModelBare means the driver accepts only the bare model id and rejects a
	// provider prefix.
	ModelBare = "bare"
)

// Error codes a plugin may return. Core distinguishes "this capability is not
// implemented" from "this plugin is broken", because the first is a normal
// outcome that a capability check should have prevented and the second must be
// reported.
const (
	CodeUnsupported   = "unsupported"
	CodeInvalid       = "invalid_request"
	CodeUnavailable   = "unavailable"
	CodeInternal      = "internal"
	CodeProtocol      = "protocol_mismatch"
	CodeUnknownMethod = "unknown_method"
)

// ErrUnsupported reports that a plugin does not implement a method. It is a
// normal answer to a method a capability said is absent, never a crash.
var ErrUnsupported = errors.New("driver plugin: method not supported")

// ErrCrashed reports that the plugin process died or stopped answering. It is
// distinct from ErrUnsupported because a crash is a plugin fault the operator
// must see, while an unsupported method is a capability the plugin declared it
// does not have.
var ErrCrashed = errors.New("driver plugin: process exited")

// AgentID is the Duo agent a launch request is about. It is a plain string so a
// plugin written in another language never has to import Duo's Go packages.
type AgentID string

// Request is one call from Core to a plugin.
type Request struct {
	Protocol int             `json:"protocol"`
	ID       int64           `json:"id"`
	Method   string          `json:"method"`
	Params   json.RawMessage `json:"params,omitempty"`
}

// Response is one reply from a plugin to a Request.
type Response struct {
	Protocol int             `json:"protocol"`
	ID       int64           `json:"id"`
	OK       bool            `json:"ok"`
	Result   json.RawMessage `json:"result,omitempty"`
	Error    *RPCError       `json:"error,omitempty"`
}

// RPCError is the failure body of a Response.
type RPCError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return "driver plugin: unknown error"
	}
	return fmt.Sprintf("driver plugin: %s: %s", e.Code, e.Message)
}

// Capabilities is everything Core is allowed to branch on. Every field is
// declared by the plugin: Core has no other source for driver behaviour, so a
// new agent that fills this in correctly needs no change to Core.
type Capabilities struct {
	// Resume is ResumeClient, ResumeServer or ResumeNone.
	Resume string `json:"resume"`
	// Models reports that the plugin can enumerate a model catalog.
	Models bool `json:"models"`
	// Thinking reports that the plugin exposes reasoning-effort levels.
	Thinking bool `json:"thinking"`
	// LiveModelSwitch reports that a new model reaches a running agent without
	// a restart. When false, Core restarts the process to apply it.
	LiveModelSwitch bool `json:"liveModelSwitch"`
	// LiveThinkingSwitch reports the same for reasoning effort.
	LiveThinkingSwitch bool `json:"liveThinkingSwitch"`
	// Activity reports that the plugin's Agent Adapter emits activity over
	// Duo's bridge, so Core never has to observe an external file.
	Activity bool `json:"activity"`
	// ContextUsage reports that the agent reports context-window usage.
	ContextUsage bool `json:"contextUsage"`
	// TokenRate reports that the plugin estimates streamed output rate.
	TokenRate bool `json:"tokenRate"`
	// Bridge declares who owns this driver's single bridge endpoint:
	// BridgeNone, BridgeAgent or BridgePlugin. It decides downstream delivery:
	// only BridgeAgent's endpoint can receive an injected prompt.
	Bridge string `json:"bridge"`
	// MCP reports that the plugin can serve Duo's coordination tools over MCP.
	MCP bool `json:"mcp"`
	// PTYFallback reports that Core may deliver prompts by writing to the
	// agent's terminal when no bridge is attached.
	PTYFallback bool `json:"ptyFallback"`
	// LiveSteering reports that a peer message reaches the agent mid-turn rather
	// than being queued until it finishes.
	LiveSteering bool `json:"liveSteering"`
	// SelfReports reports that the plugin's bridge attaches as soon as the agent
	// process starts. When false Core announces the connection itself once the
	// process is up, so every agent produces the same signal at the same moment.
	SelfReports bool `json:"selfReports"`
}

// AgentInfo names the coding agent a plugin drives.
type AgentInfo struct {
	// CLI is the executable the plugin launches, used for diagnostics and as
	// the default when the operator configured no command.
	CLI string `json:"cli"`
	// DefaultCommand is the launch command when the operator supplied none.
	DefaultCommand string `json:"defaultCommand,omitempty"`
	// Version is the agent CLI's version, filled in by probe when known.
	Version string `json:"version,omitempty"`
}

// Manifest is a plugin's self-description, returned by describe. It is the only
// thing Core reads to decide how to treat a driver.
type Manifest struct {
	Protocol     int       `json:"protocol"`
	Name         string    `json:"name"`
	Version      string    `json:"version"`
	Description  string    `json:"description,omitempty"`
	Agent        AgentInfo `json:"agent"`
	DefaultModel string    `json:"defaultModel,omitempty"`
	// ModelReference is ModelQualified or ModelBare. Core uses it to join a
	// reported provider and id back into the single reference the picker shows.
	ModelReference string       `json:"modelReference"`
	Capabilities   Capabilities `json:"capabilities"`
	// Notices are operator-facing diagnostics this driver needs, such as a
	// missing helper tool. Core shows them verbatim instead of hard-coding
	// per-driver warnings.
	Notices []string `json:"notices,omitempty"`
}

// Bare reports whether this driver's model references carry no provider prefix.
// A nil manifest is not bare: a driver Duo knows nothing about is joined and
// displayed the way every reference was treated before manifests existed.
func (m *Manifest) Bare() bool {
	return m != nil && m.ModelReference == ModelBare
}

// ProbeResult reports whether the underlying agent CLI can be used right now.
type ProbeResult struct {
	Available    bool   `json:"available"`
	Reason       string `json:"reason,omitempty"`
	AgentPath    string `json:"agentPath,omitempty"`
	AgentVersion string `json:"agentVersion,omitempty"`
}

// LaunchRequest is what Core asks a plugin to turn into a runnable agent.
// Every field is Core state the plugin may need; none of them is a driver
// concept.
type LaunchRequest struct {
	Agent          AgentID `json:"agent"`
	Mode           string  `json:"mode"`
	CWD            string  `json:"cwd"`
	RepositoryRoot string  `json:"repositoryRoot,omitempty"`
	ScopePath      string  `json:"scopePath,omitempty"`
	Session        string  `json:"session"`
	Token          string  `json:"token"`
	Host           string  `json:"host"`
	Port           string  `json:"port"`
	// BaseCommand is the operator's own command for this agent, possibly empty
	// and possibly carrying arguments. A plugin must preserve it and add to it.
	BaseCommand string `json:"baseCommand,omitempty"`
	// Model and Thinking are the resolved selections, empty when unset.
	Model    string `json:"model,omitempty"`
	Thinking string `json:"thinking,omitempty"`
	// State is the opaque blob this plugin stored on the previous run. Core
	// never inspects it.
	State json.RawMessage `json:"state,omitempty"`
}

// LaunchPlan is a plugin's finished launch specification. Core runs Command in a
// pseudo-terminal and nothing else, which is why a driver needs no support in
// Core beyond this.
type LaunchPlan struct {
	// Command is a complete shell command line, executed as
	// `sh -lc "exec <command>"`. It may reference environment variables the
	// plan sets, which is how a value is passed without re-quoting it.
	Command string `json:"command"`
	// Env is added to the agent process environment on top of Core's own.
	Env map[string]string `json:"env,omitempty"`
	// State is the opaque blob Core must persist for this agent.
	State json.RawMessage `json:"state,omitempty"`
	// SessionIdentity is a short label for logs and the session log. Core never
	// branches on it.
	SessionIdentity string `json:"sessionIdentity,omitempty"`
	// Cleanup lists paths Core must clear before the next launch, such as a
	// log file a stale value would otherwise be read from.
	Cleanup []string `json:"cleanup,omitempty"`
	// Notices are operator-facing diagnostics the plugin found while preparing
	// this launch, such as a helper tool missing from PATH. They belong here
	// rather than in the manifest because they are a property of the machine at
	// this moment: a static string in describe would be a claim Core cannot
	// keep true.
	Notices []string `json:"notices,omitempty"`
}

// Model is one selectable model in a plugin's catalog.
type Model struct {
	Provider      string `json:"provider,omitempty"`
	ID            string `json:"id"`
	DisplayName   string `json:"displayName,omitempty"`
	ContextWindow int    `json:"contextWindow,omitempty"`
	Thinking      bool   `json:"thinking"`
	Images        bool   `json:"images"`
}

// ModelList is the result of the models method.
type ModelList struct {
	Models []Model `json:"models"`
}

// ThinkingOptions is the result of the thinking method.
type ThinkingOptions struct {
	Levels  []string `json:"levels"`
	Default string   `json:"default,omitempty"`
}

// Method names. describe, probe and prepare are required; the rest exist only
// when the matching capability is true. state is additionally required for
// every resume: server driver, and optional for everyone else.
const (
	MethodDescribe = "describe"
	MethodProbe    = "probe"
	MethodPrepare  = "prepare"
	MethodState    = "state"
	MethodModels   = "models"
	MethodThinking = "thinking"
	MethodClose    = "close"
)

// StateResult is the answer to MethodState: the plugin's current resume blob,
// which may differ from the one prepare returned because the agent learned its
// own identity after launch.
type StateResult struct {
	State json.RawMessage `json:"state"`
}
