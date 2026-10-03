package agent

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"github.com/atfa/duo/internal/agent/agenttest"
	"github.com/atfa/duo/internal/driver"
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
		// Core only asks a driver whose agent assigns its own identity.
		sp.Capabilities.Resume = driver.ResumeServer
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

// TestDriverStateIgnoresABlankAnswerOverTheWire is the regression test for the
// other half of the fork: a plugin that has nothing to say must not cost the user
// the identity they already had.
//
// It runs a real plugin process on purpose. In process, a Go nil is len 0 and falls
// back by accident; over a pipe it encodes to {"state":null}, decodes to the four
// bytes "null", and a length check reads that as a real answer. Core starts a
// replacement process after a crash, so this is a state the save path meets.
func TestDriverStateIgnoresABlankAnswerOverTheWire(t *testing.T) {
	for _, shape := range []string{"empty-object", "null"} {
		t.Run(shape, func(t *testing.T) {
			bin := t.TempDir() + "/blankplugin"
			build := exec.Command("go", "build", "-o", bin, "./testdata/blankplugin")
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build fixture: %v\n%s", err, out)
			}
			plugin := driver.NewSupervised("blank", driver.SupervisedOptions{
				Path: bin,
				Env:  []string{"DUO_BLANK_STATE=" + shape},
			})
			s, err := NewSession(context.Background(), Config{
				Agent:  protocol.Austin,
				Dir:    t.TempDir(),
				Plugin: plugin,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.Start(context.Background()); err != nil {
				t.Fatal(err)
			}

			got := string(s.DriverState())
			if got != `{"sessionId":"good-id-from-prepare"}` {
				t.Fatalf("a blank answer (%s) replaced a good blob: %s", shape, got)
			}
		})
	}
}

// TestBlankStateNamesTheFourShapes documents what Core treats as carrying nothing.
// A Go nil is deliberately absent: it never survives the wire.
func TestBlankStateNamesTheFourShapes(t *testing.T) {
	for _, blank := range []string{``, `null`, `""`, `{}`, `[]`, "  {}  "} {
		if !blankState([]byte(blank)) {
			t.Errorf("%q should count as blank", blank)
		}
	}
	for _, real := range []string{`{"sessionId":"x"}`, `0`, `false`} {
		if blankState([]byte(real)) {
			t.Errorf("%q must not count as blank", real)
		}
	}
}
