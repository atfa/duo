// Command blankplugin is a test fixture, not a shipped plugin. It is a real
// out-of-process plugin whose whole purpose is to answer `state` with nothing, so
// the blank-blob path is exercised over a real pipe rather than in process.
//
// In-process tests cannot catch this: a Go nil marshals to {"state":null} on the
// wire and decodes back to the four bytes "null", which a naive length check reads
// as a real answer. DUO_BLANK_STATE picks the shape.
package main

import (
	"encoding/json"
	"log"
	"os"

	"github.com/atfa/duo/internal/driver"
)

type plugin struct{}

func (plugin) Describe() (*driver.Manifest, error) {
	return &driver.Manifest{
		Protocol:       driver.ProtocolVersion,
		Name:           "blank",
		Version:        "1.0.0",
		Description:    "test fixture whose state method reports nothing",
		Agent:          driver.AgentInfo{CLI: "blank", DefaultCommand: "blank"},
		ModelReference: driver.ModelBare,
		Capabilities: driver.Capabilities{
			// server, because that is the only shape Core asks.
			Resume: driver.ResumeServer,
			Bridge: driver.BridgeNone,
		},
	}, nil
}

func (plugin) Probe() (*driver.ProbeResult, error) {
	return &driver.ProbeResult{Available: true, AgentPath: "/bin/sh"}, nil
}

func (plugin) Prepare(driver.LaunchRequest) (*driver.LaunchPlan, error) {
	return &driver.LaunchPlan{
		Command: "sh -c 'exit 0'",
		State:   json.RawMessage(`{"sessionId":"good-id-from-prepare"}`),
	}, nil
}

// State reports nothing at all, which is what a plugin answers before it has run a
// prepare — the state of every process Core starts to replace a crashed one.
func (plugin) State() (json.RawMessage, error) {
	if os.Getenv("DUO_BLANK_STATE") == "null" {
		return nil, nil
	}
	return json.RawMessage(`{}`), nil
}

func main() {
	if err := driver.Serve(os.Stdin, os.Stdout, plugin{}); err != nil {
		log.Fatal(err)
	}
}
