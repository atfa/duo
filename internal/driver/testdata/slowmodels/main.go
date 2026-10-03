// Command slowmodels is a test fixture for one contract behaviour: a method that
// runs out of time is not a method that is missing.
//
// It returns the same error RunModelList produces when ModelListTimeout expires, so
// the contract suite's handling of a slow listing is exercised over a real process
// rather than assumed to work because the message reads well.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/atfa/duo/internal/driver"
)

type plugin struct{}

func (plugin) Describe() (*driver.Manifest, error) {
	return &driver.Manifest{
		Protocol:       driver.ProtocolVersion,
		Name:           "slow",
		Version:        "1.0.0",
		Description:    "test fixture whose model listing times out",
		Agent:          driver.AgentInfo{CLI: "slow", DefaultCommand: "slow"},
		ModelReference: driver.ModelBare,
		Capabilities:   driver.Capabilities{Resume: driver.ResumeServer, Models: true, Bridge: driver.BridgeNone},
	}, nil
}

func (plugin) Probe() (*driver.ProbeResult, error) {
	return &driver.ProbeResult{Available: true, AgentPath: "/bin/sh"}, nil
}

func (plugin) Prepare(driver.LaunchRequest) (*driver.LaunchPlan, error) {
	return &driver.LaunchPlan{Command: "sh -c 'exit 0'", State: []byte(`{"sessionId":"slow"}`)}, nil
}

// Models reproduces a listing that ran out of time.
func (plugin) Models() (*driver.ModelList, error) {
	return nil, fmt.Errorf("list models: did not finish within 20s: %w", context.DeadlineExceeded)
}

func main() {
	if err := driver.Serve(os.Stdin, os.Stdout, plugin{}); err != nil {
		log.Fatal(err)
	}
}
