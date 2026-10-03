// Package opencode implements the opencode driver for Duo Core.
//
// Like Pi, opencode is driven two ways at once: this package is the Host Adapter
// and opencode-extension/ is the Agent Adapter, running inside the opencode
// process. The one thing that differs from Pi is the session identity: opencode
// assigns session ids server-side, so this plugin never chooses one and only
// replays an id the server has already issued.
package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/atfa/duo/internal/driver"
)

// Name is the driver name an operator selects with `--agent opencode`.
const Name = "opencode"

// DriverVersion is this plugin's version, independent of the Duo binary version.
const DriverVersion = "1.0.0"

// sessionFlags are the flags through which an operator may already have chosen a
// session.
var sessionFlags = []string{"--session", "-s", "--continue", "-c", "--fork"}

// sessionIDPrefix is what an opencode session id starts with. It is checked so a
// placeholder from an older Duo is never replayed: opencode answers an id it never
// issued with "Session not found" and opens a different session.
const sessionIDPrefix = "ses_"

// state is the plugin's private resume blob. Core stores it verbatim and never
// reads it.
type state struct {
	SessionID string `json:"sessionId"`
	// SessionFile is where the Agent Adapter reports the id opencode assigned. It
	// is a file rather than a message because the extension runs inside the agent
	// process and has no other way to reach this one before the session exists.
	SessionFile string `json:"sessionFile,omitempty"`
}

// Plugin is the opencode Host Adapter.
type Plugin struct{}

// New returns the opencode driver.
func New() driver.Handler { return Plugin{} }

// Register makes opencode available to Duo in-process.
func Register() { driver.Register(Name, func() driver.Handler { return New() }) }

func (Plugin) Describe() (*driver.Manifest, error) {
	return &driver.Manifest{
		Protocol:    driver.ProtocolVersion,
		Name:        Name,
		Version:     DriverVersion,
		Description: "opencode CLI coding agent (socket bridge)",
		Agent:       driver.AgentInfo{CLI: Name, DefaultCommand: Name},
		// opencode only accepts provider/model, so a bare id would abort the agent
		// at startup and Core must join the two back together.
		DefaultModel:   DefaultModel(),
		ModelReference: driver.ModelQualified,
		Capabilities: driver.Capabilities{
			// opencode mints session ids server-side and rejects one it never
			// issued, so this plugin sends nothing on a first run and replays only
			// what the Agent Adapter has learned.
			Resume: driver.ResumeServer,
			Models: true,
			// The reasoning effort is a launch flag, spelled --variant, and applied
			// by restarting the agent.
			Thinking:           true,
			LiveModelSwitch:    false,
			LiveThinkingSwitch: false,
			Activity:           true,
			ContextUsage:       true,
			TokenRate:          true,
			// opencode-extension connects from inside the opencode process and
			// injects through client.session.promptAsync, so its endpoint does
			// receive prompts mid-turn.
			Bridge:       driver.BridgeAgent,
			MCP:          true,
			PTYFallback:  true,
			LiveSteering: true,
			// opencode only creates a session once the human or the agent sends
			// something, so the bridge attaches late and Core announces the
			// connection at process start.
			SelfReports: false,
		},
	}, nil
}

func (Plugin) Probe() (*driver.ProbeResult, error) {
	path, err := exec.LookPath(Name)
	if err != nil {
		return &driver.ProbeResult{Available: false, Reason: "the opencode CLI is not on PATH"}, nil
	}
	return &driver.ProbeResult{Available: true, AgentPath: path}, nil
}

// Prepare builds the command line and the identity to replay.
//
// --session is only injected once this plugin knows an id opencode has issued. On
// a first run there is none, and opencode must be allowed to mint its own; the
// Agent Adapter reports it and the next launch replays it.
func (Plugin) Prepare(req driver.LaunchRequest) (*driver.LaunchPlan, error) {
	var prior state
	if len(req.State) > 0 {
		if err := json.Unmarshal(req.State, &prior); err != nil {
			return nil, &driver.RPCError{
				Code:    driver.CodeInvalid,
				Message: "opencode resume state is not readable: " + err.Error(),
			}
		}
	}

	base := strings.TrimSpace(req.BaseCommand)
	if base == "" {
		base = Name
	}

	command := driver.SetFlag(base, "--model", strings.TrimSpace(req.Model))
	// opencode spells reasoning effort --variant, not --thinking.
	command = driver.SetFlag(command, "--variant", strings.TrimSpace(req.Thinking))

	parts := []string{command}
	// A placeholder from an older Duo is ignored rather than passed through: it
	// would produce "Session not found" on every launch. It is dropped entirely,
	// not just withheld from the command line, because the value that is reported
	// is also the value persisted — keeping it would replay the rejection forever.
	sessionID := strings.TrimSpace(prior.SessionID)
	if !strings.HasPrefix(sessionID, sessionIDPrefix) {
		sessionID = ""
	}
	if sessionID != "" && !driver.HasFlag(command, sessionFlags...) {
		parts = append(parts, "--session "+driver.ShellQuote(sessionID))
	}
	// Agents run unattended in their own worktree, so tool calls must not block on
	// an interactive permission prompt.
	if !strings.Contains(command, "--auto") {
		parts = append(parts, "--auto")
	}

	sessionFile := prior.SessionFile
	if sessionFile == "" {
		who := strings.ToLower(strings.TrimSpace(string(req.Agent)))
		if who == "" {
			who = "agent"
		}
		sessionFile = filepath.Join(os.TempDir(), fmt.Sprintf("duo-opencode-%s.session", who))
	}

	next := state{SessionID: sessionID, SessionFile: sessionFile}
	raw, err := json.Marshal(next)
	if err != nil {
		return nil, err
	}
	return &driver.LaunchPlan{
		Command: strings.Join(parts, " "),
		Env: map[string]string{
			"DUO_OPENCODE_SESSION_ID":   sessionID,
			"DUO_OPENCODE_SESSION_FILE": sessionFile,
		},
		State:           raw,
		SessionIdentity: sessionID,
		// The Agent Adapter rewrites this file with the id opencode assigned, so a
		// stale value from a previous run must never be read as this run's.
		Cleanup: []string{sessionFile},
	}, nil
}

func (Plugin) Models(ctx context.Context) (*driver.ModelList, error) {
	return driver.RunModelList(ctx, ListCommand(), ParseModels)
}

func (Plugin) Thinking(ctx context.Context) (*driver.ThinkingOptions, error) {
	return &driver.ThinkingOptions{Levels: []string{"off", "low", "medium", "high"}, Default: "medium"}, nil
}

// ListCommand builds the model-listing invocation.
//
// opencode takes a subcommand that accepts no flags of its own. Only the
// executable is reused: appending a subcommand to the launch command would be read
// as the positional project path ("opencode --auto models" tries to open a
// directory named "models"), and carrying over agent flags such as --model makes
// the subcommand print its usage instead of a catalog.
func ListCommand() string { return Name + " models" }

// ParseModels reads the output of `opencode models`, which prints one plain
// provider/model reference per line. The provider is split on the first slash
// because a model id may itself contain one.
func ParseModels(output string) ([]driver.Model, error) {
	var list []driver.Model
	seen := make(map[string]bool)
	output = strings.ReplaceAll(output, "\r", "\n")
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		provider, id, ok := strings.Cut(line, "/")
		if !ok {
			continue
		}
		provider = strings.TrimSpace(provider)
		id = strings.TrimSpace(id)
		if provider == "" || id == "" {
			continue
		}
		key := provider + "/" + id
		if seen[key] {
			continue
		}
		seen[key] = true
		list = append(list, driver.Model{Provider: provider, ID: id})
	}
	return list, nil
}

// DefaultModel returns the model opencode runs with when the operator chose none.
//
// opencode resolves its own default from global config and then its built-in
// catalog. Injecting a foreign provider id here would fail with "Model not found",
// so the human's configured value is used and nothing is guessed.
func DefaultModel() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	configDir := filepath.Join(home, ".config", "opencode")
	var data []byte
	var readErr error
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		data, readErr = os.ReadFile(filepath.Join(configDir, name))
		if readErr == nil {
			break
		}
	}
	if readErr != nil {
		return ""
	}
	// The config may be JSONC, so a comment must not fail the parse.
	if stripped := stripJSONComments(string(data)); stripped != "" {
		data = []byte(stripped)
	}
	var cfg struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(data, &cfg) != nil {
		return ""
	}
	return strings.TrimSpace(cfg.Model)
}

// stripJSONComments removes // and /* */ comments so a JSONC config can be parsed
// with encoding/json. String literals are preserved.
func stripJSONComments(src string) string {
	var out strings.Builder
	inString := false
	escaped := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inString {
			out.WriteByte(c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out.WriteByte(c)
			continue
		}
		if c == '/' && i+1 < len(src) {
			switch src[i+1] {
			case '/':
				for i < len(src) && src[i] != '\n' {
					i++
				}
				out.WriteByte('\n')
				continue
			case '*':
				i += 2
				for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
					i++
				}
				i++
				continue
			}
		}
		out.WriteByte(c)
	}
	return strings.TrimSpace(out.String())
}

// watchInterval is how often the reported session id is re-read. The Agent Adapter
// writes it once, so this is a slow poll for a file that appears exactly once.
var watchInterval = 250 * time.Millisecond

// LearnSessionID blocks until the Agent Adapter reports the session id opencode
// assigned, and returns it. An empty string means the agent never reported one,
// which is not an error: the run still works, it just cannot be resumed.
func LearnSessionID(ctx context.Context, sessionFile string) string {
	if strings.TrimSpace(sessionFile) == "" {
		return ""
	}
	ticker := time.NewTicker(watchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ""
		case <-ticker.C:
			data, err := os.ReadFile(sessionFile)
			if err != nil {
				continue
			}
			id := strings.TrimSpace(string(data))
			// The prefix is opencode's id format, not Duo's guess. Anything else is
			// a half-written file or a stale value from another run.
			if !strings.HasPrefix(id, sessionIDPrefix) {
				continue
			}
			return id
		}
	}
}
