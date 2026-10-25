package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
)

func TestChooseResumeSession(t *testing.T) {
	items := []sessionstore.Summary{
		{SessionID: "first", Snapshot: sessionstore.Snapshot{Phase: "EXECUTE"}},
		{SessionID: "second", Snapshot: sessionstore.Snapshot{Phase: "DONE"}},
	}
	for _, tc := range []struct {
		input string
		want  string
		bad   bool
	}{
		{input: "1\n", want: "first"},
		{input: "second\n", want: "second"},
		{input: "3\n", bad: true},
		{input: "\n", bad: true},
	} {
		got, err := chooseResumeSession(items, strings.NewReader(tc.input), &strings.Builder{})
		if (err != nil) != tc.bad {
			t.Fatalf("chooseResumeSession(%q) error = %v, want error=%t", tc.input, err, tc.bad)
		}
		if got != tc.want {
			t.Fatalf("chooseResumeSession(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
	var listing strings.Builder
	if _, err := chooseResumeSession(items, strings.NewReader("2\n"), &listing); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"status=unfinished", "status=completed", "DONE"} {
		if !strings.Contains(listing.String(), want) {
			t.Errorf("session listing %q does not contain %q", listing.String(), want)
		}
	}
}

func TestResumeChoicesIncludeCompletedSessions(t *testing.T) {
	available, broken := resumeChoices([]sessionstore.Summary{
		{SessionID: "in-progress", Snapshot: sessionstore.Snapshot{Phase: "EXECUTE"}},
		{SessionID: "completed", Snapshot: sessionstore.Snapshot{Phase: "DONE"}},
	})
	if len(broken) != 0 || len(available) != 2 || available[1].SessionID != "completed" {
		t.Fatalf("resume choices = %#v, broken=%#v; want both sessions available", available, broken)
	}
}

func TestResumeChoicesReportBrokenWithoutMakingThemSelectable(t *testing.T) {
	items := []sessionstore.Summary{
		{SessionID: "bad", Err: errors.New("corrupt state")},
		{SessionID: "only-choice", Snapshot: sessionstore.Snapshot{Phase: "EXECUTE"}},
		{SessionID: "second-choice", Snapshot: sessionstore.Snapshot{Phase: "EXECUTE"}},
	}
	available, broken := resumeChoices(items)
	if len(available) != 2 || available[0].SessionID != "only-choice" || available[1].SessionID != "second-choice" {
		t.Fatalf("available = %#v; unreadable session changed choice numbering", available)
	}
	if len(broken) != 1 || broken[0].SessionID != "bad" {
		t.Fatalf("broken = %#v, want bad session diagnostics", broken)
	}
	for _, input := range []string{"bad\n", "3\n"} {
		if got, err := chooseResumeSession(available, strings.NewReader(input), &strings.Builder{}); err == nil || got != "" {
			t.Fatalf("unreadable session was selectable from %q: id=%q err=%v", input, got, err)
		}
	}
	var diagnostics strings.Builder
	writeResumeDiagnostics(&diagnostics, broken)
	if !strings.Contains(diagnostics.String(), "bad") || !strings.Contains(diagnostics.String(), "corrupt state") {
		t.Fatalf("broken session diagnostics = %q", diagnostics.String())
	}
}

// TestResumedSessionTakesTheResumePath covers what the dashboard's /sessions
// picker reuses: the chosen id goes down the same configuration path a
// `duo resume <id>` launch takes, including the recovery grace a revived agent
// needs before the watchdog may call it idle.
func TestResumedSessionTakesTheResumePath(t *testing.T) {
	// The launch overrides a session started with `duo --mode goal` carries; they
	// must not travel into the picked session.
	cfg := config{
		mode:         project.ModeGoal,
		modeRaw:      "goal",
		modeSource:   "--mode",
		modeExplicit: true,
		harness:      harness.Config{},
	}
	got := cfg.resumedSession("20261025-081016-67064425")
	if !got.resume || got.resumeSession != "20261025-081016-67064425" {
		t.Fatalf("resumedSession = resume=%t id=%q, want a resume of the chosen id", got.resume, got.resumeSession)
	}
	if got.harness.RecoveryGrace < 30*time.Second || got.harness.RecoveryGrace > 60*time.Second {
		t.Fatalf("recovery grace %s is outside the required 30-60s window", got.harness.RecoveryGrace)
	}
	if got.modeExplicit || got.modeRaw != "" || got.modeSource != "" || got.mode != "" {
		t.Fatalf("resumedSession kept the launch mode override: %+v", got)
	}
	// A picked session persisted as FAST must resume even though the previous
	// launch was explicit Goal: keeping the override would refuse it only after
	// the open session had already been closed.
	if _, err := session.ResumeMode(got.modeRaw, got.modeExplicit, project.ModeFast, got.resumeSession, "", func(string) {}); err != nil {
		t.Fatalf("resuming a FAST session from a GOAL launch failed: %v", err)
	}
}
