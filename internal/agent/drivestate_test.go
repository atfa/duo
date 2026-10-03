package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/atfa/duo/internal/agent/agenttest"
	"github.com/atfa/duo/internal/protocol"
)

// started builds a launched session whose plugin reports prepared state.
func started(t *testing.T, apply func(*agenttest.Spec)) (*Session, *agenttest.Driver) {
	t.Helper()
	stub := agenttest.New("stub").With(func(s *agenttest.Spec) {
		s.Command = "sh -c 'exit 0'"
		s.State = []byte(`{"sessionId":"prepared"}`)
		apply(s)
	})
	s, err := NewSession(context.Background(), Config{
		Agent:       protocol.Austin,
		Dir:         t.TempDir(),
		Plugin:      stub.Caller(),
		PluginState: []byte(`{"sessionId":"stored"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, stub
}

// TestDriverStateReadsThePluginLive is the whole point of the state method: the
// identity is learned after prepare ran, so the blob prepare returned is stale by
// the time Core saves.
func TestDriverStateReadsThePluginLive(t *testing.T) {
	s, _ := started(t, func(sp *agenttest.Spec) {
		sp.LiveState = []byte(`{"sessionId":"learned-after-launch"}`)
	})
	got := string(s.DriverState())
	if got != `{"sessionId":"learned-after-launch"}` {
		t.Fatalf("DriverState returned the stale plan state, not the live one: %s", got)
	}
}

// TestDriverStateFallsBackWhenUnsupported covers a driver with nothing live to
// report. Falling back is what lets a plugin skip the method entirely.
func TestDriverStateFallsBackWhenUnsupported(t *testing.T) {
	s, _ := started(t, func(*agenttest.Spec) {})
	if got := string(s.DriverState()); got != `{"sessionId":"prepared"}` {
		t.Fatalf("expected the prepared blob, got %s", got)
	}
}

// TestDriverStateNeverReplacesAGoodBlobWithGarbage is the safety property: a
// plugin that crashed or was killed mid-call must not cost the user the identity
// they already had. A blob one launch stale resumes; no blob cannot.
func TestDriverStateNeverReplacesAGoodBlobWithGarbage(t *testing.T) {
	for name, spec := range map[string]func(*agenttest.Spec){
		"the call errors": func(sp *agenttest.Spec) { sp.LiveStateErr = errors.New("plugin crashed") },
		"the call returns nothing": func(sp *agenttest.Spec) {
			sp.ImplementsState = true
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := started(t, spec)
			if got := string(s.DriverState()); got != `{"sessionId":"prepared"}` {
				t.Fatalf("a failed read overwrote a good blob: %s", got)
			}
		})
	}
}

// TestDriverStateBeforeAnyLaunchUsesStoredState keeps a pre-launch save from
// inventing an identity the plugin never chose.
func TestDriverStateBeforeAnyLaunchUsesStoredState(t *testing.T) {
	stub := agenttest.New("stub").With(func(*agenttest.Spec) {})
	s, err := NewSession(context.Background(), Config{
		Agent:       protocol.Austin,
		Dir:         t.TempDir(),
		Plugin:      stub.Caller(),
		PluginState: []byte(`{"sessionId":"stored"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := string(s.DriverState()); got != `{"sessionId":"stored"}` {
		t.Fatalf("expected the stored blob before launch, got %s", got)
	}
}
