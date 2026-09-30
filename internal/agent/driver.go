package agent

import (
	"context"
	"io"

	"github.com/atfa/duo/internal/protocol"
)

// Driver is the common interface implemented by all Agent execution engines (Pi, Agy, etc.).
type Driver interface {
	Agent() protocol.AgentID
	DriverType() string // "pi", "agy", etc.
	Start(ctx context.Context) error
	Stop()
	Restart(ctx context.Context) error
	State() ProcessState
	Running() bool
	Resize(cols, rows int) error
	Attach(w io.Writer) []byte
	Detach()
	Write(p []byte) error
	EffectiveCommand() string
	SessionID() string
	Command() string
	SetOnExit(fn func(ExitEvent))
}
