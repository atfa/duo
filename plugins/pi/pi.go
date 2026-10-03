// Package pi implements the Pi driver for Duo Core.
//
// Pi is driven two ways at once: this package is the Host Adapter, answering the
// Duo Driver Plugin Protocol, and pi-extension/ is the Agent Adapter, running
// inside the Pi process. The split is what lets Core know nothing about Pi: the
// command line below and the socket bridge in the extension are two halves of one
// plugin, and Core sees only this half.
package pi

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/atfa/duo/internal/driver"
)

// Name is the driver name an operator selects with `--agent pi`.
const Name = "pi"

// sessionFlags are the flags through which an operator may already have chosen a
// Pi session. Detection is token-exact: a substring test for "-c" also matches
// "--config", which once cost an agent its conversation.
var sessionFlags = []string{"--session-id", "--session", "--continue", "--resume", "--fork", "--no-session", "-c", "-r"}

// state is the plugin's private resume blob. Core stores it verbatim and never
// reads it.
type state struct {
	SessionID string `json:"sessionId"`
}

// Plugin is the Pi Host Adapter.
type Plugin struct{}

// New returns the Pi driver.
func New() driver.Handler { return Plugin{} }

// Register makes Pi available to Duo in-process. M4a converts this Handler into a
// standalone `duo-plugin-pi` executable without changing it.
func Register() { driver.Register(Name, func() driver.Handler { return New() }) }

func (Plugin) Describe() (*driver.Manifest, error) {
	return &driver.Manifest{
		Protocol:    driver.ProtocolVersion,
		Name:        Name,
		Version:     DriverVersion,
		Description: "Pi CLI coding agent (socket bridge)",
		Agent:       driver.AgentInfo{CLI: Name, DefaultCommand: Name},
		// Pi takes a provider-qualified model, and Core joins a reported provider
		// and id back into exactly this form for the picker.
		DefaultModel:   DefaultModel(),
		ModelReference: driver.ModelQualified,
		Capabilities: driver.Capabilities{
			// Pi adopts a session id it has never seen, so Core never has to ask
			// whether one was generated: this plugin owns it and reuses it.
			Resume: driver.ResumeClient,
			Models: true,
			// Pi's thinking levels live in the session, not in a launch flag.
			Thinking:           true,
			LiveModelSwitch:    true,
			LiveThinkingSwitch: true,
			Activity:           true,
			ContextUsage:       true,
			TokenRate:          true,
			// pi-extension connects from inside the Pi process, so its endpoint can
			// receive an injected prompt.
			Bridge:       driver.BridgeAgent,
			MCP:          true,
			PTYFallback:  true,
			LiveSteering: true,
			// It connects on session_start, which is as early as it can.
			SelfReports: true,
		},
	}, nil
}

func (Plugin) Probe() (*driver.ProbeResult, error) {
	path, err := lookPath(Name)
	if err != nil {
		return &driver.ProbeResult{Available: false, Reason: "the pi CLI is not on PATH"}, nil
	}
	return &driver.ProbeResult{Available: true, AgentPath: path}, nil
}

// Prepare builds the command line, the identity to replay and the environment the
// Pi process needs. Everything Pi-specific is decided here; Core only runs what
// comes back.
func (Plugin) Prepare(req driver.LaunchRequest) (*driver.LaunchPlan, error) {
	var prior state
	if len(req.State) > 0 {
		if err := json.Unmarshal(req.State, &prior); err != nil {
			return nil, &driver.RPCError{
				Code:    driver.CodeInvalid,
				Message: "pi resume state is not readable: " + err.Error(),
			}
		}
	}

	base := strings.TrimSpace(req.BaseCommand)
	if base == "" {
		base = Name
	}

	// Pi adopts a session id it has never seen, which is exactly why it is
	// ResumeClient: this plugin mints one on the first run and replays it after,
	// and Pi reattaches to the same conversation every time. A driver whose agent
	// rejects an unknown id would instead have to send nothing here and declare
	// ResumeServer.
	id := prior.SessionID
	if id == "" {
		id = NewSessionID()
	}

	// Pi takes model and thinking level over its bridge, not as launch flags, so
	// these are only the starting values for a process that has not connected
	// yet. An operator who put the flag on the command line or in config keeps it:
	// Duo must not override a choice a human made.
	command := driver.AppendFlag(base, "--model", strings.TrimSpace(req.Model))
	command = driver.AppendFlag(command, "--thinking", strings.TrimSpace(req.Thinking))
	if id != "" && !driver.HasFlag(command, sessionFlags...) {
		// Passed through the environment rather than interpolated, so no quoting
		// mistake can corrupt the id and no shell can expand it.
		command = driver.AppendLiteral(command, "--session-id", `"$DUO_PI_SESSION_ID"`)
	}

	raw, err := json.Marshal(state{SessionID: id})
	if err != nil {
		return nil, err
	}
	return &driver.LaunchPlan{
		Command: command,
		Env:     map[string]string{"DUO_PI_SESSION_ID": id},
		State:   raw,
		// Shown in the session log so an operator can see which conversation an
		// agent is on.
		SessionIdentity: id,
	}, nil
}

func (Plugin) Models(ctx context.Context) (*driver.ModelList, error) {
	command := ListCommand()
	return driver.RunModelList(ctx, command, ParseListModels)
}

func (Plugin) Thinking(ctx context.Context) (*driver.ThinkingOptions, error) {
	return &driver.ThinkingOptions{Levels: ThinkingLevels, Default: ThinkingLevels[2]}, nil
}

// ThinkingLevels are the levels Pi understands, in escalation order. Pi clamps a
// level to what the current model supports, so this is the order Core cycles and
// the plugin reports back whatever Pi settled on.
var ThinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}
