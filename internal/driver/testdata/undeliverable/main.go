// Command undeliverable is a test fixture for the one manifest Core cannot serve:
// a driver with no way to receive a prompt. It declares bridge:none, so no endpoint
// can carry one, and ptyFallback:false, so nothing typed at the terminal reaches it.
//
// The contract suite uses it to prove such a driver is reported rather than
// silently accepted — the failure being a prompt that looks delivered and is not.
package main

import (
	"log"
	"os"

	"github.com/atfa/duo/internal/driver"
)

type plugin struct{}

func (plugin) Describe() (*driver.Manifest, error) {
	return &driver.Manifest{
		Protocol:       driver.ProtocolVersion,
		Name:           "undeliverable",
		Version:        "1.0.0",
		Description:    "test fixture a prompt cannot reach",
		Agent:          driver.AgentInfo{CLI: "undeliverable", DefaultCommand: "sh"},
		ModelReference: driver.ModelBare,
		Capabilities: driver.Capabilities{
			Resume:      driver.ResumeClient,
			Bridge:      driver.BridgeNone,
			PTYFallback: false,
		},
	}, nil
}

func (plugin) Probe() (*driver.ProbeResult, error) {
	return &driver.ProbeResult{Available: true, AgentPath: "/bin/sh"}, nil
}

func (plugin) Prepare(driver.LaunchRequest) (*driver.LaunchPlan, error) {
	return &driver.LaunchPlan{
		Command: "sh -c 'exit 0'",
		State:   []byte(`{"sessionId":"undeliverable"}`),
		Notices: []string{"this driver has no delivery path; the contract suite expects to be told so"},
	}, nil
}

func main() {
	if err := driver.Serve(os.Stdin, os.Stdout, plugin{}); err != nil {
		log.Fatal(err)
	}
}
