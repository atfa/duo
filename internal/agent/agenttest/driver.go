// Package agenttest provides a configurable driver for tests that need an agent
// without running a real coding agent.
//
// It exists because Core's tests must exercise delivery decisions — does this
// message go over the bridge or to the terminal — and those decisions are made
// from a driver's declared capabilities. A stub that declares capabilities is
// therefore the only honest way to test them: a test that hard-codes a driver name
// would be testing the thing this refactor removes.
package agenttest

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/atfa/duo/internal/driver"
)

// Spec is what a stub driver declares and does.
type Spec struct {
	// Name is the driver name Core selects it by.
	Name string
	// Capabilities are what Core branches on.
	Capabilities driver.Capabilities
	// Command is the launch command prepare returns. Empty means "use whatever the
	// operator configured, or the driver name", which is what a real plugin does.
	Command string
	// Env is added to the agent process environment.
	Env map[string]string
	// State is the opaque resume blob prepare returns.
	State json.RawMessage
	// Notices are diagnostics prepare reports.
	Notices []string
	// Cleanup lists paths prepare asks Core to clear.
	Cleanup []string
	// Available is what probe reports.
	Available bool
	// ProbeReason is why probe says the CLI is unavailable.
	ProbeReason string
	// Models is the catalog the models method returns.
	Models []driver.Model
	// Thinking is the reasoning-effort set the thinking method returns.
	Thinking []string
}

// Driver is a stub implementing the plugin protocol's Handler interface.
type Driver struct {
	spec Spec

	mu       sync.Mutex
	prepares []driver.LaunchRequest
	closed   bool
}

// New returns a stub driver with sensible defaults: available, a trivial command
// line, and a plugin-owned bridge endpoint.
func New(name string) *Driver {
	return &Driver{spec: Spec{
		Name: name,
		Capabilities: driver.Capabilities{
			Resume:       driver.ResumeClient,
			Models:       true,
			Thinking:     true,
			Bridge:       driver.BridgeAgent,
			MCP:          true,
			PTYFallback:  true,
			LiveSteering: true,
			SelfReports:  true,
		},
		Available: true,
	}}
}

// WithCapabilities returns a copy of the spec with the given capabilities.
func (d *Driver) WithCapabilities(caps driver.Capabilities) *Driver {
	d.spec.Capabilities = caps
	return d
}

// With returns a copy of the spec with one field changed, for the options a test
// needs to vary.
func (d *Driver) With(apply func(*Spec)) *Driver {
	apply(&d.spec)
	return d
}

// Spec exposes the configured spec, so a test can assert what Core was handed.
func (d *Driver) Spec() Spec { return d.spec }

// Prepares returns every launch request Core made, in order.
func (d *Driver) Prepares() []driver.LaunchRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]driver.LaunchRequest(nil), d.prepares...)
}

// Closed reports whether Core released the driver.
func (d *Driver) Closed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.closed
}

// Capabilities is the driver-facing convenience for tests that hold a Caller.
func Capabilities(caller driver.Caller) driver.Capabilities {
	man, err := caller.Describe(context.Background())
	if err != nil {
		return driver.Capabilities{}
	}
	return man.Capabilities
}

// Caller wraps the stub as a Caller, ready to hand to a Session.
func (d *Driver) Caller() driver.Caller { return driver.NewBuiltin(d.spec.Name, d) }

func (s Spec) command() string {
	if strings.TrimSpace(s.Command) != "" {
		return s.Command
	}
	return s.Name
}

func (d *Driver) Describe() (*driver.Manifest, error) {
	return &driver.Manifest{
		Protocol:       driver.ProtocolVersion,
		Name:           d.spec.Name,
		Version:        "test",
		Agent:          driver.AgentInfo{CLI: d.spec.Name, DefaultCommand: d.spec.command()},
		ModelReference: driver.ModelQualified,
		Capabilities:   d.spec.Capabilities,
	}, nil
}

func (d *Driver) Probe() (*driver.ProbeResult, error) {
	return &driver.ProbeResult{
		Available: d.spec.Available,
		Reason:    d.spec.ProbeReason,
		AgentPath: d.spec.command(),
	}, nil
}

func (d *Driver) Prepare(req driver.LaunchRequest) (*driver.LaunchPlan, error) {
	d.mu.Lock()
	d.prepares = append(d.prepares, req)
	d.mu.Unlock()
	command := d.spec.command()
	if strings.TrimSpace(d.spec.Command) == "" && strings.TrimSpace(req.BaseCommand) != "" {
		command = req.BaseCommand
	}
	return &driver.LaunchPlan{
		Command:         command,
		Env:             d.spec.Env,
		State:           d.spec.State,
		SessionIdentity: "test-session",
		Cleanup:         d.spec.Cleanup,
		Notices:         d.spec.Notices,
	}, nil
}

func (d *Driver) Models() (*driver.ModelList, error) {
	return &driver.ModelList{Models: d.spec.Models}, nil
}

func (d *Driver) Thinking() (*driver.ThinkingOptions, error) {
	return &driver.ThinkingOptions{Levels: d.spec.Thinking}, nil
}
