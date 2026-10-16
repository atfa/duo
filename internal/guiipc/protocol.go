// Package guiipc carries the frozen NDJSON protocol between Duo and the gpui
// graphical frontend, and hosts a session behind it.
//
// It is a frontend-neutral adapter, not a second frontend: it reads the same
// session.View projection the terminal interface reads and translates the same
// session.Command values the terminal interface sends. It owns no workflow
// state, opens no socket and names no live core object, so the GUI cannot reach
// the coordinator through it.
//
// The wire contract is written down in docs/gui-protocol.md; this file is its Go
// half, and the field names here are the contract, not an implementation detail.
package guiipc

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/models"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
)

// Frame types on the wire. Go sends view/event/error/closed; the GUI sends
// command.
const (
	typeView    = "view"
	typeEvent   = "event"
	typeError   = "error"
	typeClosed  = "closed"
	typeCommand = "command"
)

// Command kinds the GUI may send. This is deliberately the set of human verbs,
// not the set of session.Command values: the model and thinking pickers need the
// driver's model catalogue, which is outside the MVP, so set_model and
// cycle_thinking are not on the wire yet.
const (
	KindSubmitTask     = "submit_task"
	KindEscalateToGoal = "escalate_to_goal"
	KindRestartAgent   = "restart_agent"
	KindSetLanguage    = "set_language"
	KindSetModel       = "set_model"
	KindCycleThinking  = "cycle_thinking"
	KindQuit           = "quit"
)

// Ready is the per-agent sign-off pair, keyed by the wire agent spelling.
type Ready struct {
	Austin bool `json:"austin"`
	Tony   bool `json:"tony"`
}

// Agent is the wire projection of one agent's session.AgentView. Activity is a
// short summary; Work carries bounded structured runtime data for the preview.
type Agent struct {
	Present        bool        `json:"present"`
	WorktreePath   string      `json:"worktree_path"`
	WorktreeBranch string      `json:"worktree_branch"`
	WorktreeHead   string      `json:"worktree_head"`
	Connected      bool        `json:"connected"`
	Process        string      `json:"process"`
	Driver         string      `json:"driver"`
	Model          string      `json:"model"`
	Thinking       string      `json:"thinking"`
	Busy           bool        `json:"busy"`
	Activity       string      `json:"activity"`
	Work           WorkPreview `json:"work"`
	Models         []Model     `json:"models"`
	ModelsLoaded   bool        `json:"models_loaded"`
	ModelsError    string      `json:"models_error,omitempty"`
}

type WorkPreview struct {
	ProviderActive  bool       `json:"provider_active"`
	ToolDepth       int        `json:"tool_depth"`
	HumanAttached   bool       `json:"human_attached"`
	LastActivity    string     `json:"last_activity,omitempty"`
	Tool            string     `json:"tool"`
	ToolDetail      string     `json:"tool_detail"`
	ToolFailed      bool       `json:"tool_failed"`
	StreamTail      string     `json:"stream_tail"`
	LastError       string     `json:"last_error"`
	ContextTokens   int        `json:"context_tokens"`
	ContextWindow   int        `json:"context_window"`
	TokensPerSecond float64    `json:"tokens_per_second"`
	Tools           int        `json:"tools"`
	TurnStarted     string     `json:"turn_started,omitempty"`
	ToolStarted     string     `json:"tool_started,omitempty"`
	TokenSpeedAt    string     `json:"token_speed_at,omitempty"`
	Failures        int        `json:"failures"`
	Recent          []ToolNote `json:"recent"`
}

type ToolNote struct {
	Name       string `json:"name"`
	Detail     string `json:"detail"`
	OK         bool   `json:"ok"`
	DurationMS int64  `json:"duration_ms"`
}

// Model is one option reported by the agent's driver catalog.
type Model struct {
	Provider  string `json:"provider"`
	ID        string `json:"id"`
	Reference string `json:"reference"`
	Thinking  bool   `json:"thinking"`
	Images    bool   `json:"images"`
}

// View is the wire projection of session.View. Notices carries the driver
// diagnostics a session buffered before any frontend existed, so the GUI shows
// them the way the terminal interface does rather than losing them.
type View struct {
	Version     string           `json:"version"`
	Session     string           `json:"session"`
	Mode        string           `json:"mode"`
	Phase       string           `json:"phase"`
	Started     bool             `json:"started"`
	Language    string           `json:"language"`
	Repository  string           `json:"repository"`
	BaseBranch  string           `json:"base_branch"`
	BaseCommit  string           `json:"base_commit"`
	Status      string           `json:"status"`
	Plan        string           `json:"plan"`
	PlanVersion int              `json:"plan_version"`
	Ready       Ready            `json:"ready"`
	StepsTotal  int              `json:"steps_total"`
	StepCursor  int              `json:"step_cursor"`
	Delivery    string           `json:"delivery"`
	Notices     []string         `json:"notices"`
	Agents      map[string]Agent `json:"agents"`
}

// Event is one timeline line. Live session events and replayed history records
// land in the same shape: the store records error/warning flags rather than
// kinds, so fromHistory derives the kind and both sources render one way.
type Event struct {
	Time  string `json:"time"`
	Kind  string `json:"kind"`
	Agent string `json:"agent"`
	Peer  string `json:"peer"`
	Text  string `json:"text"`
	Label string `json:"label,omitempty"`
}

// Command is the envelope the GUI sends. Type is always "command"; Kind selects
// which human verb it carries.
type Command struct {
	Type     string `json:"type"`
	Kind     string `json:"kind"`
	Text     string `json:"text,omitempty"`
	Agent    string `json:"agent,omitempty"`
	Language string `json:"language,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// writeFrame is the single envelope for everything Go sends, so the GUI decodes
// one struct and switches on Type.
type writeFrame struct {
	Type  string `json:"type"`
	View  *View  `json:"view,omitempty"`
	Event *Event `json:"event,omitempty"`
	Text  string `json:"text,omitempty"`
}

// wireAgent is the canonical wire spelling of an agent identity.
//
// The protocol spells agents in lower case ("austin"/"tony"/"duo") while the
// session spells them in title case (protocol.Austin and so on), and history
// panes are title case too. The translation lives only here, so no other
// translation can disagree with it.
func wireAgent(id protocol.AgentID) string {
	if id == "" {
		return ""
	}
	return strings.ToLower(string(id))
}

// parseAgent turns a wire spelling back into a session identity. It accepts
// either case so a GUI bug in capitalization degrades to a working command
// instead of a silent no-op, but only known agents parse.
func parseAgent(s string) (protocol.AgentID, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(wireAgent(protocol.Austin)):
		return protocol.Austin, nil
	case string(wireAgent(protocol.Tony)):
		return protocol.Tony, nil
	default:
		return "", fmt.Errorf("unknown agent %q", s)
	}
}

// fromView projects a session view onto the wire view. A frontend consuming the
// result cannot reach back into the session: every field is a value copy.
func fromView(v session.View, status string) View {
	out := View{
		Version:     v.Version,
		Session:     v.SessionID,
		Mode:        string(v.EffectiveMode()),
		Phase:       string(v.Phase()),
		Started:     v.Project.Started,
		Language:    v.Language,
		Repository:  v.Worktrees.Repository,
		BaseBranch:  v.Worktrees.BaseBranch,
		BaseCommit:  v.Worktrees.BaseCommit,
		Status:      status,
		Plan:        v.Project.Plan,
		PlanVersion: v.Project.PlanVersion,
		Ready: Ready{
			Austin: v.Project.Ready[protocol.Austin],
			Tony:   v.Project.Ready[protocol.Tony],
		},
		StepsTotal: len(v.Project.Steps),
		StepCursor: v.Project.Step,
		Delivery:   v.Delivery.Status,
		Notices:    append([]string{}, v.Notices...),
		Agents:     map[string]Agent{},
	}
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		av := v.Agents[id]
		worktree, _ := v.Worktrees.For(id)
		out.Agents[wireAgent(id)] = Agent{
			Present:        av.Present,
			WorktreePath:   worktree.Path,
			WorktreeBranch: worktree.Branch,
			WorktreeHead:   worktree.Head,
			Connected:      av.Connected,
			Process:        av.Process.String(),
			Driver:         av.Driver,
			Model:          av.Model,
			Thinking:       av.Thinking,
			Busy:           av.Runtime.Busy,
			Activity:       activityLine(av.Runtime),
			Work:           workPreview(av.Runtime),
			Models:         []Model{},
		}
	}
	return out
}

func workPreview(rt harness.AgentRuntime) WorkPreview {
	out := WorkPreview{ProviderActive: rt.ProviderActive, ToolDepth: rt.ToolDepth, HumanAttached: rt.HumanAttached,
		LastActivity: wireTime(rt.LastActivity), Tool: truncateRunes(rt.Tool, 100), ToolDetail: truncateRunes(rt.ToolDetail, 500), ToolFailed: rt.ToolFailed, StreamTail: truncateRunes(rt.StreamTail, 1200),
		LastError: truncateRunes(rt.LastError, 500), ContextTokens: rt.ContextTokens, ContextWindow: rt.ContextWindow,
		TokensPerSecond: rt.TokensPerSecond, Tools: rt.Tools, TurnStarted: wireTime(rt.TurnStarted), ToolStarted: wireTime(rt.ToolStarted),
		TokenSpeedAt: wireTime(rt.TokenSpeedAt), Failures: rt.Failures, Recent: make([]ToolNote, 0, len(rt.Recent))}
	for _, note := range rt.Recent {
		out.Recent = append(out.Recent, ToolNote{Name: truncateRunes(note.Name, 100), Detail: truncateRunes(note.Detail, 300), OK: note.OK, DurationMS: note.Duration.Milliseconds()})
	}
	return out
}

func wireTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func fromModels(ms []models.Model) []Model {
	out := make([]Model, 0, len(ms))
	for _, m := range ms {
		out = append(out, Model{Provider: m.Provider, ID: m.ID, Reference: m.Reference(), Thinking: m.Thinking, Images: m.Images})
	}
	return out
}

// activityMaxRunes bounds the activity line so a long stream tail cannot make
// every heartbeat frame arbitrarily large.
const activityMaxRunes = 200

// activityLine reduces the runtime's work detail to one line, in the order the
// native preview reads it: the running tool, its detail, the latest streamed
// text, then the last error.
func activityLine(rt harness.AgentRuntime) string {
	for _, candidate := range []string{rt.ToolDetail, rt.Tool, rt.StreamTail, rt.LastError} {
		if line := truncateRunes(strings.Join(strings.Fields(candidate), " "), activityMaxRunes); line != "" {
			return line
		}
	}
	return ""
}

// truncateRunes clips s to at most n runes without splitting a multi-byte
// character.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// fromEvent projects a live session event onto the wire.
func fromEvent(e events.Event) Event {
	return Event{
		Time:  e.Time.UTC().Format(time.RFC3339Nano),
		Kind:  string(e.Kind),
		Agent: wireAgent(e.Agent),
		Peer:  wireAgent(e.Peer),
		Text:  e.Text,
	}
}

// fromHistory projects one persisted pane entry onto the same wire shape. The
// store keeps flags, not kinds, so the kind is derived the way the terminal
// timeline derives its colour: error first, then warning as a verdict, then an
// ordinary agent line. Pane is the speaker, and Label is the "Austin → Tony"
// header the store already rendered.
func fromHistory(e sessionstore.TUIEntry) Event {
	kind := events.KindAssistant
	switch {
	case e.Error:
		kind = events.KindError
	case e.Warning:
		kind = events.KindVerdict
	}
	return Event{
		Time:  e.Time.UTC().Format(time.RFC3339Nano),
		Kind:  string(kind),
		Agent: strings.ToLower(strings.TrimSpace(e.Pane)),
		Text:  e.Text,
		Label: e.Label,
	}
}

// decodeCommand parses one command frame. The bool reports a quit, which is not
// a session command: quitting belongs to the transport, not to the workflow.
func decodeCommand(line []byte) (session.Command, bool, error) {
	var cmd Command
	if err := json.Unmarshal(line, &cmd); err != nil {
		return nil, false, fmt.Errorf("decode command: %w", err)
	}
	if cmd.Type != typeCommand {
		return nil, false, fmt.Errorf("unexpected frame type %q; the GUI may only send %q", cmd.Type, typeCommand)
	}
	switch cmd.Kind {
	case KindSubmitTask:
		if strings.TrimSpace(cmd.Text) == "" {
			return nil, false, errors.New("submit_task carries no text")
		}
		return session.SubmitTask{Text: cmd.Text}, false, nil
	case KindEscalateToGoal:
		return session.EscalateToGoal{}, false, nil
	case KindRestartAgent:
		agent, err := parseAgent(cmd.Agent)
		if err != nil {
			return nil, false, err
		}
		return session.RestartAgent{Agent: agent}, false, nil
	case KindSetLanguage:
		if strings.TrimSpace(cmd.Language) == "" {
			return nil, false, errors.New("set_language carries no language")
		}
		return session.SetLanguage{Language: cmd.Language}, false, nil
	case KindSetModel:
		agent, err := parseAgent(cmd.Agent)
		if err != nil {
			return nil, false, err
		}
		if strings.TrimSpace(cmd.Model) == "" {
			return nil, false, errors.New("set_model carries no model")
		}
		return session.SetModel{Agent: agent, Provider: cmd.Provider, Model: cmd.Model}, false, nil
	case KindCycleThinking:
		agent, err := parseAgent(cmd.Agent)
		if err != nil {
			return nil, false, err
		}
		return session.CycleThinking{Agent: agent}, false, nil
	case KindQuit:
		return nil, true, nil
	default:
		return nil, false, fmt.Errorf("unknown command kind %q", cmd.Kind)
	}
}
