package gotui

import (
	"context"
	"fmt"
	"image"
	"time"

	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/gdamore/tcell/v3"
	ui "github.com/metaspartan/gotui/v5"
)

// sessionsOverlay lists the sessions this repository can resume. It shows the
// facts a choice needs — mode, phase, how long it ran, what it cost — and Enter
// hands the id to the session, which is preserved and then resumed by the outer
// CLI exactly as `duo resume <id>` would be. The list is read when the picker
// opens: it is a snapshot of a choice, not session state.
type sessionsOverlay struct {
	*ui.Block
	theme  theme
	items  []sessionstore.Summary
	cursor int
	offset int
	box    image.Rectangle
}

func newSessions(t theme) *sessionsOverlay {
	s := &sessionsOverlay{Block: newModalBlock(t), theme: t}
	s.Title = " SESSIONS "
	return s
}

func (s *sessionsOverlay) set(items []sessionstore.Summary) {
	s.items = items
	s.cursor, s.offset = 0, 0
}

func (s *sessionsOverlay) place(l layout) {
	s.box = centered(l, 96, 20)
	s.Block.SetRect(s.box.Min.X, s.box.Min.Y, s.box.Max.X, s.box.Max.Y)
}

func (s *sessionsOverlay) move(delta int) {
	if len(s.items) == 0 {
		return
	}
	s.cursor = (s.cursor + delta + len(s.items)) % len(s.items)
}

func (s *sessionsOverlay) selected() (sessionstore.Summary, bool) {
	if len(s.items) == 0 {
		return sessionstore.Summary{}, false
	}
	return s.items[s.cursor], true
}

// sessionRan is how long the session was worked on: the span its own record was
// last written against the moment it was created. A resumed session keeps its
// original creation time, so this is the session's age rather than one sitting.
func sessionRan(item sessionstore.Summary) string {
	created, updated := item.Snapshot.CreatedAt, item.Snapshot.UpdatedAt
	if created.IsZero() || updated.IsZero() || updated.Before(created) {
		return "—"
	}
	return updated.Sub(created).Round(time.Second).String()
}

func sessionCost(item sessionstore.Summary) string {
	if item.Snapshot.Usage == nil {
		return "—"
	}
	return fmt.Sprintf("$%.2f", item.Snapshot.Usage.Total().CostUSD)
}

func (s *sessionsOverlay) header() string {
	return fmt.Sprintf("%-6s %-10s %-10s %-8s %-12s %s",
		s.theme.tr("MODE"), s.theme.tr("PHASE"), s.theme.tr("RAN"), s.theme.tr("COST"), s.theme.tr("UPDATED"), s.theme.tr("SESSION"))
}

func (s *sessionsOverlay) row(item sessionstore.Summary) string {
	updated := "—"
	if !item.Snapshot.UpdatedAt.IsZero() {
		updated = item.Snapshot.UpdatedAt.Local().Format("01-02 15:04")
	}
	return fmt.Sprintf("%-6s %-10s %-10s %-8s %-12s %s",
		item.Snapshot.EffectiveMode().Display(), item.Snapshot.Phase, sessionRan(item), sessionCost(item), updated, item.SessionID)
}

func (s *sessionsOverlay) rows() int { return max(1, s.Inner.Dy()-2) }

func (s *sessionsOverlay) Draw(buf *ui.Buffer) {
	fillBlock(buf, s.GetRect(), ui.Style{Fg: s.theme.Fg, Bg: s.theme.PanelHigh})
	s.Title = " " + s.theme.tr("SESSIONS") + " "
	s.Block.Draw(buf)
	width := s.Inner.Dx()
	if len(s.items) == 0 {
		buf.SetString(fitText(s.theme.tr("no resumable sessions in this repository"), width), s.theme.Subtle, image.Pt(s.Inner.Min.X, s.Inner.Min.Y))
		buf.SetString(fitText(s.theme.tr("Esc close"), width), s.theme.Timestamp, image.Pt(s.Inner.Min.X, s.Inner.Max.Y-1))
		return
	}
	buf.SetString(fitText(s.header(), width), s.theme.Timestamp, image.Pt(s.Inner.Min.X, s.Inner.Min.Y))
	rows := s.rows()
	if s.cursor < s.offset {
		s.offset = s.cursor
	}
	if s.cursor >= s.offset+rows {
		s.offset = s.cursor - rows + 1
	}
	for i := 0; i < rows; i++ {
		idx := s.offset + i
		if idx >= len(s.items) {
			break
		}
		style := s.theme.Body
		if idx == s.cursor {
			style = ui.Style{Fg: s.theme.Bg, Bg: s.theme.Accent, Modifier: ui.ModifierBold}
		}
		buf.SetString(fitText(s.row(s.items[idx]), width), style, image.Pt(s.Inner.Min.X, s.Inner.Min.Y+1+i))
	}
	buf.SetString(fitText(s.theme.tr("↑/↓ choose · Enter resume · Esc close"), width), s.theme.Timestamp, image.Pt(s.Inner.Min.X, s.Inner.Max.Y-1))
}

// openSessions reads the resumable sessions and shows the picker.
func (a *App) openSessions() {
	if a.svc == nil {
		return
	}
	a.sessions.set(a.svc.Sessions())
	a.overlay = overlaySessions
}

// handleSessionsKey drives the picker. Enter asks the session to switch: the
// session records the request, the view reports it, and the outer CLI resumes the
// chosen session after this one is closed and preserved.
func (a *App) handleSessionsKey(ev *tcell.EventKey) {
	switch ev.Key() {
	case tcell.KeyEsc:
		a.closeOverlay()
	case tcell.KeyUp:
		a.sessions.move(-1)
	case tcell.KeyDown:
		a.sessions.move(1)
	case tcell.KeyPgUp:
		a.sessions.move(-max(1, a.sessions.rows()-1))
	case tcell.KeyPgDn:
		a.sessions.move(max(1, a.sessions.rows()-1))
	case tcell.KeyEnter:
		item, ok := a.sessions.selected()
		if !ok {
			return
		}
		if err := a.svc.Do(context.Background(), session.ResumeSession{SessionID: item.SessionID}); err != nil {
			a.setStatus(err.Error(), true)
			return
		}
		a.closeOverlay()
		a.setStatus(fmt.Sprintf(a.theme.tr("resuming session %s…"), item.SessionID), false)
	}
}
