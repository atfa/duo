package gotui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/project"
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
	for i, line := range lines {
		text := line.runs[0].text
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
	if strings.TrimSpace(lines[0].runs[0].text) != "" {
		t.Error("the first row is not the quiet zone")
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
