package guiipc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
)

// Session is the seam the GUI host reads. It is exactly the surface the terminal
// interface uses: a projection, a stream, the human verbs, and the two reads a
// window needs on top. Naming it here rather than depending on *session.Service
// is what lets the host be driven by a fake in a test.
//
// A frontend that can call these methods still cannot start, stop, steer or sign
// a session beyond the human verbs in session.Command: there is no gate verb on
// this interface, and the compiler is what keeps it that way.
type Session interface {
	// View is the complete projection at one moment.
	View() session.View
	// Subscribe returns the view plus every event after it.
	Subscribe(buffer int) (session.Subscription, func())
	// Do performs one human verb.
	Do(ctx context.Context, cmd session.Command) error
	// StatusText renders the workflow status line.
	StatusText(ctx context.Context) string
	// History returns the interface entries recorded before a resume.
	History() []sessionstore.TUIEntry
}

const (
	// defaultHeartbeat is how often a full view is re-sent. Status and agent
	// activity change without an event of their own, so the view is the only way
	// the window can follow them.
	defaultHeartbeat = 500 * time.Millisecond
	// defaultBuffer matches the bus's own default. A slow renderer drops events
	// rather than blocking the session, and the next view repairs the display.
	defaultBuffer = 64
	// maxCommandBytes bounds one command line so a frontend that never sends a
	// newline cannot make the host buffer without limit.
	maxCommandBytes = 1 << 20
)

// Host serves the frozen NDJSON protocol over a Gpio pair, driving one session.
//
// It is frontend-neutral in the only sense that matters: it translates the same
// View and the same Commands the terminal interface uses, and it invents no
// workflow of its own. Everything it sends is derived from a session read, and
// everything it accepts is a session.Command.
type Host struct {
	s Session

	// heartbeat and buffer are the defaults above, overridden only by tests.
	heartbeat time.Duration
	buffer    int
}

// NewHost returns a host for one session.
func NewHost(s Session) *Host {
	return &Host{s: s, heartbeat: defaultHeartbeat, buffer: defaultBuffer}
}

// Serve speaks the protocol until the frontend goes away, the session closes its
// event stream, or ctx is cancelled.
//
// in and out are the GUI's command stream and the GUI's display stream, so a
// caller that has spawned a renderer subprocess passes the child's stdout as in
// and the child's stdin as out. Nothing else may be written to out: the GUI's
// decoder treats every line as a protocol frame.
func (h *Host) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sub, unsubscribe := h.s.Subscribe(h.buffer)
	defer unsubscribe()

	sink := &frameSink{w: out}

	// The view comes first so the window has headers before the backlog lands.
	if err := sink.view(fromView(sub.View, h.s.StatusText(ctx))); err != nil {
		return err
	}
	// Then the conversation that led here. The store keeps it per pane rather
	// than per event; fromHistory is the one place that translation happens.
	for _, entry := range h.s.History() {
		if err := sink.event(fromHistory(entry)); err != nil {
			return err
		}
	}

	done := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(done) }) }

	go h.readCommands(ctx, in, sink, stop)

	// Live events: each one, then a fresh view, so one dropped event can never
	// leave a stale field on screen.
	go func() {
		defer stop()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-sub.Events:
				if !ok {
					// The session closed its bus. Say so before Serve returns, so the
					// window can stop waiting for a view that will never come.
					_ = sink.closed("session ended")
					return
				}
				if err := sink.event(fromEvent(ev)); err != nil {
					return
				}
				if err := h.sendView(ctx, sink); err != nil {
					return
				}
			}
		}
	}()

	go func() {
		defer stop()
		ticker := time.NewTicker(h.heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := h.sendView(ctx, sink); err != nil {
					return
				}
			}
		}
	}()

	select {
	case <-done:
	case <-ctx.Done():
	}

	// A write failure is the only failure this host owns: a malformed command is
	// the frontend's bug and was already reported, and a closed session is a
	// normal end.
	if err := sink.err(); err != nil {
		return err
	}
	return ctx.Err()
}

// sendView projects and sends one view. It is the only place a view is built, so
// the initial frame, the post-event frame and the heartbeat cannot disagree.
func (h *Host) sendView(ctx context.Context, sink *frameSink) error {
	return sink.view(fromView(h.s.View(), h.s.StatusText(ctx)))
}

// readCommands is the only writer to the session. It runs until the command
// stream ends, which is what a frontend process exiting looks like: the session
// then has no one left to render it, so Serve returns.
func (h *Host) readCommands(ctx context.Context, in io.Reader, sink *frameSink, stop func()) {
	defer stop()

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), maxCommandBytes)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		cmd, quit, err := decodeCommand(line)
		if err != nil {
			// Report and keep going: one bad frame must not end the window.
			_ = sink.failure(err)
			continue
		}
		if quit {
			return
		}
		if err := h.s.Do(ctx, cmd); err != nil {
			_ = sink.failure(err)
		}
	}
}

// frameSink serializes the writers. The command reader never writes, but the
// event stream and the heartbeat do, and a frame must reach the GUI whole.
type frameSink struct {
	mu   sync.Mutex
	w    io.Writer
	fail error
	// terminal is set by the closed frame. It is what makes "closed" the end of
	// the stream rather than a race with the heartbeat.
	terminal bool
}

func (s *frameSink) view(v View) error { return s.write(writeFrame{Type: typeView, View: &v}) }

func (s *frameSink) event(e Event) error { return s.write(writeFrame{Type: typeEvent, Event: &e}) }

func (s *frameSink) failure(err error) error {
	return s.write(writeFrame{Type: typeError, Text: err.Error()})
}

func (s *frameSink) closed(text string) error {
	return s.writeFrame(writeFrame{Type: typeClosed, Text: text}, true)
}

// err reports the first write failure, which is what makes Serve return a real
// error instead of silently dropping frames.
func (s *frameSink) err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fail
}

// write encodes one frame and writes it as a single line.
//
// The frame is marshalled before the lock so two goroutines cannot interleave
// halves of a line, and the first failure is remembered rather than retried: a
// broken pipe stays broken, and every later write would just fail again.
func (s *frameSink) write(frame writeFrame) error {
	return s.writeFrame(frame, false)
}

// writeFrame writes one frame, and writes the stream's last frame when terminal
// is true. Setting the flag under the same lock that writes the line is what makes
// it the end of the stream: a heartbeat view already in flight when the session
// ends is dropped instead of arriving after the frame that said it was over.
func (s *frameSink) writeFrame(frame writeFrame, terminal bool) error {
	line, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal {
		return nil
	}
	if s.fail != nil {
		return s.fail
	}
	if _, err := s.w.Write(line); err != nil {
		s.fail = err
		return err
	}
	s.terminal = terminal
	return nil
}
