package gotui

import (
	"context"
	"fmt"
	"image"
	"strings"
	"time"

	"github.com/atfa/duo/internal/protocol"
	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui/v5"
	"github.com/skip2/go-qrcode"
)

// RemoteStatus is the remote pairing as a frontend may render it. It carries no
// secret: the secret exists only inside the pairing URL, which is drawn as a QR
// code and never printed.
type RemoteStatus struct {
	Enabled     bool
	Server      string
	Room        string
	ExpiresAt   time.Time
	Connected   bool
	HostPresent bool
	Guests      int
	LastError   string
}

// RemotePairing is one pairing a frontend may show.
//
// URL is the full link, including the fragment that carries the pairing secret;
// it is used only to draw the QR code. Display is the same link without the
// fragment, so it is safe to print on screen and in a log.
type RemotePairing struct {
	URL     string
	Display string
	Status  RemoteStatus
}

// Remote is the terminal frontend's whole view of remote access. The session
// owns the pairing file and the connection; this package only asks and renders.
type Remote interface {
	// Start connects, minting a pairing when none exists or rotating it when
	// rotate is set.
	Start(ctx context.Context, rotate bool) (RemotePairing, error)
	// Stop revokes the pairing and disconnects.
	Stop(ctx context.Context) (RemoteStatus, error)
	// Status reports the current pairing without touching the network.
	Status() RemoteStatus
}

// qrcodeOverlay shows the pairing QR code. A phone camera app scans it, opens
// the page and decrypts the first state, which is the whole of pairing.
type qrcodeOverlay struct {
	*ui.Block
	theme   theme
	pairing RemotePairing
	status  RemoteStatus
	lines   []renderLine
	width   int
	box     image.Rectangle
}

func newQrcode(t theme) *qrcodeOverlay {
	q := &qrcodeOverlay{Block: newModalBlock(t), theme: t}
	q.Title = " " + t.tr("REMOTE PAIRING") + " "
	return q
}

func (q *qrcodeOverlay) place(l layout) {
	q.box = centered(l, max(24, q.width+2), max(12, len(q.lines)+2))
	q.Block.SetRect(q.box.Min.X, q.box.Min.Y, q.box.Max.X, q.box.Max.Y)
}

func (q *qrcodeOverlay) rebuild(pairing RemotePairing, status RemoteStatus) {
	q.pairing = pairing
	q.status = status
	q.lines = nil
	add := func(text string, style ui.Style) {
		q.lines = append(q.lines, makeLine(text, style, 0))
	}
	qr, err := qrLines(pairing.URL)
	if err != nil {
		add(q.theme.tr("the pairing code could not be drawn"), q.theme.Error)
	} else {
		q.lines = append(q.lines, qr...)
	}
	q.width = 0
	for _, line := range q.lines {
		for _, run := range line.runs {
			q.width = max(q.width, rw.StringWidth(run.text))
		}
	}
	add("", q.theme.Subtle)
	if pairing.Display != "" {
		add(pairing.Display, q.theme.Subtle)
	}
	add(fmt.Sprintf("%s %s", q.theme.tr("room"), status.Room), q.theme.BodyMuted)
	if !status.ExpiresAt.IsZero() {
		add(fmt.Sprintf("%s %s", q.theme.tr("expires"), humanUntil(time.Until(status.ExpiresAt))), q.theme.BodyMuted)
	}
	switch {
	case status.Connected && status.Guests > 0:
		add(q.theme.tr("a phone is connected"), q.theme.Success)
	case status.Connected:
		add(q.theme.tr("waiting for the phone to scan"), q.theme.Warning)
	case status.LastError != "":
		add(status.LastError, q.theme.Error)
	default:
		add(q.theme.tr("connecting to the relay…"), q.theme.Warning)
	}
	add(q.theme.tr("scan with the phone camera; the secret stays in the code"), q.theme.Timestamp)
}

func (q *qrcodeOverlay) Draw(buf *ui.Buffer) {
	fillBlock(buf, q.GetRect(), ui.Style{Fg: q.theme.Fg, Bg: q.theme.PanelHigh})
	q.Title = " " + q.theme.tr("REMOTE PAIRING") + " "
	q.Block.Draw(buf)
	height := max(0, q.Inner.Dy()-1)
	// A tall code is centred vertically so the block still reads as one panel.
	top := 0
	if len(q.lines) < height {
		top = (height - len(q.lines)) / 2
	}
	for i := 0; i < len(q.lines) && top+i < height; i++ {
		line := q.lines[i]
		x := q.Inner.Min.X + max(0, (q.Inner.Dx()-lineWidth(line))/2)
		for _, run := range line.runs {
			buf.SetString(run.text, run.style, image.Pt(x, q.Inner.Min.Y+top+i))
			x += rw.StringWidth(run.text)
		}
	}
	buf.SetString(fitText(q.theme.tr("Esc close"), q.Inner.Dx()), q.theme.Timestamp, image.Pt(q.Inner.Min.X, q.Inner.Max.Y-1))
}

func lineWidth(line renderLine) int {
	width := 0
	for _, run := range line.runs {
		width += rw.StringWidth(run.text)
	}
	return width
}

// qrLines renders a QR code with half blocks: two module rows per text row, one
// character per module column, which keeps the code square in a terminal and
// scannable without an image protocol.
func qrLines(link string) ([]renderLine, error) {
	if link == "" {
		return nil, fmt.Errorf("no pairing link")
	}
	code, err := qrcode.New(link, qrcode.Medium)
	if err != nil {
		return nil, err
	}
	bitmap := code.Bitmap()
	if len(bitmap) == 0 {
		return nil, fmt.Errorf("empty pairing code")
	}
	if len(bitmap)%2 == 1 {
		bitmap = append(bitmap, make([]bool, len(bitmap[0])))
	}
	lines := make([]renderLine, 0, len(bitmap)/2)
	for y := 0; y < len(bitmap); y += 2 {
		var b strings.Builder
		for x := range bitmap[y] {
			top := bitmap[y][x]
			bottom := bitmap[y+1][x]
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		lines = append(lines, makeLine(b.String(), ui.Style{Fg: ui.ColorWhite, Bg: ui.ColorBlack}, 0))
	}
	return lines, nil
}

// humanUntil renders a duration the way a phone user reads it: hours and
// minutes, never a raw Go duration.
func humanUntil(d time.Duration) string {
	if d <= 0 {
		return "0m"
	}
	d = d.Truncate(time.Minute)
	if d < time.Minute {
		return "1m"
	}
	hours := int(d / time.Hour)
	minutes := int((d % time.Hour) / time.Minute)
	if hours == 0 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%dh%02dm", hours, minutes)
}

// remoteStatusText is the one-line answer to `/remote`: it names the relay and
// the room, and says whether a phone is attached.
func remoteStatusText(t theme, status RemoteStatus) string {
	if !status.Enabled {
		return t.tr("remote: off (no pairing on this machine)")
	}
	state := t.tr("waiting for the phone")
	if status.Guests > 0 {
		state = fmt.Sprintf(t.tr("%d phone(s) connected"), status.Guests)
	} else if status.LastError != "" {
		state = status.LastError
	}
	return fmt.Sprintf("%s %s · %s %s · %s %s · %s",
		t.tr("remote:"), status.Server, t.tr("room"), status.Room,
		t.tr("expires"), humanUntil(time.Until(status.ExpiresAt)), state)
}

// SetRemote installs the remote control this frontend renders. Without it,
// /qrcode and /remote say that remote access is unavailable here rather than
// pretending to pair.
func (a *App) SetRemote(r Remote) { a.remote = r }

func (a *App) remoteStatus() RemoteStatus {
	if a.remote == nil {
		return RemoteStatus{}
	}
	return a.remote.Status()
}

// openQrcode asks the session for a pairing and shows the code. The request can
// touch the network, so it runs off the event loop and answers through
// asyncCh: the interface never blocks on a relay.
func (a *App) openQrcode(rotate bool) {
	if a.remote == nil {
		a.setStatus(a.theme.tr("remote access is not available here"), true)
		return
	}
	a.setStatus(a.theme.tr("preparing the pairing…"), false)
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		pairing, err := a.remote.Start(ctx, rotate)
		a.asyncCh <- asyncResult{kind: "remote_start", payload: pairing, err: err}
	}()
}

func (a *App) remoteOff() {
	if a.remote == nil {
		a.setStatus(a.theme.tr("remote access is not available here"), true)
		return
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		_, err := a.remote.Stop(ctx)
		a.asyncCh <- asyncResult{kind: "remote_stop", err: err}
	}()
}

func (a *App) showRemoteStatus() {
	if a.remote == nil {
		a.setStatus(a.theme.tr("remote access is not available here"), true)
		return
	}
	a.addLabeled(protocol.Duo, directionLabel(protocol.Duo, ""), remoteStatusText(a.theme, a.remote.Status()), false, false)
	a.setStatus(a.theme.tr("remote status shown in the timeline"), false)
}
