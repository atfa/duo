package coordinator

import (
	"context"
	"strings"
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

// TestLoopTaskAfterQuietSwitchWakesAustin pins the reported bug: a session
// started in GOAL, switched to LOOP from the interface (/mode → "Not now", which
// is the documented safe answer for typing the next prompt first) must still
// deliver the task. LOOP only queues the prompt as an inbox event and RunLoop
// sends the wake, so a task that leaves the quiet-switch hold in place is dropped
// silently: no agent receives anything and the event never leaves the inbox.
func TestLoopTaskAfterQuietSwitchWakesAustin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime := startE2EWithMode(t, ctx, project.ModeGoal)
	austin := runtime.dialAgent(t, protocol.Austin)
	tony := runtime.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool {
		return runtime.server.IsConnected(protocol.Austin) && runtime.server.IsConnected(protocol.Tony)
	}, "both agents to connect")

	if err := runtime.coord.SubmitUserTask(ctx, "publish the release notes"); err != nil {
		t.Fatalf("goal task: %v", err)
	}
	for _, agent := range []*testAgent{austin, tony} {
		agent.awaitMessage(t, "goal task", func(m protocol.Message) bool {
			return m.Type == protocol.MsgHumanPrompt
		})
	}

	if err := runtime.coord.SwitchModeQuiet(ctx, project.ModeLoop, ""); err != nil {
		t.Fatalf("SwitchModeQuiet: %v", err)
	}
	if !runtime.coord.HarnessHeld() {
		t.Fatal("a quiet mode switch must hold the harness until the human continues")
	}

	go runtime.coord.RunLoop(ctx)
	if err := runtime.coord.SubmitUserTask(ctx, "also update the changelog"); err != nil {
		t.Fatalf("LOOP task: %v", err)
	}
	if runtime.coord.HarnessHeld() {
		t.Fatal("a human task must release the harness hold held by the quiet switch")
	}

	// The prompt itself stays an inbox event, so the wake is what makes it
	// effective for Austin.
	austin.awaitMessage(t, "LOOP wake for the human task", func(m protocol.Message) bool {
		return m.Type == protocol.MsgDuoNotice && strings.Contains(m.Text, "LOOP event")
	})
	snap := runtime.state.Snapshot()
	if got := len(snap.Loop.Events[protocol.Austin]); got != 1 {
		t.Fatalf("Austin's loop inbox has %d events, want the one human event", got)
	}
	if got := snap.Loop.Events[protocol.Austin][0]; got.Kind != "human" || !strings.Contains(got.Text, "changelog") {
		t.Fatalf("queued event = %+v, want the human task", got)
	}
	if snap.Mode != project.ModeLoop || snap.Started != true {
		t.Fatalf("mode=%s started=%v, want a started LOOP round", snap.Mode, snap.Started)
	}
}

// TestRestoredLoopSessionTaskReleasesResumeHold is the same defect through the
// other door: a restored session is held by HoldResume (resumePending and
// harnessOff), and only an explicit human continuation clears it. A LOOP task
// must count as one, otherwise RunLoop never wakes the restored round.
func TestRestoredLoopSessionTaskReleasesResumeHold(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime := startE2EWithMode(t, ctx, project.ModeLoop)
	austin := runtime.dialAgent(t, protocol.Austin)
	runtime.dialAgent(t, protocol.Tony)
	waitFor(t, func() bool {
		return runtime.server.IsConnected(protocol.Austin) && runtime.server.IsConnected(protocol.Tony)
	}, "both agents to connect")

	// What recovery does for a started, unfinished session.
	runtime.state.MarkStarted()
	runtime.coord.HoldResume()
	if !runtime.coord.HarnessHeld() || !runtime.coord.ResumePending() {
		t.Fatal("a restored session must stay quiet until the human continues")
	}

	go runtime.coord.RunLoop(ctx)
	if err := runtime.coord.SubmitUserTask(ctx, "finish the restored round"); err != nil {
		t.Fatalf("restored LOOP task: %v", err)
	}
	if runtime.coord.HarnessHeld() || runtime.coord.ResumePending() {
		t.Fatal("a human task must clear the restored-session hold")
	}
	austin.awaitMessage(t, "LOOP wake for the restored task", func(m protocol.Message) bool {
		return m.Type == protocol.MsgDuoNotice && strings.Contains(m.Text, "LOOP event")
	})
}

func TestQuietModeSwitchDoesNotWakeAgents(t *testing.T) {
	for _, tc := range []struct {
		name string
		from project.Mode
		to   project.Mode
	}{
		{"fast-to-goal-escalation", project.ModeFast, project.ModeGoal},
		{"goal-to-step", project.ModeGoal, project.ModeStep},
		// LOOP is scheduled by RunLoop, so a quiet switch there must be quiet too:
		// only a human task releases the hold that keeps the watchdog silent.
		{"goal-to-loop", project.ModeGoal, project.ModeLoop},
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
