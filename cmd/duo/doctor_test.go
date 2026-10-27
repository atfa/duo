package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/observer"
)

// doctorEnvForTest is a machine that passes everything, so each test changes the
// one thing it is about. The seams exist for exactly this: the failures worth
// diagnosing are the ones a developer's machine does not have.
func doctorEnvForTest(t *testing.T) *doctorEnv {
	t.Helper()
	home := t.TempDir()
	env := newDoctorEnv("")
	env.findRoot = func(context.Context, string) (string, error) { return "/repo", nil }
	env.git = func(_ context.Context, _ string, args ...string) (string, error) {
		switch args[0] {
		case "version":
			return "git version 2.45.0\n", nil
		case "status":
			return "", nil
		case "rev-parse":
			return "main\n", nil
		}
		return "", nil
	}
	env.loadConfig = func(string) (configFile, error) { return configFile{}, nil }
	env.pluginLookup = func(name string) (string, bool) { return "/usr/bin/duo-plugin-" + name, true }
	env.probe = func(context.Context, string) (driver.ProbeResult, error) {
		return driver.ProbeResult{Available: true, AgentPath: "/usr/bin/pi"}, nil
	}
	env.contract = func(context.Context, string, string) driver.Report {
		return driver.Report{Results: []driver.Result{{Name: "describe", OK: true}}}
	}
	env.stat = func(string) (os.FileInfo, error) { return dirInfo{}, nil }
	env.httpGet = func(context.Context, string) (int, error) { return 200, nil }
	env.home = func() string { return home }
	env.workDir = t.TempDir
	return env
}

// dirInfo is a stat result that says "directory here".
type dirInfo struct{}

func (dirInfo) Name() string       { return "duo" }
func (dirInfo) Size() int64        { return 0 }
func (dirInfo) Mode() os.FileMode  { return os.ModeDir | 0o755 }
func (dirInfo) ModTime() time.Time { return time.Time{} }
func (dirInfo) IsDir() bool        { return true }
func (dirInfo) Sys() any           { return nil }

func findCheck(t *testing.T, checks []doctorCheck, code string) doctorCheck {
	t.Helper()
	for _, check := range checks {
		if check.Code == code {
			return check
		}
	}
	t.Fatalf("no check reported %q; report was %+v", code, checks)
	return doctorCheck{}
}

// TestDoctorHealthyMachineHasNoFails is the positive control: a machine that has
// everything must not be told it has a problem, or the operator learns to ignore
// the report.
func TestDoctorHealthyMachineHasNoFails(t *testing.T) {
	checks := doctorEnvForTest(t).run(context.Background())
	if doctorFailed(checks) {
		for _, check := range checks {
			t.Logf("%s %s: %s", check.Status, check.Code, check.Detail)
		}
		t.Fatal("a healthy machine reported a failure")
	}
	for _, code := range []string{"git.version", "repo.git", "repo.dirty", "base.branch", "worktree.root", "config.file", "driver.plugin", "driver.cli", "driver.contract", "extension.pi"} {
		findCheck(t, checks, code)
	}
}

func TestDoctorReportsMissingDriver(t *testing.T) {
	env := doctorEnvForTest(t)
	env.probe = func(context.Context, string) (driver.ProbeResult, error) {
		return driver.ProbeResult{Available: false, Reason: "the pi CLI is not on PATH"}, nil
	}

	check := findCheck(t, env.run(context.Background()), "driver.cli")

	if check.Status != doctorFail {
		t.Fatalf("status = %s, want fail (%s)", check.Status, check.Detail)
	}
	if !strings.Contains(check.Detail, "pi CLI is not on PATH") {
		t.Errorf("detail %q does not carry the driver's own reason", check.Detail)
	}
	if check.Fix == "" {
		t.Error("a missing driver CLI has to say how to fix it")
	}
}

// TestDoctorReportsMissingPlugin separates the two failures an operator cannot
// tell apart from the outside: the agent CLI is missing, and the Duo plugin that
// knows how to launch it is missing.
func TestDoctorReportsMissingPlugin(t *testing.T) {
	env := doctorEnvForTest(t)
	env.pluginLookup = func(string) (string, bool) { return "", false }

	check := findCheck(t, env.run(context.Background()), "driver.plugin")

	if check.Status != doctorFail {
		t.Fatalf("status = %s, want fail (%s)", check.Status, check.Detail)
	}
	if !strings.Contains(check.Fix, "duo-plugin-") {
		t.Errorf("fix %q does not name the executable to install", check.Fix)
	}
}

func TestDoctorReportsDirtyRepo(t *testing.T) {
	env := doctorEnvForTest(t)
	env.git = func(_ context.Context, _ string, args ...string) (string, error) {
		switch args[0] {
		case "version":
			return "git version 2.45.0\n", nil
		case "status":
			return " M src/app.go\n?? scratch.txt\n", nil
		}
		return "main\n", nil
	}

	check := findCheck(t, env.run(context.Background()), "repo.dirty")

	if check.Status != doctorFail {
		t.Fatalf("status = %s, want fail (%s)", check.Status, check.Detail)
	}
	if !strings.Contains(check.Detail, "2 uncommitted path") {
		t.Errorf("detail %q does not count the dirty paths", check.Detail)
	}
	if !strings.Contains(check.Fix, "commit or stash") {
		t.Errorf("fix %q does not say what to do", check.Fix)
	}
}

func TestDoctorReportsBadConfig(t *testing.T) {
	env := doctorEnvForTest(t)
	env.loadConfig = func(string) (configFile, error) {
		return configFile{}, errors.New("parse .duo/config.json: invalid character '}' looking for beginning of object key string")
	}

	check := findCheck(t, env.run(context.Background()), "config.file")

	if check.Status != doctorFail {
		t.Fatalf("status = %s, want fail (%s)", check.Status, check.Detail)
	}
	if !strings.Contains(check.Detail, "invalid character") {
		t.Errorf("detail %q drops the parser's reason", check.Detail)
	}
	if !strings.Contains(check.Fix, "config.json") {
		t.Errorf("fix %q does not name a file to edit", check.Fix)
	}
}

// A view the interface cannot draw is a launch failure, so the report has to
// name it instead of saying the configuration parses.
func TestDoctorReportsBadTUIMode(t *testing.T) {
	env := doctorEnvForTest(t)
	env.loadConfig = func(string) (configFile, error) {
		return configFile{TUIMode: "mosaic"}, nil
	}

	check := findCheck(t, env.run(context.Background()), "config.file")

	if check.Status != doctorFail {
		t.Fatalf("status = %s, want fail (%s)", check.Status, check.Detail)
	}
	if !strings.Contains(check.Detail, "mosaic") {
		t.Errorf("detail %q does not name the rejected value", check.Detail)
	}
	if !strings.Contains(check.Fix, "tuiMode") {
		t.Errorf("fix %q does not name the key to edit", check.Fix)
	}
}

func TestDoctorReportsOldGit(t *testing.T) {
	env := doctorEnvForTest(t)
	env.git = func(_ context.Context, _ string, args ...string) (string, error) {
		if args[0] == "version" {
			return "git version 1.9.1\n", nil
		}
		return "main\n", nil
	}

	check := findCheck(t, env.run(context.Background()), "git.version")

	if check.Status != doctorFail {
		t.Fatalf("status = %s, want fail (%s)", check.Status, check.Detail)
	}
	if !strings.Contains(check.Fix, "worktree") {
		t.Errorf("fix %q does not say why the version matters", check.Fix)
	}
}

// TestDoctorReportsDetachedHead is the failure that costs the most: the work is
// finished and signed, and there is no branch to hand it to.
func TestDoctorReportsDetachedHead(t *testing.T) {
	env := doctorEnvForTest(t)
	env.git = func(_ context.Context, _ string, args ...string) (string, error) {
		switch args[0] {
		case "version":
			return "git version 2.45.0\n", nil
		case "rev-parse":
			return "HEAD\n", nil
		}
		return "", nil
	}

	check := findCheck(t, env.run(context.Background()), "base.branch")

	if check.Status != doctorFail {
		t.Fatalf("status = %s, want fail (%s)", check.Status, check.Detail)
	}
	if !strings.Contains(check.Fix, "branch") {
		t.Errorf("fix %q does not name the fix", check.Fix)
	}
}

func TestDoctorReportsContractFailure(t *testing.T) {
	env := doctorEnvForTest(t)
	env.contract = func(context.Context, string, string) driver.Report {
		return driver.Report{Results: []driver.Result{
			{Name: "describe", OK: true},
			{Name: "capability-models", OK: false, Detail: "capabilities.models is declared but models did not answer"},
		}}
	}

	check := findCheck(t, env.run(context.Background()), "driver.contract")

	if check.Status != doctorFail {
		t.Fatalf("status = %s, want fail (%s)", check.Status, check.Detail)
	}
	if !strings.Contains(check.Detail, "capability-models") {
		t.Errorf("detail %q does not name the failing check", check.Detail)
	}
}

// TestDoctorReportsMissingBridge is the failure that shows up as an agent that
// starts, answers nothing, and never attaches: the driver binary is installed,
// the adapter that runs inside the agent is not.
func TestDoctorReportsMissingBridge(t *testing.T) {
	env := doctorEnvForTest(t)
	env.stat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }

	check := findCheck(t, env.run(context.Background()), "extension.pi")

	if check.Status != doctorFail {
		t.Fatalf("status = %s, want fail (%s)", check.Status, check.Detail)
	}
	if !strings.Contains(check.Fix, "install-pi-extension.sh") {
		t.Errorf("fix %q does not name the installer", check.Fix)
	}
}

// TestDoctorBridgeChecksSkipUnconfiguredDrivers keeps the report honest about the
// machine the session will use: a driver nobody configured is not a problem.
func TestDoctorBridgeChecksSkipUnconfiguredDrivers(t *testing.T) {
	env := doctorEnvForTest(t)
	env.stat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }

	check := findCheck(t, env.run(context.Background()), "extension.opencode")

	if check.Status != doctorSkip {
		t.Fatalf("status = %s, want skip (%s)", check.Status, check.Detail)
	}
}

func TestDoctorObserverUnreachableIsAWarn(t *testing.T) {
	env := doctorEnvForTest(t)
	env.loadConfig = func(string) (configFile, error) {
		return configFile{Observer: &observer.Settings{Enabled: true, Endpoint: "http://127.0.0.1:9/v1", Model: "m"}}, nil
	}
	env.httpGet = func(context.Context, string) (int, error) { return 0, errors.New("connection refused") }

	checks := env.run(context.Background())
	check := findCheck(t, checks, "observer.reach")

	if check.Status != doctorWarn {
		t.Fatalf("status = %s, want warn: an observer that is down must not stop a session", check.Status)
	}
	if doctorFailed(checks) {
		for _, c := range checks {
			if c.Status == doctorFail {
				t.Fatalf("the observer being unreachable produced a fail: %s %s", c.Code, c.Detail)
			}
		}
	}
}

func TestDoctorReportsInvalidObserverConfig(t *testing.T) {
	env := doctorEnvForTest(t)
	env.loadConfig = func(string) (configFile, error) {
		return configFile{Observer: &observer.Settings{Enabled: true, Endpoint: "not-a-url"}}, nil
	}

	check := findCheck(t, env.run(context.Background()), "observer.config")

	if check.Status != doctorFail {
		t.Fatalf("status = %s, want fail (%s)", check.Status, check.Detail)
	}
}

// TestDoctorObserverAnswerIsReachable keeps the check about reachability. The
// observer posts to <endpoint>/chat/completions, so a 404 on the base URL is what
// an OpenAI-compatible gateway answers, and calling that unreachable sends the
// operator hunting for a problem that is not there.
func TestDoctorObserverAnswerIsReachable(t *testing.T) {
	env := doctorEnvForTest(t)
	env.loadConfig = func(string) (configFile, error) {
		return configFile{Observer: &observer.Settings{Enabled: true, Endpoint: "http://127.0.0.1:4000/v1", Model: "m"}}, nil
	}
	env.httpGet = func(context.Context, string) (int, error) { return 404, nil }

	check := findCheck(t, env.run(context.Background()), "observer.reach")

	if check.Status != doctorOK {
		t.Fatalf("status = %s, want ok: an HTTP answer proves the endpoint is up (%s)", check.Status, check.Detail)
	}
}

func TestDoctorExitCodeFollowsTheReport(t *testing.T) {
	if doctorFailed([]doctorCheck{{Code: "x", Status: doctorWarn}}) {
		t.Error("a warn must not change the exit code")
	}
	if !doctorFailed([]doctorCheck{{Code: "x", Status: doctorFail}}) {
		t.Error("a fail must change the exit code")
	}
}

func TestParseDoctorArgs(t *testing.T) {
	repo, err := parseDoctorArgs([]string{"/tmp/project"})
	if err != nil || repo != "/tmp/project" {
		t.Fatalf("repository = %q, err = %v", repo, err)
	}
	if _, err := parseDoctorArgs([]string{"--nope"}); err == nil {
		t.Error("an unknown flag was accepted")
	}
	if _, err := parseDoctorArgs([]string{"a", "b"}); err == nil {
		t.Error("two repositories were accepted")
	}
}
