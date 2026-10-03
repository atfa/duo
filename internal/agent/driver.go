package agent

import (
	"context"
	"encoding/json"
	"io"

	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/protocol"
)

// Driver is the interface the coordinator and the TUI hold for one agent process.
//
// It is deliberately split in two. The first block is process lifecycle: starting,
// stopping, resizing, attaching and writing — all generic, and identical for every
// coding agent. The second block is the driver's declared behaviour: what it can
// do and what state it owns. Everything Core knows about a specific agent comes
// from those declarations, so adding an agent never changes this interface.
type Driver interface {
	Agent() protocol.AgentID

	// Process lifecycle.
	Start(ctx context.Context) error
	Stop()
	Restart(ctx context.Context) error
	RestartRunning(ctx context.Context) error
	State() ProcessState
	Running() bool
	Resize(cols, rows int) error
	Attach(w io.Writer) []byte
	Detach()
	Write(p []byte) error
	EffectiveCommand() string
	Command() string
	SetOnExit(fn func(ExitEvent))
	SetModel(model string)
	Model() string
	SetEffort(effort string)
	Close()

	// The driver's self-description. DriverType is a name for display; the
	// manifest is the only thing Core is allowed to branch on.
	DriverType() string
	Manifest() *driver.Manifest
	Capabilities() driver.Capabilities

	// The driver's own opaque state, and the label it reports for this agent's
	// conversation. SessionID is display-only; nothing parses it.
	DriverState() json.RawMessage
	SessionID() string
}
