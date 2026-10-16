package guiipc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/models"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
)

// fakeSession is the seam with nothing live behind it: a view, a real event bus,
// and the commands the host dispatched. It exists because the host depends on
// the Session interface rather than *session.Service, which is the same property
// that makes a second frontend possible.
type fakeSession struct {
	mu      sync.Mutex
	view    session.View
	status  string
	history []sessionstore.TUIEntry
	bus     *events.Bus

	cancels     []func()
	got         []session.Command
	doErr       error
	modelResult []models.Model
	modelErr    error
}

func newFakeSession() *fakeSession {
	return &fakeSession{view: testView(), status: "status line", bus: events.NewBus()}
}

func (f *fakeSession) View() session.View {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view
}

func (f *fakeSession) Subscribe(buffer int) (session.Subscription, func()) {
	ch, cancel := f.bus.Subscribe(buffer)
	f.mu.Lock()
	f.cancels = append(f.cancels, cancel)
	f.mu.Unlock()
	return session.Subscription{View: f.View(), Events: ch}, cancel
}

func (f *fakeSession) Do(_ context.Context, cmd session.Command) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, cmd)
	return f.doErr
}

func (f *fakeSession) StatusText(context.Context) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *fakeSession) History() []sessionstore.TUIEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sessionstore.TUIEntry(nil), f.history...)
}

func (f *fakeSession) Models(context.Context, protocol.AgentID) ([]models.Model, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]models.Model(nil), f.modelResult...), f.modelErr
}

func (f *fakeSession) emit(e events.Event) { f.bus.Emit(e) }

// closeEvents closes the stream the host subscribed to, which is what a session
// closing its bus looks like from the host's side.
func (f *fakeSession) closeEvents() {
	f.mu.Lock()
	cancels := append([]func(){}, f.cancels...)
	f.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (f *fakeSession) commands() []session.Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]session.Command(nil), f.got...)
}

func (f *fakeSession) failDo(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.doErr = err
}

// frameRecorder decodes the host's display stream in the background so a test can
// assert on frames without racing the writer.
type frameRecorder struct {
	mu     sync.Mutex
	frames []writeFrame
	dec    *json.Decoder
}

func newFrameRecorder(r io.Reader) *frameRecorder {
	rec := &frameRecorder{dec: json.NewDecoder(r)}
	go rec.collect()
	return rec
}

func (r *frameRecorder) collect() {
	for {
		var f writeFrame
		if err := r.dec.Decode(&f); err != nil {
			return
		}
		r.mu.Lock()
		r.frames = append(r.frames, f)
		r.mu.Unlock()
	}
}

func (r *frameRecorder) snapshot() []writeFrame {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]writeFrame(nil), r.frames...)
}

// hostHarness runs one host over pipes, the way the CLI runs it over a child
// process, and cleans up every end of every pipe.
type hostHarness struct {
	t      *testing.T
	fake   *fakeSession
	host   *Host
	inW    *io.PipeWriter
	outW   *io.PipeWriter
	rec    *frameRecorder
	done   chan error
	cancel context.CancelFunc
}

func startHost(t *testing.T, fake *fakeSession, tune ...func(*Host)) *hostHarness {
	t.Helper()

	cmdR, cmdW := io.Pipe()
	outR, outW := io.Pipe()
	host := NewHost(fake)
	for _, fn := range tune {
		fn(host)
	}

	ctx, cancel := context.WithCancel(context.Background())
	h := &hostHarness{
		t:      t,
		fake:   fake,
		host:   host,
		inW:    cmdW,
		outW:   outW,
		rec:    newFrameRecorder(outR),
		done:   make(chan error, 1),
		cancel: cancel,
	}
	go func() { h.done <- host.Serve(ctx, cmdR, outW) }()
	t.Cleanup(func() {
		cancel()
		_ = cmdW.Close()
		_ = outW.Close()
	})
	return h
}

// quiet pins the heartbeat far out of the way for tests that assert exact frame
// sequences, so a ticker cannot insert a frame between two assertions.
func quiet(h *Host) { h.heartbeat = time.Hour }

func (h *hostHarness) send(line string) {
	h.t.Helper()
	if _, err := io.WriteString(h.inW, line+"\n"); err != nil {
		h.t.Fatalf("send command: %v", err)
	}
}

func (h *hostHarness) wait() error {
	h.t.Helper()
	select {
	case err := <-h.done:
		return err
	case <-time.After(5 * time.Second):
		h.t.Fatal("Serve did not return")
		return nil
	}
}

func (h *hostHarness) waitFrames(n int) []writeFrame {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		frames := h.rec.snapshot()
		if len(frames) >= n {
			return frames
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %d frames; got %d: %+v", n, len(frames), frames)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (h *hostHarness) waitCommands(n int) []session.Command {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		cmds := h.fake.commands()
		if len(cmds) >= n {
			return cmds
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %d commands; got %d: %+v", n, len(cmds), cmds)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestServeSendsViewThenHistoryThenEvents(t *testing.T) {
	fake := newFakeSession()
	fake.history = []sessionstore.TUIEntry{{
		Time:  eventTime,
		Pane:  "Austin",
		Text:  "earlier line",
		Label: "Austin → Tony",
	}}
	h := startHost(t, fake, quiet)

	frames := h.waitFrames(2)
	if frames[0].Type != typeView || frames[0].View == nil {
		t.Fatalf("first frame = %+v; want a view", frames[0])
	}
	if frames[0].View.Session != fake.view.SessionID || frames[0].View.Status != "status line" {
		t.Errorf("view session/status = %q/%q", frames[0].View.Session, frames[0].View.Status)
	}
	if len(frames[0].View.Notices) != 1 {
		t.Errorf("view notices = %v; driver diagnostics must reach the window", frames[0].View.Notices)
	}
	if frames[1].Type != typeEvent || frames[1].Event == nil {
		t.Fatalf("second frame = %+v; want a history event", frames[1])
	}
	if frames[1].Event.Text != "earlier line" || frames[1].Event.Agent != "austin" || frames[1].Event.Kind != "assistant" {
		t.Errorf("history event = %+v", frames[1].Event)
	}
	if frames[1].Event.Label != "Austin → Tony" {
		t.Errorf("history label = %q; replay must keep the direction header", frames[1].Event.Label)
	}

	fake.emit(events.Event{Time: eventTime, Kind: events.KindPeer, Agent: protocol.Tony, Peer: protocol.Austin, Text: "live"})
	frames = h.waitFrames(4)
	if frames[2].Type != typeEvent || frames[2].Event.Text != "live" || frames[2].Event.Kind != "peer" {
		t.Errorf("live event frame = %+v", frames[2])
	}
	if frames[3].Type != typeView {
		t.Errorf("frame after an event = %+v; want a refreshed view", frames[3])
	}

	h.send(`{"type":"command","kind":"quit"}`)
	if err := h.wait(); err != nil {
		t.Errorf("Serve returned %v after quit; want nil", err)
	}
}

func TestServeLoadsDriverCatalogAndPublishesItAsynchronously(t *testing.T) {
	fake := newFakeSession()
	fake.modelResult = []models.Model{{Provider: "provider", ID: "model-id", Thinking: true}}
	h := startHost(t, fake, func(host *Host) { host.heartbeat = 5 * time.Millisecond })
	frames := h.waitFrames(2)
	deadline := time.Now().Add(5 * time.Second)
	for {
		frames = h.rec.snapshot()
		found := false
		for _, frame := range frames {
			if frame.View != nil {
				a := frame.View.Agents["austin"]
				if a.ModelsLoaded && len(a.Models) == 1 {
					found = true
					if a.Models[0].Reference != "provider/model-id" || !a.Models[0].Thinking {
						t.Errorf("catalog option = %+v", a.Models[0])
					}
				}
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("catalog was not published; frames=%+v", frames)
		}
		time.Sleep(time.Millisecond)
	}
	if frames[0].View == nil || frames[0].Type != typeView {
		t.Fatalf("initial frame = %+v", frames[0])
	}
	h.send(`{"type":"command","kind":"quit"}`)
	if err := h.wait(); err != nil {
		t.Fatalf("Serve: %v", err)
	}
}

func TestServeDispatchesCommandsAndReportsFailures(t *testing.T) {
	fake := newFakeSession()
	h := startHost(t, fake, quiet)
	h.waitFrames(1)

	h.send(`{"type":"command","kind":"submit_task","text":"ship it"}`)
	h.send(`{"type":"command","kind":"restart_agent","agent":"tony"}`)
	got := h.waitCommands(2)
	if want := (session.SubmitTask{Text: "ship it"}); got[0] != want {
		t.Errorf("command 0 = %#v; want %#v", got[0], want)
	}
	if want := (session.RestartAgent{Agent: protocol.Tony}); got[1] != want {
		t.Errorf("command 1 = %#v; want %#v", got[1], want)
	}

	// A frame the protocol does not know is reported, not fatal: the window
	// stays open so the human can see why their keystroke did nothing.
	h.send(`{"type":"command","kind":"unknown_kind"}`)
	frames := h.waitFrames(2)
	if frames[1].Type != typeError || !strings.Contains(frames[1].Text, "unknown command kind") {
		t.Errorf("bad frame produced %+v", frames[1])
	}

	// A session that rejects a known verb is reported the same way.
	fake.failDo(errors.New("session is closed"))
	h.send(`{"type":"command","kind":"escalate_to_goal"}`)
	frames = h.waitFrames(3)
	if frames[2].Type != typeError || !strings.Contains(frames[2].Text, "session is closed") {
		t.Errorf("rejected command produced %+v", frames[2])
	}
	if cmds := h.waitCommands(3); len(cmds) != 3 {
		t.Errorf("dispatched %d commands; want 3", len(cmds))
	}
}

func TestServeReturnsWhenSessionClosesItsEvents(t *testing.T) {
	fake := newFakeSession()
	h := startHost(t, fake, quiet)
	h.waitFrames(1)

	fake.closeEvents()
	frames := h.waitFrames(2)
	if frames[1].Type != typeClosed {
		t.Fatalf("frame after the bus closed = %+v; want closed", frames[1])
	}
	if err := h.wait(); err != nil {
		t.Errorf("Serve returned %v after the session ended; want nil", err)
	}
}

func TestServeReturnsWhenTheCommandStreamEnds(t *testing.T) {
	fake := newFakeSession()
	h := startHost(t, fake, quiet)
	h.waitFrames(1)

	if err := h.inW.Close(); err != nil {
		t.Fatalf("close command stream: %v", err)
	}
	if err := h.wait(); err != nil {
		t.Errorf("Serve returned %v after the frontend exited; want nil", err)
	}
}

// TestServeReturnsWhenTheContextIsCancelled covers the one exit that is neither
// the frontend's nor the session's: the process running the host is going away.
func TestServeReturnsWhenTheContextIsCancelled(t *testing.T) {
	fake := newFakeSession()
	h := startHost(t, fake, quiet)
	h.waitFrames(1)

	h.cancel()
	if err := h.wait(); !errors.Is(err, context.Canceled) {
		t.Errorf("Serve returned %v after cancellation; want context.Canceled", err)
	}
}

// TestServeSendsNothingAfterTheSessionEnds pins that the closed frame really is
// the end of the stream. The heartbeat is still running when the bus closes, so
// without the terminal flag a view can land after it and contradict the frame the
// window just read.
func TestServeSendsNothingAfterTheSessionEnds(t *testing.T) {
	fake := newFakeSession()
	h := startHost(t, fake, func(host *Host) { host.heartbeat = time.Millisecond })
	h.waitFrames(1)

	fake.closeEvents()
	if err := h.wait(); err != nil {
		t.Errorf("Serve returned %v after the session ended; want nil", err)
	}
	// Let any heartbeat already in flight land before deciding.
	time.Sleep(20 * time.Millisecond)

	frames := h.rec.snapshot()
	closed := -1
	for i, frame := range frames {
		if frame.Type == typeClosed {
			closed = i
		}
	}
	if closed < 0 {
		t.Fatalf("no closed frame: %+v", frames)
	}
	if closed != len(frames)-1 {
		t.Errorf("frames followed the closed frame: %+v", frames[closed+1:])
	}
}

func TestServeHeartbeatsTheView(t *testing.T) {
	fake := newFakeSession()
	h := startHost(t, fake, func(host *Host) { host.heartbeat = 10 * time.Millisecond })

	frames := h.waitFrames(3)
	for i, f := range frames {
		if f.Type != typeView || f.View == nil {
			t.Fatalf("frame %d = %+v; heartbeat must send views only", i, f)
		}
	}
}

func TestServeReportsAWriteFailure(t *testing.T) {
	host := NewHost(newFakeSession())
	host.heartbeat = time.Hour

	err := host.Serve(context.Background(), strings.NewReader(""), failingWriter{})
	if !errors.Is(err, errWriteRefused) {
		t.Fatalf("Serve returned %v; want the write failure", err)
	}
}

var errWriteRefused = errors.New("write refused")

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errWriteRefused }
