package coordinator

import (
	"context"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// A quiet mode switch must update the adapters without injecting a user turn.
// ResumeMode is the separate, explicit wake-up used after the human confirms.
func TestHumanTaskReleasesHarnessHold(t *testing.T) {
	ctx := context.Background()
	runtime := startE2EWithMode(t, ctx, project.ModeGoal)
	runtime.dialAgent(t, protocol.Austin)
	runtime.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool {
		return runtime.server.IsConnected(protocol.Austin) && runtime.server.IsConnected(protocol.Tony)
	}, "both agents to connect")

	if err := runtime.coord.SwitchModeQuiet(ctx, project.ModeStep, ""); err != nil {
		t.Fatal(err)
	}
	if !runtime.coord.HarnessHeld() {
		t.Fatal("quiet switch did not hold the harness")
	}
	if err := runtime.coord.SubmitUserTask(ctx, "continue with a human task"); err != nil {
		t.Fatal(err)
	}
	if runtime.coord.HarnessHeld() {
		t.Fatal("a human task must release the harness hold")
	}
}

func TestQuietModeSwitchDoesNotWakeAgents(t *testing.T) {
	for _, tc := range []struct {
		name string
		from project.Mode
		to   project.Mode
	}{
		{"fast-to-goal-escalation", project.ModeFast, project.ModeGoal},
		{"goal-to-step", project.ModeGoal, project.ModeStep},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			runtime := startE2EWithMode(t, ctx, tc.from)
			austin := runtime.dialAgent(t, protocol.Austin)
			tony := runtime.dialAgent(t, protocol.Tony)
			waitFor(t, func() bool {
				return runtime.server.IsConnected(protocol.Austin) && runtime.server.IsConnected(protocol.Tony)
			}, "both agents to connect")

			if err := runtime.coord.SwitchModeQuiet(ctx, tc.to, ""); err != nil {
				t.Fatalf("SwitchModeQuiet: %v", err)
			}
			for _, agent := range []*testAgent{austin, tony} {
				message := agent.awaitMessage(t, "quiet mode change", func(m protocol.Message) bool {
					return m.Type == protocol.MsgModeChange
				})
				if message.Mode != tc.to.String() {
					t.Fatalf("mode change target = %q, want %q", message.Mode, tc.to)
				}
				if message.Text != "" {
					t.Fatalf("quiet mode change injected text: %q", message.Text)
				}
			}
			if got := runtime.state.Snapshot().Mode; got != tc.to {
				t.Fatalf("state mode = %s, want %s", got, tc.to)
			}
			if !runtime.coord.HarnessHeld() {
				t.Fatal("quiet mode switch must hold the harness until the human resumes")
			}

			if err := runtime.coord.ResumeMode(ctx); err != nil {
				t.Fatalf("ResumeMode: %v", err)
			}
			if runtime.coord.HarnessHeld() {
				t.Fatal("ResumeMode must release the harness hold")
			}
			for _, agent := range []*testAgent{austin, tony} {
				message := agent.awaitMessage(t, "resume notice", func(m protocol.Message) bool {
					return m.Type == protocol.MsgModeChange && m.Text != ""
				})
				if message.Mode != tc.to.String() {
					t.Fatalf("resume target = %q, want %q", message.Mode, tc.to)
				}
			}
		})
	}
}
