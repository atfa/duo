package session

import (
	"strings"

	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/models"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// DefaultDriver is the driver an agent gets when nothing names one. It is a name
// and nothing more: Duo Core branches on nothing here, and the driver it selects
// answers for its own binary, models and defaults from its manifest.
const DefaultDriver = "pi"

// Options is the whole of a frontend's configuration for one Duo session.
//
// It is deliberately frontend-neutral: nothing here refers to a terminal, a flag
// parser or an environment variable. A CLI fills it from flags, an MCP client from
// a JSON body, and a graphical frontend from its own settings, and Duo Core starts
// the same session for all of them.
type Options struct {
	// Where the session runs. LaunchDir is where the frontend was invoked, and
	// Repository is empty to discover the repository from it. BaseDir is where
	// session records live, and is empty for the default location.
	LaunchDir  string
	Repository string
	BaseDir    string

	// Identity and isolation of a NEW session.
	Session      string
	BaseRef      string
	WorktreeRoot string
	Mode         project.Mode
	ModeSource   string

	// Resume reloads a persisted session instead of creating one. ResumeSession
	// names it; empty means "the single unfinished session".
	Resume        bool
	ResumeSession string
	// ModeRaw and ModeExplicit carry an operator's requested mode so a resume can
	// refuse an explicit disagreement with the persisted mode and warn about an
	// ambient one.
	ModeRaw      string
	ModeExplicit bool

	// Listen is the address the agent bridge binds to.
	Listen string

	// Harness.
	HarnessEnabled bool
	Harness        harness.Config

	// Drivers. Every map is keyed by agent. A missing entry means "the driver's
	// own default", never a name Core made up.
	AgentCommands       map[protocol.AgentID]string
	AgentDrivers        map[protocol.AgentID]string
	AgentModels         map[protocol.AgentID]string
	AgentThinkings      map[protocol.AgentID]string
	AgentDriverExplicit map[protocol.AgentID]bool
	TestCommand         string

	// RegisterDrivers registers the driver plugins this build ships. It is a
	// function rather than an import because the plugins live in cmd/duo: keeping
	// every plugin import in the CLI is what makes adding a fourth agent a one-line
	// change there and nothing at all in Core. It is called once, by New.
	RegisterDrivers func()
}

// AgentDriver names the driver an agent should launch.
//
// It is a free function rather than a method so the CLI's own configuration type
// can answer the same question with the same rule instead of a second copy of it.
func AgentDriver(drivers map[protocol.AgentID]string, agent protocol.AgentID) string {
	if drv, ok := drivers[agent]; ok && drv != "" {
		return drv
	}
	return DefaultDriver
}

// AgentDriver names the driver an agent should launch.
func (o *Options) AgentDriver(agent protocol.AgentID) string {
	return AgentDriver(o.AgentDrivers, agent)
}

// ResolveDriver settles the three things only the driver can answer: which binary
// to run, which model to start on, and what the operator's requested effort means.
//
// It runs after the driver resolves rather than while configuration is parsed, and
// that ordering is the whole point rather than an inconvenience: a driver's
// manifest is the only thing that knows the spelling of its model references, its
// default, or the binary it prefers, and reading any of them earlier is what made
// Core carry a table of driver names.
func ResolveDriver(
	commands, mods, thinkings map[protocol.AgentID]string,
	agent protocol.AgentID,
	manifest *driver.Manifest,
) (command, model, effort string) {
	command = strings.TrimSpace(commands[agent])
	model = strings.TrimSpace(mods[agent])
	effort = strings.TrimSpace(thinkings[agent])
	if manifest == nil {
		return command, model, effort
	}
	if command == "" {
		command = strings.TrimSpace(manifest.Agent.DefaultCommand)
	}
	// A reference spelled for another driver is folded into this one's, because a
	// model the agent has never heard of aborts it at startup rather than being
	// ignored.
	model = models.Apply(model, manifest.ModelReference)
	// The other direction: a bare id left behind by a driver that takes bare ones is
	// just as unusable here, and this one aborts rather than ignoring it. Dropping it
	// lets the manifest's default stand, or nothing at all if the driver declares
	// none — which is the same answer the agent would have given on its own.
	if manifest.ModelReference == driver.ModelQualified && model != "" && !strings.Contains(model, "/") {
		model = ""
	}
	if model == "" {
		model = models.Default(manifest)
	}
	return command, model, effort
}

// ResolveDriver settles the binary, model and effort for an agent.
func (o *Options) ResolveDriver(agent protocol.AgentID, manifest *driver.Manifest) (command, model, effort string) {
	return ResolveDriver(o.AgentCommands, o.AgentModels, o.AgentThinkings, agent, manifest)
}
