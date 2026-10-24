package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
)

// fakeRound is a headless round's session without agents: it hands out prepared
// views and records the task it was given.
type fakeRound struct {
	views     []session.View
	index     int
	submitted []string
}

func (f *fakeRound) View() session.View {
	view := f.views[f.index]
	if f.index < len(f.views)-1 {
		f.index++
	}
	return view
}

func (f *fakeRound) Do(_ context.Context, cmd session.Command) error {
	if task, ok := cmd.(session.SubmitTask); ok {
		f.submitted = append(f.submitted, task.Text)
	}
	return nil
}

// TestRunFastModeExitsDelivered is the headless contract a script relies on: the
// task is submitted, the round runs to DONE, the delivered artifact exits 0, and
// --json reports the same facts.
func TestRunFastModeExitsDelivered(t *testing.T) {
	svc := &fakeRound{views: []session.View{
		{
			SessionID: "sess-run",
			Mode:      project.ModeFast,
			Project:   project.Snapshot{Mode: project.ModeFast, Phase: project.PhaseExecute},
		},
		{
			SessionID: "sess-run",
			Mode:      project.ModeFast,
			Project:   project.Snapshot{Mode: project.ModeFast, Phase: project.PhaseDone},
			Delivery: sessionstore.Delivery{
				Status:       sessionstore.DeliveryApplied,
				AppliedHead:  "abc1234",
				TargetRepo:   "/tmp/repo",
				TargetBranch: "main",
			},
		},
	}}

	view, stop, err := runRound(context.Background(), svc, "fix the bug", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if stop != runStopDone {
		t.Fatalf("stop = %v, want the round to reach DONE", stop)
	}
	if len(svc.submitted) != 1 || svc.submitted[0] != "fix the bug" {
		t.Fatalf("task was not submitted to the session: %v", svc.submitted)
	}
	if code := runExitCode(stop, view.Delivery.Applied()); code != runExitDelivered {
		t.Fatalf("exit code = %d, want %d for a delivered artifact", code, runExitDelivered)
	}
	if err := reportRun(&bytes.Buffer{}, view, stop, false); err != nil {
		t.Fatalf("a delivered round must not be reported as a failure: %v", err)
	}

	var out bytes.Buffer
	if err := reportRun(&out, view, stop, true); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Phase     string `json:"phase"`
		Delivered bool   `json:"delivered"`
		ExitCode  int    `json:"exitCode"`
		Delivery  struct {
			AppliedHead string `json:"appliedHead"`
		} `json:"delivery"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("--json output is not JSON: %v\n%s", err, out.String())
	}
	if report.Phase != "DONE" || !report.Delivered || report.ExitCode != 0 {
		t.Fatalf("json report = %+v", report)
	}
	if report.Delivery.AppliedHead != "abc1234" {
		t.Fatalf("json report lost the delivered head: %+v", report)
	}
}

// TestRunTimeoutExitCode keeps a hung round from hanging the script that started
// it: the timeout stops the wait, the session stays resumable, and the exit code
// is the conventional one.
func TestRunTimeoutExitCode(t *testing.T) {
	svc := &fakeRound{views: []session.View{{
		SessionID: "sess-run",
		Mode:      project.ModeFast,
		Project:   project.Snapshot{Mode: project.ModeFast, Phase: project.PhaseExecute},
	}}}

	start := time.Now()
	view, stop, err := runRound(context.Background(), svc, "long task", 40*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if stop != runStopTimeout {
		t.Fatalf("stop = %v, want the timeout to stop the round", stop)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the timeout took %s", elapsed)
	}
	if code := runExitCode(stop, view.Delivery.Applied()); code != runExitTimeout {
		t.Fatalf("exit code = %d, want %d", code, runExitTimeout)
	}
	if err := reportRun(&bytes.Buffer{}, view, stop, false); !errors.Is(err, errRunTimeout) {
		t.Fatalf("reportRun = %v, want the timeout sentinel", err)
	}
}

// TestRunReportsRefusedDelivery covers the third exit code: the round ended but
// the artifact is pending on a human, which must not look like success.
func TestRunReportsRefusedDelivery(t *testing.T) {
	view := session.View{
		SessionID: "sess-run",
		Mode:      project.ModeFast,
		Project:   project.Snapshot{Mode: project.ModeFast, Phase: project.PhaseDone},
		Delivery:  sessionstore.Delivery{Status: sessionstore.DeliveryPending, Reason: "the original repository has uncommitted changes"},
	}
	var out bytes.Buffer
	err := reportRun(&out, view, runStopDone, false)
	if !errors.Is(err, errRunRefused) {
		t.Fatalf("reportRun = %v, want the refused sentinel", err)
	}
	if code := runExitCode(runStopDone, view.Delivery.Applied()); code != runExitRefused {
		t.Fatalf("exit code = %d, want %d", code, runExitRefused)
	}
	if !strings.Contains(out.String(), "uncommitted changes") {
		t.Fatalf("the refusal reason was not reported:\n%s", out.String())
	}
}

// TestParseRunArgsModeTimeoutJSON pins the command line the headless entry point
// documents.
func TestParseRunArgsModeTimeoutJSON(t *testing.T) {
	got, err := parseRunArgs([]string{"-m", "step", "--json", "--timeout", "90m", "fix", "the", "bug"})
	if err != nil {
		t.Fatal(err)
	}
	if got.mode != project.ModeStep || !got.modeSet || !got.json || got.timeout != 90*time.Minute {
		t.Fatalf("parsed %+v", got)
	}
	if got.task != "fix the bug" {
		t.Fatalf("task = %q", got.task)
	}
	if _, err := parseRunArgs([]string{"-m", "turbo", "task"}); err == nil {
		t.Fatal("an unknown mode must be rejected")
	}
	if _, err := parseRunArgs([]string{"--timeout", "soon", "task"}); err == nil {
		t.Fatal("a non-duration timeout must be rejected")
	}
	if _, err := parseRunArgs([]string{"--json"}); err == nil {
		t.Fatal("a run without a task must be rejected")
	}
}
