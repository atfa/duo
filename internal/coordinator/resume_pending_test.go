package coordinator

import (
	"context"
	"testing"

	"github.com/atfa/duo/internal/project"
)

func TestWakeByModeChangeClearsResumePending(t *testing.T) {
	for _, tc := range []struct {
		name string
		wake func(*Coordinator) error
	}{
		{"switch mode", func(c *Coordinator) error { return c.SwitchMode(context.Background(), project.ModeStep, "") }},
		{"resume mode work", func(c *Coordinator) error { return c.ResumeMode(context.Background()) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := startE2EWithMode(t, context.Background(), project.ModeGoal)
			runtime.coord.HoldResume()
			if err := tc.wake(runtime.coord); err != nil {
				t.Fatal(err)
			}
			if runtime.coord.ResumePending() || runtime.coord.HarnessHeld() {
				t.Fatal("explicit mode wake left resume pending or harness held")
			}
		})
	}
}

func TestQuietModeSwitchKeepsResumePending(t *testing.T) {
	runtime := startE2EWithMode(t, context.Background(), project.ModeGoal)
	runtime.coord.HoldResume()
	if err := runtime.coord.SwitchModeQuiet(context.Background(), project.ModeStep, ""); err != nil {
		t.Fatal(err)
	}
	if !runtime.coord.ResumePending() || !runtime.coord.HarnessHeld() {
		t.Fatal("quiet mode switch should preserve pending resume and harness hold")
	}
}

func TestNotifyEscalationClearsResumePending(t *testing.T) {
	runtime := startE2EWithMode(t, context.Background(), project.ModeGoal)
	runtime.coord.HoldResume()
	if err := runtime.coord.SwitchModeQuiet(context.Background(), project.ModeFast, ""); err != nil {
		t.Fatal(err)
	}
	if !runtime.coord.ResumePending() {
		t.Fatal("quiet switch unexpectedly cleared resume pending")
	}
	if err := runtime.coord.SwitchMode(context.Background(), project.ModeGoal, "wake"); err != nil {
		t.Fatal(err)
	}
	if runtime.coord.ResumePending() || runtime.coord.HarnessHeld() {
		t.Fatal("notify escalation left resume pending or harness held")
	}
}
