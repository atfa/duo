package agent

import (
	"context"
	"io"

	"github.com/atfa/duo/internal/protocol"
)

// SelfReportsOnLaunch reports whether a driver's bridge attaches as soon as its
// process starts, which is what lets the coordinator announce the connection on
// its own.
//
// pi does. agy has no bridge at all, and opencode's only attaches once its TUI
// has created a session, so neither can be announced that way: the caller has to
// say so when the process comes up instead. Everything else, including external
// plugin drivers, is assumed to have a working bridge, which is how it behaved
// before this distinction existed.
func SelfReportsOnLaunch(driverType string) bool {
	switch normalizeDriverType(driverType) {
	case "agy", "opencode":
		return false
	default:
		return true
	}
}

// Driver is the common interface implemented by all Agent execution engines (Pi, Agy, opencode).
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
	Model() string
	SetOnExit(fn func(ExitEvent))
	SetModel(model string)
	SetEffort(effort string)
	RestartRunning(ctx context.Context) error
}
