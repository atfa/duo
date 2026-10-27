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
	// RecallPreview reports what /recall would discard, and whether the session
	// may be recalled at all. A git read failure returns an error: the frontend
	// must not open the panel on an answer it cannot trust.
	RecallPreview(ctx context.Context) (session.RecallPreview, error)
	Changes(ctx context.Context) []string
	Notices() []string
	// Sessions lists the sessions this repository can resume right now. It is
	// empty for a session that never started; the picker then says so.
	Sessions() []sessionstore.Summary
	RecordEntry(entry sessionstore.TUIEntry)
	// RequestObserverReport asks the advisory observer for one status report. It
	// returns immediately; the answer arrives through View().Observer.Reports.
	RequestObserverReport()
	// RequestObserverAnswer asks the advisory observer one human-typed question.
	// It returns immediately; the answer arrives through the same Reports list
	// with the question attached. Blank input is a no-op.
	RequestObserverAnswer(question string)

	Attach(agent protocol.AgentID, w io.Writer) ([]byte, error)
	Detach(agent protocol.AgentID)
	Write(agent protocol.AgentID, data []byte) error
	Resize(cols, rows int) error
}

var _ Session = (*session.Service)(nil)
