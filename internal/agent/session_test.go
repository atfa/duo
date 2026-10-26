package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/atfa/duo/internal/agent/agenttest"
	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/protocol"
)

type blockingWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type signalWriter struct {
	started chan struct{}
	once    sync.Once
}

func (w *signalWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	return len(p), nil
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release
	return len(p), nil
}

// TestSessionRunsInsidePTY is the guarantee the driver refactor must not break: an
// agent still runs in a real pseudo-terminal, and the command line still comes from
// the driver rather than from Core.
func TestSessionRunsInsidePTY(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stub := agenttest.New("stub").With(func(s *agenttest.Spec) {
		s.Command = `printf 'duo-pty-ok:%s:%s:%s\n' "$DUO_ACTIVE" "$DUO_SESSION" "$DUO_TOKEN"`
	})
	s, err := NewSession(ctx, Config{
		Agent: protocol.Austin, Dir: t.TempDir(), Host: "127.0.0.1", Port: "1",
		Session: "session-1", Token: "secret",
		Plugin: stub.Caller(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if s.State() == ProcessExited || s.State() == ProcessFailed {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the agent process never finished; state=%s", s.State())
}

func TestBlockedAttachedOutputDoesNotBlockAgentInput(t *testing.T) {
	stub := agenttest.New("stub").With(func(spec *agenttest.Spec) {
		spec.Command = "sh -c \"printf ready; exec sleep 60\""
	})
	s, err := NewSession(context.Background(), Config{Agent: protocol.Austin, Dir: t.TempDir(), Plugin: stub.Caller()})
	if err != nil {
		t.Fatal(err)
	}
	writer := &blockingWriter{started: make(chan struct{}), release: make(chan struct{})}
	s.Attach(writer)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	defer s.Stop()
	defer close(writer.release)

	select {
	case <-writer.started:
	case <-time.After(2 * time.Second):
		t.Fatal("agent output did not reach the attached writer")
	}

	written := make(chan error, 1)
	go func() { written <- s.Write([]byte("notice\r")) }()
	select {
	case err := <-written:
		if err != nil {
			t.Fatalf("write to the agent PTY: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("a blocked attached terminal writer prevented input from reaching the agent")
	}
}

func TestBlockedAgentInputDoesNotPreventStop(t *testing.T) {
	stub := agenttest.New("stub").With(func(spec *agenttest.Spec) {
		spec.Command = "sh -c \"stty raw -echo; printf ready; exec sleep 60\""
	})
	s, err := NewSession(context.Background(), Config{Agent: protocol.Austin, Dir: t.TempDir(), Plugin: stub.Caller()})
	if err != nil {
		t.Fatal(err)
	}
	output := &signalWriter{started: make(chan struct{})}
	s.Attach(output)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	defer func() {
		if s.proc != nil {
			s.proc.kill()
		}
	}()
	select {
	case <-output.started:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not enter the PTY test command")
	}
	if err := s.Resize(100, 40); err != nil {
		t.Fatalf("resize PTY before backpressure: %v", err)
	}

	written := make(chan error, 1)
	go func() { written <- s.Write(make([]byte, 1<<20)) }()
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-written:
		t.Fatalf("large write unexpectedly completed while the agent was not reading: %v", err)
	default:
	}

	stopped := make(chan struct{})
	go func() { s.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		// Break a regression deadlock so the test process can exit cleanly.
		if s.proc != nil {
			s.proc.kill()
		}
		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
		}
		t.Fatal("Stop blocked behind an agent PTY write")
	}
	select {
	case err := <-written:
		if err == nil {
			t.Fatal("closing a backpressured PTY write returned no error")
		}
	case <-time.After(time.Second):
		t.Fatal("stopping the agent did not release the blocked PTY write")
	}
}

func TestRestartRunningCarriesForwardLiveDriverState(t *testing.T) {
	stub := agenttest.New("stub").With(func(spec *agenttest.Spec) {
		spec.Command = "sh -c 'sleep 60'"
		spec.Capabilities.Resume = driver.ResumeServer
		spec.State = []byte(`{"sessionId":"prepared"}`)
		spec.LiveState = []byte(`{"sessionId":"learned"}`)
	})
	s, err := NewSession(context.Background(), Config{Agent: protocol.Austin, Dir: t.TempDir(), Plugin: stub.Caller(), ResumeSession: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	defer s.Stop()
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.RestartRunning(context.Background()); err != nil {
		t.Fatal(err)
	}
	prepares := stub.Prepares()
	if len(prepares) != 2 || string(prepares[1].State) != `{"sessionId":"learned"}` {
		t.Fatalf("restart prepare state = %#v, want live session identity", prepares)
	}
	if !prepares[0].ResumeSession || prepares[1].ResumeSession {
		t.Fatalf("resume session flags = [%t, %t], want true only on initial launch", prepares[0].ResumeSession, prepares[1].ResumeSession)
	}
}

func TestWithDriverUsesRequestedResumeSession(t *testing.T) {
	stub := agenttest.New("stub")
	previous, err := NewSession(context.Background(), Config{Agent: protocol.Austin, Dir: t.TempDir(), Plugin: stub.Caller(), ResumeSession: true})
	if err != nil {
		t.Fatal(err)
	}
	defer previous.Close()
	replacement, err := previous.WithDriver(context.Background(), stub.Caller(), "stub", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if err := replacement.Preflight(context.Background()); err != nil {
		t.Fatal(err)
	}
	prepares := stub.Prepares()
	if len(prepares) != 1 || prepares[0].ResumeSession {
		t.Fatalf("replacement prepare = %#v, want ResumeSession=false", prepares)
	}
}
