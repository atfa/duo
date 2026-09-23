package agent

import (
	"context"
	"testing"

	"github.com/atfa/duo/internal/protocol"
)

func TestExitStatusMapsToProcessState(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want ProcessState
	}{
		{"zero exit is a normal exit", "sh -c 'exit 0'", ProcessExited},
		{"non-zero exit is a failure", "sh -c 'exit 1'", ProcessFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSession(Config{Agent: protocol.Austin, Dir: t.TempDir(), Command: tt.cmd})
			if err := s.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			waitFor(t, func() bool {
				state := s.State()
				return state != ProcessStarting && state != ProcessRunning && state != ProcessStopping
			})
			if got := s.State(); got != tt.want {
				t.Fatalf("state = %v, want %v (waitErr=%v)", got, tt.want, s.WaitError())
			}
		})
	}
}

func TestStopReportsNormalExit(t *testing.T) {
	s := NewSession(Config{Agent: protocol.Tony, Dir: t.TempDir(), Command: "sleep 30"})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s.Running)
	s.Stop()
	if got := s.State(); got != ProcessExited {
		t.Fatalf("state after Duo-initiated Stop = %v, want %v", got, ProcessExited)
	}
}
