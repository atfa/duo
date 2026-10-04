package session

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/coordinator"
	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// Switching a model or an effort level has three consequences that only exist
// inside the session: the reference the driver is handed, whether its process is
// bounced so a startup flag takes effect, and whether a bounce that failed is
// reported instead of a switch that never happened. None of them are visible from
// the interface — which is the point of moving them here — so they are pinned here.

// modelDriver is a driver that does nothing but remember what the session told it.
type modelDriver struct {
	agent.Driver // methods this test never reaches stay unimplemented

	id        protocol.AgentID
	reference string // driver.ModelQualified or driver.ModelBare
	// liveModel declares that the driver picks a model up over the bridge, so no
	// restart is needed. liveThink is the same for an effort level.
	liveModel  bool
	liveThink  bool
	restartErr error

	restarts int
	model    string
	effort   string
}

func (d *modelDriver) Agent() protocol.AgentID         { return d.id }
func (d *modelDriver) DriverType() string              { return "fake" }
func (d *modelDriver) Model() string                   { return d.model }
func (d *modelDriver) SetModel(model string)           { d.model = model }
func (d *modelDriver) SetEffort(effort string)         { d.effort = effort }
func (d *modelDriver) SetOnExit(func(agent.ExitEvent)) {}
func (d *modelDriver) State() agent.ProcessState       { return agent.ProcessExited }
func (d *modelDriver) Running() bool                   { return false }
func (d *modelDriver) RestartRunning(context.Context) error {
	d.restarts++
	return d.restartErr
}

func (d *modelDriver) Manifest() *driver.Manifest {
	ref := d.reference
	if ref == "" {
		ref = driver.ModelQualified
	}
	return &driver.Manifest{
		Protocol:       driver.ProtocolVersion,
		Name:           "fake",
		ModelReference: ref,
		Capabilities:   d.Capabilities(),
	}
}

func (d *modelDriver) Capabilities() driver.Capabilities {
	return driver.Capabilities{
		Resume: driver.ResumeServer, Bridge: driver.BridgeAgent, PTYFallback: true,
		LiveModelSwitch: d.liveModel, LiveThinkingSwitch: d.liveThink,
	}
}

// modelSession builds just enough of a service for a command that reaches a driver:
// a coordinator with no socket, and a manager holding the driver under test.
func modelSession(t *testing.T, d *modelDriver) *Service {
	t.Helper()
	state := project.NewStateFor(project.ModeFast)
	agents := agent.NewManager()
	agents.Add(d)
	return &Service{
		state:  state,
		coord:  coordinator.New(nil, state, harness.NewTracker(), nil, events.NewBus()),
		agents: agents,
	}
}

func TestSetModelGivesTheDriverAReferenceItCanUse(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reference string
		want      string
	}{
		// opencode only accepts provider/model and aborts on a bare id.
		{"qualified", driver.ModelQualified, "cline/gemini-3.8-flash"},
		// A driver that strips the prefix itself must be given the bare one.
		{"bare", driver.ModelBare, "gemini-3.8-flash"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &modelDriver{id: protocol.Austin, reference: tc.reference}
			svc := modelSession(t, d)

			if err := svc.Do(context.Background(), SetModel{Agent: protocol.Austin, Provider: "cline", Model: "gemini-3.8-flash"}); err != nil {
				t.Fatalf("SetModel: %v", err)
			}
			if d.model != tc.want {
				t.Fatalf("driver model = %q, want %q", d.model, tc.want)
			}
		})
	}
}

// The launch flag only takes effect on a new process, so a driver that cannot switch
// live has to be restarted — and one that can switch live must not be.
func TestSetModelRestartsOnlyWhenTheDriverCannotSwitchLive(t *testing.T) {
	for _, tc := range []struct {
		name     string
		live     bool
		restarts int
	}{
		{"bridge switch needs no restart", true, 0},
		{"launch flag needs a restart", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &modelDriver{id: protocol.Austin, liveModel: tc.live}
			svc := modelSession(t, d)

			if err := svc.Do(context.Background(), SetModel{Agent: protocol.Austin, Provider: "cline", Model: "gemini-3.8-flash"}); err != nil {
				t.Fatalf("SetModel: %v", err)
			}
			if d.restarts != tc.restarts {
				t.Fatalf("restarts = %d, want %d", d.restarts, tc.restarts)
			}
		})
	}
}

// A restart that failed leaves the agent dead, so the command must fail rather than
// report a model the agent never received.
func TestSetModelReportsAFailedRestart(t *testing.T) {
	d := &modelDriver{id: protocol.Austin, restartErr: errors.New("pty gone")}
	svc := modelSession(t, d)

	err := svc.Do(context.Background(), SetModel{Agent: protocol.Austin, Provider: "cline", Model: "gemini-3.8-flash"})
	if err == nil {
		t.Fatal("a failed restart was reported as a successful switch")
	}
	if !strings.Contains(err.Error(), "gemini-3.8-flash") {
		t.Errorf("error %q does not name the model that failed to apply", err)
	}
	if d.restarts != 1 {
		t.Fatalf("restarts = %d, want one attempt", d.restarts)
	}
}

func TestCycleThinkingRestartsOnlyWhenTheDriverCannotSwitchLive(t *testing.T) {
	for _, tc := range []struct {
		name     string
		live     bool
		restarts int
	}{
		{"bridge switch needs no restart", true, 0},
		{"launch flag needs a restart", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &modelDriver{id: protocol.Austin, liveThink: tc.live}
			svc := modelSession(t, d)

			if err := svc.Do(context.Background(), CycleThinking{Agent: protocol.Austin}); err != nil {
				t.Fatalf("CycleThinking: %v", err)
			}
			if d.restarts != tc.restarts {
				t.Fatalf("restarts = %d, want %d", d.restarts, tc.restarts)
			}
			if d.effort == "" {
				t.Error("the driver was never told the new effort level")
			}
		})
	}
}

func TestCycleThinkingReportsAFailedRestart(t *testing.T) {
	d := &modelDriver{id: protocol.Austin, restartErr: errors.New("pty gone")}
	svc := modelSession(t, d)

	err := svc.Do(context.Background(), CycleThinking{Agent: protocol.Austin})
	if err == nil {
		t.Fatal("a failed restart was reported as a successful change")
	}
	if d.restarts != 1 {
		t.Fatalf("restarts = %d, want one attempt", d.restarts)
	}
}
