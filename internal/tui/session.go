package tui

import (
	"context"
	"io"

	"github.com/atfa/duo/internal/models"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
)

// Session is everything the terminal interface needs from Duo Core, and nothing
// else is reachable from here.
//
// It is an interface rather than the concrete service for one reason that matters:
// the list below is the complete surface this frontend may use. A method the TUI
// needs must be added here, in the open, rather than reached for through a struct
// field — which is how the interface and the core became entangled in the first
// place. *session.Service implements it.
type Session interface {
	// View returns the current projection. Rendering reads only this.
	View() session.View
	// History returns the conversation recorded before a resumed session.
	History() []sessionstore.TUIEntry
	// Subscribe returns the current View and a stream of later events.
	Subscribe(buffer int) (session.Subscription, func())
	// Do performs one human verb.
	Do(ctx context.Context, cmd session.Command) error
	// Models reads an agent's model catalog.
	Models(ctx context.Context, agent protocol.AgentID) ([]models.Model, error)
	// StatusText renders the workflow status.
	StatusText(ctx context.Context) string
	// Changes lists what the session changed relative to its base.
	Changes(ctx context.Context) []string
	// Notices returns the diagnostics buffered before the interface existed.
	Notices() []string
	// RecordEntry writes one interface entry to the journal and transcript.
	RecordEntry(entry sessionstore.TUIEntry)

	// The agent process surface. It is deliberately separate from Do: these move
	// a live byte stream, which cannot be serialized as a command.
	Attach(agent protocol.AgentID, w io.Writer) ([]byte, error)
	Detach(agent protocol.AgentID)
	Write(agent protocol.AgentID, data []byte) error
	Resize(cols, rows int) error
}
