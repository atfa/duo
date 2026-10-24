package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// ProtocolVersion is the wire version both sides speak.
	ProtocolVersion = 1

	// MaxFrame bounds one JSON frame. The relay enforces the same number, so a
	// client that respects it never has a frame refused.
	MaxFrame = 4096

	// MaxPlaintext bounds one payload before encryption. It leaves room for the
	// base64url expansion and the envelope inside MaxFrame.
	MaxPlaintext = 3000

	// Frame kinds.
	frameHello    = "hello"
	frameReady    = "ready"
	frameMsg      = "msg"
	frameCmd      = "cmd"
	frameAck      = "ack"
	framePing     = "ping"
	framePong     = "pong"
	framePresence = "presence"
	frameBye      = "bye"
	frameError    = "error"

	// Roles.
	RoleHost  = "host"
	RoleGuest = "guest"

	// Message kinds: what a `msg` frame carries.
	KindState = "state"
	KindEvent = "event"

	// Command kinds: what the guest may ask for.
	CmdTask           = "task"
	CmdNewSession     = "new_session"
	CmdObserverReport = "observer_report"
	// CmdSwitchMode carries the mode name in Text. The host validates it and
	// switches quietly, so changing mode from a phone never wakes an agent.
	CmdSwitchMode = "switch_mode"
	// CmdSetDriver carries "<agent> <driver>" in Text: the phone replaces the
	// executable behind one agent.
	CmdSetDriver = "set_driver"
	// CmdCatalog carries an agent name in Text and asks for its model list. It is a
	// read: loading a catalog starts the driver's own CLI, so the answer arrives
	// as a later state rather than as the command's result.
	CmdCatalog = "catalog"
	// CmdSetModel carries "<agent> <reference>" in Text, where the reference is one
	// the host published in a catalog.
	CmdSetModel = "set_model"
)

// Close and error codes. They are stable identifiers: a frontend localises them
// and never parses prose from the relay.
const (
	CodeAuth        = "auth"
	CodeBadFrame    = "bad_frame"
	CodeRateLimited = "rate_limited"
	CodeExpired     = "expired"
	CodeRoomFull    = "room_full"
	CodeUnsupported = "unsupported"

	CodeRevoked    = "revoked"
	CodeOverBudget = "over_budget"
	CodeReplaced   = "replaced"
)

// ErrStopped reports that the client stopped on purpose.
var ErrStopped = errors.New("remote: stopped")

// ErrTerminal reports that the relay ended the room for good; reconnecting would
// only repeat the refusal.
type ErrTerminal struct {
	Code string
}

func (e *ErrTerminal) Error() string { return "remote: relay closed the room: " + e.Code }

// frame is one wire frame. One struct covers every type; encoding/json omits the
// fields a type does not use, and the relay validates what it receives.
type frame struct {
	T         string `json:"t"`
	V         int    `json:"v,omitempty"`
	Role      string `json:"role,omitempty"`
	Room      string `json:"room,omitempty"`
	Proof     string `json:"proof,omitempty"`
	Since     int64  `json:"since,omitempty"`
	Seq       int64  `json:"seq,omitempty"`
	Kind      string `json:"kind,omitempty"`
	IV        string `json:"iv,omitempty"`
	CT        string `json:"ct,omitempty"`
	ID        string `json:"id,omitempty"`
	Code      string `json:"code,omitempty"`
	ExpiresAt int64  `json:"expiresAt,omitempty"`
	Host      bool   `json:"host,omitempty"`
	Guests    int    `json:"guests,omitempty"`
}

func decodeFrame(data []byte) (frame, error) {
	if len(data) > MaxFrame {
		return frame{}, fmt.Errorf("remote: frame is %d bytes, limit %d", len(data), MaxFrame)
	}
	var f frame
	if err := json.Unmarshal(data, &f); err != nil {
		return frame{}, fmt.Errorf("remote: malformed frame: %w", err)
	}
	if f.T == "" {
		return frame{}, errors.New("remote: frame has no type")
	}
	return f, nil
}

func (f frame) encode() ([]byte, error) {
	data, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFrame {
		return nil, fmt.Errorf("remote: frame is %d bytes, limit %d", len(data), MaxFrame)
	}
	return data, nil
}

// State is the snapshot the phone renders. It is a plain projection: the caller
// maps a session view into it, so this package never imports session state and
// the page never sees a handle it could act through.
type State struct {
	V       int              `json:"v"`
	N       int64            `json:"n"`
	TS      int64            `json:"ts"`
	Project string           `json:"project"`
	Repo    string           `json:"repo"`
	Session string           `json:"session"`
	Mode    string           `json:"mode"`
	Phase   string           `json:"phase"`
	Started bool             `json:"started"`
	Drivers []string         `json:"drivers"`
	Step    *StepState       `json:"step"`
	Plan    string           `json:"plan"`
	Agents  map[string]Agent `json:"agents"`
	// Catalog is the model list the phone last asked for. It is absent until it
	// asks, because loading one starts another CLI and most states do not need it.
	Catalog  *CatalogState  `json:"catalog"`
	Pause    PauseState     `json:"pause"`
	Activity []Activity     `json:"activity"`
	Observer ObserverState  `json:"observer"`
	Delivery *DeliveryState `json:"delivery"`

	// Command is the outcome of the last command the phone sent, so a rejected
	// instruction is visible on the phone instead of only in a local log.
	Command *CommandResult `json:"command"`
}

// CommandResult is the host's answer to one command, addressed by its id.
type CommandResult struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Accepted bool   `json:"accepted"`
	Note     string `json:"note,omitempty"`
}

// CatalogState is one agent's model list as the phone shows it.
//
// Total is the whole catalog, so a page whose list was cut can say so; Loading
// is set while the driver's own CLI is being asked, and Error carries the reason
// a driver published no list at all (a driver without a catalog is an answer,
// not a failure).
type CatalogState struct {
	Agent   string   `json:"agent"`
	Loading bool     `json:"loading"`
	Models  []string `json:"models"`
	Total   int      `json:"total,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// StepState is the current Step-mode entry, or nil when the mode has no queue.
type StepState struct {
	Index  int    `json:"index"`
	Total  int    `json:"total"`
	Policy string `json:"policy"`
	Title  string `json:"title"`
}

// Agent is one agent's runtime state as the phone shows it.
type Agent struct {
	Connected bool   `json:"connected"`
	Running   bool   `json:"running"`
	Driver    string `json:"driver"`
	Model     string `json:"model"`
	Thinking  string `json:"thinking"`
	Activity  string `json:"activity"`
	// State is the driver process state: starting, running, stopping, exited or
	// failed, and empty when the session has no driver for this agent at all.
	// The phone draws its indicator from it, so a failure is the only red one and
	// "not started yet" never reads as an error.
	State string `json:"state"`
}

// PauseState is the workflow pause projection.
type PauseState struct {
	State string `json:"state"`
}

// Activity is one recent event the phone lists.
type Activity struct {
	At    int64  `json:"at"`
	Kind  string `json:"kind"`
	Agent string `json:"agent,omitempty"`
	Text  string `json:"text"`
}

// ObserverState carries the observer's on-demand reports.
type ObserverState struct {
	Analyzing bool             `json:"analyzing"`
	Reports   []ObserverReport `json:"reports"`
}

// ObserverReport is one status report and when it was produced.
type ObserverReport struct {
	At   int64  `json:"at"`
	Text string `json:"text"`
}

// DeliveryState reports where the finished artifact landed.
type DeliveryState struct {
	Applied bool   `json:"applied"`
	Head    string `json:"head"`
	Note    string `json:"note"`
}

// Event kinds, the values of Event.Kind.
const (
	EventActivity = "activity"
	EventPhase    = "phase"
	EventAgent    = "agent"
	EventObserver = "observer"
	EventDelivery = "delivery"
)

// Event is an incremental update between snapshots.
type Event struct {
	V     int    `json:"v"`
	N     int64  `json:"n"`
	At    int64  `json:"at"`
	Kind  string `json:"kind"`
	Agent string `json:"agent,omitempty"`
	Text  string `json:"text"`
}

// Command is one instruction from the phone, after decryption.
type Command struct {
	V    int    `json:"v"`
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Text string `json:"text,omitempty"`
	N    int64  `json:"n"`
	MAC  string `json:"mac"`
}

// ParseAgentCommand splits a command's text into the agent it addresses and the
// value that follows it.
//
// The split is at the first space only, and the rest is kept verbatim: a driver
// name or a model reference may itself contain spaces, and the host has to
// receive exactly what the phone meant. Nothing here knows which agents exist;
// the host maps the name and refuses one it does not run.
func ParseAgentCommand(text string) (agent, value string, err error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", "", errors.New("the command named no agent")
	}
	agent, value, found := strings.Cut(text, " ")
	value = strings.TrimSpace(value)
	if !found || value == "" {
		return "", "", fmt.Errorf("the command named no value for %s", agent)
	}
	return agent, value, nil
}

// truncate shortens a text field so a payload stays under MaxPlaintext. It
// counts runes, not bytes, so a multi-byte character is never cut in half.
func truncate(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}

// unixMilli is the one timestamp unit the wire uses.
func unixMilli(t time.Time) int64 { return t.UnixMilli() }
