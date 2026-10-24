package gotui

import (
	"context"
	"image"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/project"
	"github.com/gdamore/tcell/v3"
	"github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui/v5"
	qrcode "github.com/skip2/go-qrcode"
)

// remoteTestSnapshot is a started session, so /qrcode and /remote are not
// rejected by the workflow.
func remoteTestSnapshot() project.Snapshot {
	return project.Snapshot{Mode: project.ModeStep, Phase: project.PhaseExecute, Started: true}
}

type fakeRemote struct {
	status  RemoteStatus
	started int
	stopped int
	rotate  bool
}

func (f *fakeRemote) Start(ctx context.Context, rotate bool) (RemotePairing, error) {
	f.started++
	f.rotate = rotate
	f.status.Enabled = true
	f.status.Room = "room-1"
	f.status.Server = "https://relay.example"
	f.status.ExpiresAt = time.Now().Add(time.Hour)
	return RemotePairing{
		URL:     "https://relay.example/p/room-1#secret",
		Display: "https://relay.example/p/room-1",
		Status:  f.status,
	}, nil
}

func (f *fakeRemote) Stop(ctx context.Context) (RemoteStatus, error) {
	f.stopped++
	f.status = RemoteStatus{}
	return f.status, nil
}

func (f *fakeRemote) Status() RemoteStatus { return f.status }

// The code has to be square, complete and made only of block characters: a QR
// renderer that drops a row or mixes in text is not scannable, and the failure
// would only show up on a phone.
func TestQRCodeLinesAreSquareAndComplete(t *testing.T) {
	lines, err := qrLines("https://remote.xduo.dev/p/QEFCQ0RFRkdISUpLTE1OTw#AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8")
	if err != nil {
		t.Fatalf("qrLines: %v", err)
	}
	if len(lines) < 10 {
		t.Fatalf("only %d rows; a pairing code is taller", len(lines))
	}
	width := -1
	for i, text := range lines {
		runes := []rune(text)
		if width == -1 {
			width = len(runes)
			if width < 21 || width%2 == 0 {
				t.Fatalf("width %d is not an odd QR size with a quiet zone", width)
			}
		}
		if len(runes) != width {
			t.Fatalf("row %d is %d wide, want %d", i, len(runes), width)
		}
		for _, r := range runes {
			if !strings.ContainsRune("█▀▄ ", r) {
				t.Fatalf("row %d contains %q, which is not a QR block", i, r)
			}
		}
	}
	// The quiet zone is part of the bitmap, so the border rows are blank.
	if strings.TrimSpace(lines[0]) != "" {
		t.Error("the first row is not the quiet zone")
	}
}

// A camera reads black modules on a light background. The overlay once painted
// the modules white on the black panel: an inverted code, which phone scanners
// reject, so a scan failed and the only link left on screen was the one without
// the secret, which can never pair.
func TestQRCodeIsDarkOnLight(t *testing.T) {
	if qrStyle.Fg != ui.ColorBlack || qrStyle.Bg != ui.ColorWhite {
		t.Fatalf("code style is fg=%v bg=%v; a scanner needs dark modules on a light cell", qrStyle.Fg, qrStyle.Bg)
	}
	const link = "https://remote.xduo.dev/p/QEFCQ0RFRkdISUpLTE1OTw#AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	lines, err := qrLines(link)
	if err != nil {
		t.Fatalf("qrLines: %v", err)
	}
	// The drawn blocks must be exactly the symbol's dark modules, or the code is
	// missing rows or has them flipped.
	code, err := qrcode.New(link, qrcode.Medium)
	if err != nil {
		t.Fatalf("qrcode.New: %v", err)
	}
	bitmap := code.Bitmap()
	// An odd module count is padded with one light row so every text row holds
	// two modules; that row is quiet zone, not symbol.
	if len(bitmap)%2 == 1 {
		bitmap = append(bitmap, make([]bool, len(bitmap[0])))
	}
	if len(lines)*2 != len(bitmap) {
		t.Fatalf("drew %d module rows, want %d", len(lines)*2, len(bitmap))
	}
	for y, text := range lines {
		for x, r := range []rune(text) {
			top, bottom := false, false
			switch r {
			case '█':
				top, bottom = true, true
			case '▀':
				top = true
			case '▄':
				bottom = true
			}
			if top != bitmap[y*2][x] || bottom != bitmap[y*2+1][x] {
				t.Fatalf("module (%d,%d) is drawn top=%v bottom=%v, want top=%v bottom=%v", x, y*2, top, bottom, bitmap[y*2][x], bitmap[y*2+1][x])
			}
		}
	}
}

// A CJK locale makes go-runewidth measure the block glyphs as two cells wide
// while the terminal draws them in one. Writing the code as a string then leaves
// a panel-coloured gap in every second module column, which no scanner can read,
// so the overlay places one cell per module instead.
func TestQRCodeStaysContiguousUnderAWideLocale(t *testing.T) {
	previous := runewidth.DefaultCondition.EastAsianWidth
	runewidth.DefaultCondition.EastAsianWidth = true
	t.Cleanup(func() { runewidth.DefaultCondition.EastAsianWidth = previous })

	q := newQrcode(newTheme())
	q.rebuild(RemotePairing{URL: "https://relay.example/p/room-1#secret"}, RemoteStatus{Room: "room-1", Connected: true})
	if len(q.code) == 0 {
		t.Fatal("the overlay has no pairing code")
	}
	q.SetRect(0, 0, len([]rune(q.code[0]))+4, len(q.code)+len(q.lines)+4)
	buf := ui.NewBuffer(image.Rect(0, 0, q.Dx(), q.Dy()))
	q.Draw(buf)

	width := len([]rune(q.code[0]))
	rows := 0
	for y := 0; y < q.Dy(); y++ {
		first, last, count := -1, -1, 0
		for x := 0; x < q.Dx(); x++ {
			cell := buf.GetCell(image.Pt(x, y))
			if cell.Style.Fg != qrStyle.Fg || cell.Style.Bg != qrStyle.Bg {
				continue
			}
			if first < 0 {
				first = x
			}
			last = x
			count++
		}
		if first < 0 {
			continue
		}
		rows++
		if count != width || last-first+1 != width {
			t.Fatalf("code row %d has %d code cells spread over %d columns, want %d adjacent cells", y, count, last-first+1, width)
		}
	}
	if rows != len(q.code) {
		t.Fatalf("drew %d code rows, want %d", rows, len(q.code))
	}
}

func TestQRCodeLinesRefuseAnEmptyLink(t *testing.T) {
	if _, err := qrLines(""); err == nil {
		t.Fatal("an empty pairing link produced a code")
	}
}

func TestHumanUntil(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0m"},
		{-time.Minute, "0m"},
		{30 * time.Second, "1m"},
		{90 * time.Minute, "1h30m"},
		{8 * time.Hour, "8h00m"},
	}
	for _, tc := range cases {
		if got := humanUntil(tc.in); got != tc.want {
			t.Errorf("humanUntil(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestQrcodeWithoutRemoteReportsUnavailable(t *testing.T) {
	_, app := newModeSwitchApp(t, remoteTestSnapshot())
	app.runSlash("/qrcode")
	if !app.statusErr {
		t.Fatal("/qrcode without a remote control did not report an error")
	}
	if app.overlay == overlayQrcode {
		t.Fatal("/qrcode opened a pairing overlay without a remote control")
	}
}

// /qrcode must not block the event loop: it asks the session in the background
// and opens the overlay when the answer arrives.
func TestQrcodeOpensThePairingOverlay(t *testing.T) {
	_, app := newModeSwitchApp(t, remoteTestSnapshot())
	remote := &fakeRemote{}
	app.SetRemote(remote)

	app.runSlash("/qrcode")
	if app.overlay == overlayQrcode {
		t.Fatal("/qrcode opened the overlay before the pairing arrived")
	}
	app.handleAsync(<-app.asyncCh)
	if remote.started != 1 {
		t.Fatalf("Start called %d times, want 1", remote.started)
	}
	if app.overlay != overlayQrcode {
		t.Fatalf("overlay = %v, want the pairing overlay", app.overlay)
	}
	if !strings.Contains(app.qrPairing.URL, "#secret") {
		t.Errorf("pairing URL = %q, want the full link with the secret", app.qrPairing.URL)
	}
	if strings.Contains(app.qrPairing.Display, "#") {
		t.Errorf("display URL = %q, want no secret", app.qrPairing.Display)
	}

	app.runSlash("/remote off")
	app.handleAsync(<-app.asyncCh)
	if remote.stopped != 1 {
		t.Fatalf("Stop called %d times, want 1", remote.stopped)
	}
	if app.overlay == overlayQrcode {
		t.Error("/remote off left the pairing overlay open")
	}
}

// The overlay is the whole answer to /qrcode. A footer status would replace the
// idle key hint on the bottom row with a line the human does not need, and it
// never cleared, so the same text came back on every /qrcode.
func TestQrcodeOpensWithoutAFooterStatus(t *testing.T) {
	_, app := newModeSwitchApp(t, remoteTestSnapshot())
	app.SetRemote(&fakeRemote{})

	app.runSlash("/qrcode")
	app.handleAsync(<-app.asyncCh)
	if app.overlay != overlayQrcode {
		t.Fatalf("overlay = %v, want the pairing overlay", app.overlay)
	}
	if app.status != "" {
		t.Fatalf("status = %q, want the idle key hint instead", app.status)
	}

	app.runSlash("/qrcode new")
	app.handleAsync(<-app.asyncCh)
	if app.status != "" {
		t.Fatalf("status after a second /qrcode = %q, want the idle key hint instead", app.status)
	}
}

func TestQrcodeOverlayClosesOnEscape(t *testing.T) {
	_, app := newModeSwitchApp(t, remoteTestSnapshot())
	app.SetRemote(&fakeRemote{})
	app.runSlash("/qrcode")
	app.handleAsync(<-app.asyncCh)
	if app.overlay != overlayQrcode {
		t.Fatalf("overlay = %v, want the pairing overlay", app.overlay)
	}

	app.handleKey(tcell.NewEventKey(tcell.KeyEsc, "", tcell.ModNone))
	if app.overlay != overlayNone {
		t.Fatalf("Esc left overlay %v open", app.overlay)
	}
}

func TestQrcodeNewRotatesThePairing(t *testing.T) {
	_, app := newModeSwitchApp(t, remoteTestSnapshot())
	remote := &fakeRemote{}
	app.SetRemote(remote)
	app.runSlash("/qrcode new")
	app.handleAsync(<-app.asyncCh)
	if !remote.rotate {
		t.Error("/qrcode new did not ask for a fresh pairing")
	}
}

func TestRemoteStatusTextNamesTheRelayAndThePhone(t *testing.T) {
	app := New(nil)
	status := RemoteStatus{
		Enabled:   true,
		Server:    "https://relay.example",
		Room:      "room-1",
		ExpiresAt: time.Now().Add(90 * time.Minute),
		Guests:    1,
	}
	text := remoteStatusText(app.theme, status)
	for _, want := range []string{"https://relay.example", "room-1", "expires 1h2", "1 phone"} {
		if !strings.Contains(text, want) {
			t.Errorf("status %q does not mention %q", text, want)
		}
	}
	if got := remoteStatusText(app.theme, RemoteStatus{}); !strings.Contains(got, "off") {
		t.Errorf("unpaired status = %q, want it to say off", got)
	}
}

// Closing the panel is the obvious next move, and a human cannot tell from the
// screen whether that ends the phone's access. It does not: only /remote off (or
// the pairing expiring, or Duo exiting) disconnects. The panel has to say so, and
// this fails if the hint is dropped.
func TestQrcodeOverlaySaysClosingItKeepsThePhone(t *testing.T) {
	q := newQrcode(newTheme())
	q.rebuild(
		RemotePairing{URL: "https://relay.example/p/room-1#secret", Display: "https://relay.example/p/room-1"},
		RemoteStatus{Room: "room-1", Connected: true, Guests: 1, ExpiresAt: time.Now().Add(time.Hour)},
	)

	shown := overlayText(q.lines)
	for _, want := range []string{
		"closing this panel keeps the phone connected",
		"/remote off revokes the pairing",
	} {
		if !strings.Contains(shown, want) {
			t.Errorf("the pairing overlay does not say %q:\n%s", want, shown)
		}
	}

	// A hint wider than the panel is drawn past its border on a narrow terminal,
	// so no line may be wider than the widest line that was already there.
	limit := 57
	for _, line := range q.lines {
		if width := lineWidth(line); width > limit {
			t.Errorf("overlay line %q is %d cells wide, want at most %d", overlayLineText(line), width, limit)
		}
	}
}

// overlayText joins the overlay's lines so a test can read what a human sees.
func overlayText(lines []renderLine) string {
	texts := make([]string, 0, len(lines))
	for _, line := range lines {
		texts = append(texts, overlayLineText(line))
	}
	return strings.Join(texts, "\n")
}

func overlayLineText(line renderLine) string {
	var b strings.Builder
	for _, run := range line.runs {
		b.WriteString(run.text)
	}
	return b.String()
}
