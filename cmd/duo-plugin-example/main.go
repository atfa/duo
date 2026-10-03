// Command duo-plugin-example is the reference implementation of the Duo Driver
// Plugin Protocol v1. It drives no real coding agent: it exists so the protocol
// is executable documentation, and so `duo plugin test` has something to verify
// that is known-good by construction.
//
// A real plugin is a main that serves the protocol on stdin/stdout and keeps
// whatever state it needs in the blob it returns from Prepare. Everything else —
// the wire format, the capability semantics, the contract checks — is here.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/atfa/duo/internal/driver"
)

// state is the plugin's private resume blob. Duo Core stores it verbatim and
// never reads it, which is what lets a new driver add resume support without a
// change to Duo's session schema.
type state struct {
	SessionID string `json:"sessionId"`
}

type plugin struct{}

// Describe is the plugin's self-description and the only thing Core reads to
// decide how to treat this driver.
func (plugin) Describe() (*driver.Manifest, error) {
	return &driver.Manifest{
		Protocol:       driver.ProtocolVersion,
		Name:           "example",
		Version:        "1.0.0",
		Description:    "Reference plugin for the Duo Driver Plugin Protocol",
		Agent:          driver.AgentInfo{CLI: "example-agent", DefaultCommand: "example-agent"},
		DefaultModel:   "example/default",
		ModelReference: driver.ModelQualified,
		Capabilities: driver.Capabilities{
			Resume:             driver.ResumeClient,
			Models:             true,
			Thinking:           true,
			LiveModelSwitch:    true,
			LiveThinkingSwitch: true,
			Activity:           true,
			ContextUsage:       true,
			TokenRate:          true,
			Bridge:             driver.BridgeAgent,
			MCP:                true,
			PTYFallback:        true,
			LiveSteering:       true,
			SelfReports:        true,
		},
	}, nil
}

// Probe reports whether the underlying agent CLI can be used right now. A
// missing CLI is a normal answer with Available false and a reason, not an
// error: Core reports the reason to the operator instead of failing a call.
func (plugin) Probe() (*driver.ProbeResult, error) {
	path, err := exec.LookPath("example-agent")
	if err != nil {
		return &driver.ProbeResult{
			Available: false,
			Reason:    "example-agent is not on PATH",
		}, nil
	}
	return &driver.ProbeResult{Available: true, AgentPath: path}, nil
}

// Prepare turns Core state into a runnable launch specification. Core runs the
// returned Command in a pseudo-terminal and persists State verbatim; it never
// inspects either.
func (plugin) Prepare(req driver.LaunchRequest) (*driver.LaunchPlan, error) {
	var prior state
	if len(req.State) > 0 {
		if err := json.Unmarshal(req.State, &prior); err != nil {
			return nil, &driver.RPCError{
				Code:    driver.CodeInvalid,
				Message: fmt.Sprintf("resume state: %v", err),
			}
		}
	}

	base := strings.TrimSpace(req.BaseCommand)
	if base == "" {
		base = "example-agent"
	}

	// ResumeClient means this plugin owns the identity: it chooses one on the
	// first run and replays it afterwards. A driver whose agent assigns ids
	// server-side declares ResumeServer and never passes an unissued id.
	id := prior.SessionID
	if id == "" {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		id = hex.EncodeToString(b)
	}

	command := base
	if prior.SessionID != "" {
		command += " --session-id " + shellQuote(id)
	}
	if model := strings.TrimSpace(req.Model); model != "" {
		command += " --model " + shellQuote(model)
	}
	if effort := strings.TrimSpace(req.Thinking); effort != "" {
		command += " --effort " + shellQuote(effort)
	}

	raw, err := json.Marshal(state{SessionID: id})
	if err != nil {
		return nil, err
	}
	return &driver.LaunchPlan{
		Command: command,
		// Plugin-owned environment. Duo Core never interprets these names; it
		// passes them to the agent process and nothing else.
		Env:             map[string]string{"DUO_EXAMPLE_SESSION_ID": id},
		State:           raw,
		SessionIdentity: id,
	}, nil
}

// Models lists the catalog. Core only calls it when Capabilities.Models is set,
// so a driver that cannot enumerate models simply omits the method.
func (plugin) Models() (*driver.ModelList, error) {
	return &driver.ModelList{Models: []driver.Model{
		{Provider: "example", ID: "default", DisplayName: "Example Default", ContextWindow: 200_000, Thinking: true, Images: true},
		{Provider: "example", ID: "mini", DisplayName: "Example Mini", ContextWindow: 128_000},
	}}, nil
}

// Thinking lists reasoning-effort levels, gated on Capabilities.Thinking.
func (plugin) Thinking() (*driver.ThinkingOptions, error) {
	return &driver.ThinkingOptions{
		Levels:  []string{"off", "low", "medium", "high"},
		Default: "medium",
	}, nil
}

func main() {
	// --version is answered without touching the protocol so an operator can
	// check which plugin binary they have.
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Printf("duo-plugin-example 1.0.0 (protocol %d)\n", driver.ProtocolVersion)
		return
	}
	_ = context.Background()
	if err := driver.Serve(os.Stdin, os.Stdout, plugin{}); err != nil {
		log.Fatalf("duo-plugin-example: %v", err)
	}
}

// shellQuote quotes a value for /bin/sh. %q produces Go escaping, not shell
// escaping, so a value containing $ or a backtick must be quoted the shell way.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
