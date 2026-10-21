package gotui

import (
	"context"
	"io"

	"github.com/atfa/duo/internal/models"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
)

// Session is everything this frontend needs from Duo Core and nothing else.
//
// Keeping the method set local means this package depends only on the
// session.Service seam; the compile-time assertion below is the contract that
// *session.Service still implements it.
type Session interface {
	View() session.View
	History() []sessionstore.TUIEntry
	Subscribe(buffer int) (session.Subscription, func())
	Do(ctx context.Context, cmd session.Command) error
	Models(ctx context.Context, agent protocol.AgentID) ([]models.Model, error)
	Drivers(ctx context.Context) ([]session.DriverChoice, error)
	StatusText(ctx context.Context) string
	Changes(ctx context.Context) []string
	Notices() []string
	RecordEntry(entry sessionstore.TUIEntry)

	Attach(agent protocol.AgentID, w io.Writer) ([]byte, error)
	Detach(agent protocol.AgentID)
	Write(agent protocol.AgentID, data []byte) error
	Resize(cols, rows int) error
}

var _ Session = (*session.Service)(nil)
