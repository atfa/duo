package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/workspace"
)

// newTestOnboarding builds a wizard whose home directory is its own temporary
// directory and whose drivers all pass, so a test only has to describe the one
// situation it is about.
func newTestOnboarding(t *testing.T, dir, answers string) (*onboarding, string, *strings.Builder) {
	t.Helper()
	home := t.TempDir()
	var out strings.Builder
	o := newOnboarding(dir, strings.NewReader(answers), &out)
	o.home = func() string { return home }
	o.drivers = func(context.Context) ([]driverStatus, error) {
		return []driverStatus{{Name: "pi", CLIPath: "/usr/bin/pi"}}, nil
	}
	return o, home, &out
}

// onboardRepo creates a repository with one commit, which is the state Duo needs
// before it can do anything.
func onboardRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if _, err := gitText(context.Background(), dir, args...); err != nil {
			t.Fatal(err)
		}
	}
	git("init")
	git("config", "user.name", "Duo Test")
	git("config", "user.email", "duo-test@example.invalid")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-m", "base")
	return dir
}

// configureGitIdentity gives the temporary home a committer, which the wizard
// needs to create a first commit and must never invent on its own.
func configureGitIdentity(t *testing.T, home string) {
	t.Helper()
	for _, pair := range [][2]string{{"user.name", "Duo Test"}, {"user.email", "duo-test@example.invalid"}} {
		if _, err := gitText(context.Background(), home, "config", "--global", pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
}

func onboardGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitText(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

// writeFileAt writes a file, creating its parent, for the config fixtures.
func writeFileAt(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// committedProjectConfig gives the repository a project configuration without
// leaving it dirty, so the test is only about the configuration questions.
func committedProjectConfig(t *testing.T, dir, body string) {
	t.Helper()
	writeFileAt(t, filepath.Join(dir, ".duo", "config.json"), body)
	onboardGit(t, dir, "add", "-A")
	onboardGit(t, dir, "commit", "-m", "project config")
}

// A first run in a plain directory is the whole point: `git init` and an initial
// commit are offered, and the global config the human accepted is written with
// the defaults the plan documented.
func TestOnboardingPreparesAnUninitializedDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o, home, out := newTestOnboarding(t, dir, "1\n1\n1\n1\n")
	if err := o.onboard(context.Background()); err != nil {
		t.Fatalf("onboard: %v\n%s", err, out.String())
	}

	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("git init was not run: %v", err)
	}
	if head := strings.TrimSpace(onboardGit(t, dir, "rev-parse", "--verify", "HEAD")); head == "" {
		t.Fatal("no initial commit was created")
	}
	if dirty := strings.TrimSpace(onboardGit(t, dir, "status", "--porcelain")); dirty != "" {
		t.Fatalf("working tree still dirty after onboarding: %q", dirty)
	}
	if !strings.Contains(out.String(), "git init") {
		t.Errorf("prompts did not offer git init:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "GOAL") {
		t.Errorf("the default config offer does not name the GOAL workflow:\n%s", out.String())
	}
	global, err := os.ReadFile(filepath.Join(home, ".duo", "config.json"))
	if err != nil {
		t.Fatalf("default global config was not written: %v", err)
	}
	for _, want := range []string{`"language": "en"`, `"mode": "goal"`, `"tuiMode": "timeline"`} {
		if !strings.Contains(string(global), want) {
			t.Errorf("global config %q missing %q", global, want)
		}
	}
}

// Declining leaves the directory exactly as it was: no repository, no commit, no
// files. The human said no and the wizard is not allowed to guess.
func TestOnboardingDeclineChangesNothing(t *testing.T) {
	dir := t.TempDir()
	o, home, _ := newTestOnboarding(t, dir, "2\n")
	err := o.onboard(context.Background())
	if !errors.Is(err, errOnboardingDeclined) {
		t.Fatalf("onboard error = %v, want errOnboardingDeclined", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(statErr) {
		t.Fatalf(".git exists after a declined onboarding: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".duo", "config.json")); !os.IsNotExist(statErr) {
		t.Fatalf("config exists after a declined onboarding: %v", statErr)
	}
}

func TestOnboardingCommitsUncommittedWork(t *testing.T) {
	dir := onboardRepo(t)
	committedProjectConfig(t, dir, `{"mode":"fast"}`)
	writeFileAt(t, filepath.Join(dir, "new.txt"), "work\n")

	o, home, out := newTestOnboarding(t, dir, "1\n")
	writeFileAt(t, filepath.Join(home, ".duo", "config.json"), `{"mode":"fast"}`)
	if err := o.onboard(context.Background()); err != nil {
		t.Fatalf("onboard: %v\n%s", err, out.String())
	}
	if dirty := strings.TrimSpace(onboardGit(t, dir, "status", "--porcelain")); dirty != "" {
		t.Fatalf("working tree still dirty after commit: %q", dirty)
	}
	if subject := strings.TrimSpace(onboardGit(t, dir, "log", "-1", "--pretty=%s")); subject != "Commit work in progress before starting Duo" {
		t.Fatalf("last commit subject = %q", subject)
	}
	if tracked := strings.TrimSpace(onboardGit(t, dir, "ls-tree", "--name-only", "HEAD", "new.txt")); tracked == "" {
		t.Fatal("the committed change is not in HEAD")
	}
}

func TestOnboardingStashesUncommittedWork(t *testing.T) {
	dir := onboardRepo(t)
	committedProjectConfig(t, dir, `{"mode":"fast"}`)
	writeFileAt(t, filepath.Join(dir, "new.txt"), "work\n")

	o, home, out := newTestOnboarding(t, dir, "2\n")
	writeFileAt(t, filepath.Join(home, ".duo", "config.json"), `{"mode":"fast"}`)
	if err := o.onboard(context.Background()); err != nil {
		t.Fatalf("onboard: %v\n%s", err, out.String())
	}
	if dirty := strings.TrimSpace(onboardGit(t, dir, "status", "--porcelain")); dirty != "" {
		t.Fatalf("working tree still dirty after stash: %q", dirty)
	}
	if stashed := strings.TrimSpace(onboardGit(t, dir, "stash", "list")); stashed == "" {
		t.Fatal("the change was not stashed")
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("untracked file stayed in the tree: %v", err)
	}
}

// A first run with no agent CLI anywhere is refused before anything is touched,
// and the refusal names the drivers so the human knows what to install.
func TestOnboardingRefusesWithoutAnyDriverCLI(t *testing.T) {
	dir := t.TempDir()
	o, _, out := newTestOnboarding(t, dir, "")
	o.drivers = func(context.Context) ([]driverStatus, error) {
		return []driverStatus{
			{Name: "agy", Reason: "the agy CLI is not on PATH"},
			{Name: "codex", Reason: "the codex CLI is not on PATH"},
			{Name: "opencode", Reason: "the opencode CLI is not on PATH"},
			{Name: "pi", Reason: "the pi CLI is not on PATH"},
		}, nil
	}
	err := o.onboard(context.Background())
	if err == nil {
		t.Fatal("onboard started without a driver CLI")
	}
	for _, want := range []string{"agy", "codex", "opencode", "pi", "Install the latest version"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not contain %q", err, want)
		}
	}
	if strings.Contains(out.String(), "git init") {
		t.Errorf("onboarding prompted about Git after refusing for drivers:\n%s", out.String())
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(statErr) {
		t.Fatalf(".git exists after a driver refusal: %v", statErr)
	}
}

// An existing project configuration is the human's decision already, so the
// wizard asks nothing and prints nothing.
func TestOnboardingAsksNothingWhenEverythingIsReady(t *testing.T) {
	dir := onboardRepo(t)
	committedProjectConfig(t, dir, `{"mode":"fast"}`)

	o, home, out := newTestOnboarding(t, dir, "")
	writeFileAt(t, filepath.Join(home, ".duo", "config.json"), `{"mode":"fast"}`)
	if err := o.onboard(context.Background()); err != nil {
		t.Fatalf("onboard: %v\n%s", err, out.String())
	}
	if out.String() != "" {
		t.Fatalf("a ready repository produced prompts:\n%s", out.String())
	}
}

// The global config is offered, not imposed: "skip" must leave it absent and the
// repository's own configuration alone.
func TestOnboardingGlobalConfigIsOnlyWrittenWhenAccepted(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answer  string
		written bool
	}{
		{name: "accepted", answer: "1\n1\n", written: true},
		{name: "skipped", answer: "2\n1\n", written: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := onboardRepo(t)
			committedProjectConfig(t, dir, `{"mode":"fast"}`)
			o, home, out := newTestOnboarding(t, dir, tc.answer)
			if err := o.onboard(context.Background()); err != nil {
				t.Fatalf("onboard: %v\n%s", err, out.String())
			}
			_, err := os.Stat(filepath.Join(home, ".duo", "config.json"))
			if tc.written != (err == nil) {
				t.Fatalf("global config written = %t, want %t (stat err %v)", err == nil, tc.written, err)
			}
			if !tc.written && !strings.Contains(out.String(), "No global configuration") {
				t.Errorf("the offer was not shown:\n%s", out.String())
			}
		})
	}
}

// A missing project configuration is confirmed once, and declining stops the
// launch instead of starting on settings the human did not agree to.
func TestOnboardingConfirmsDefaultsForAProjectWithoutConfig(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answer  string
		wantErr bool
	}{
		{name: "start on defaults", answer: "1\n", wantErr: false},
		{name: "quit", answer: "2\n", wantErr: true},
		{name: "no answer", answer: "", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := onboardRepo(t)
			o, home, out := newTestOnboarding(t, dir, tc.answer)
			writeFileAt(t, filepath.Join(home, ".duo", "config.json"), `{"language":"en","mode":"step"}`)
			err := o.onboard(context.Background())
			if tc.wantErr != errors.Is(err, errOnboardingDeclined) {
				t.Fatalf("onboard error = %v, want declined=%t\n%s", err, tc.wantErr, out.String())
			}
			if !strings.Contains(out.String(), "no project configuration") {
				t.Errorf("the confirmation was not shown:\n%s", out.String())
			}
		})
	}
}

// The commit check runs before the dirty check and clears the tree, so a freshly
// initialised repository with files needs one prompt for init and one for the
// initial commit, not a third for the same files.
func TestOnboardingInitialCommitLeavesTheTreeClean(t *testing.T) {
	dir := t.TempDir()
	o, home, out := newTestOnboarding(t, dir, "1\n1\n1\n")
	writeFileAt(t, filepath.Join(home, ".duo", "config.json"), `{"language":"en","mode":"step"}`)
	if err := o.onboard(context.Background()); err != nil {
		t.Fatalf("onboard: %v\n%s", err, out.String())
	}
	if dirty := strings.TrimSpace(onboardGit(t, dir, "status", "--porcelain")); dirty != "" {
		t.Fatalf("working tree dirty after the initial commit: %q", dirty)
	}
	if got := strings.TrimSpace(onboardGit(t, dir, "log", "-1", "--pretty=%s")); got != "Initial commit" {
		t.Fatalf("initial commit subject = %q", got)
	}
	if root, err := workspace.FindRoot(context.Background(), dir); err != nil || !workspace.SamePath(root, dir) {
		t.Fatalf("FindRoot after init = %q, %v", root, err)
	}
}

// overrideGuideDrivers describes the machine the launch path sees.
func overrideGuideDrivers(t *testing.T, statuses []driverStatus) {
	t.Helper()
	previous := guideDrivers
	guideDrivers = func(context.Context) ([]driverStatus, error) { return statuses, nil }
	t.Cleanup(func() { guideDrivers = previous })
}

// overrideGuideTTY replaces the terminal the guide asks on. failIfCalled proves
// the guide did not try to ask at all.
func overrideGuideTTY(t *testing.T, open func() (*os.File, error)) {
	t.Helper()
	previous := openGuideTTY
	openGuideTTY = open
	t.Cleanup(func() { openGuideTTY = previous })
}

// TestFirstRunGuideAsksNothingWithoutATerminal is the contract scripts rely on:
// with no terminal to ask on, the guide leaves the launch exactly as it was, so
// a pipeline keeps the errors it always saw.
func TestFirstRunGuideAsksNothingWithoutATerminal(t *testing.T) {
	overrideGuideDrivers(t, []driverStatus{{Name: "pi", CLIPath: "/usr/bin/pi"}})
	overrideGuideTTY(t, func() (*os.File, error) { return nil, errors.New("no terminal") })

	dir := t.TempDir()
	if err := maybeGuideFirstRun(context.Background(), []string{dir}, false); err != nil {
		t.Fatalf("guide without a terminal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git exists after a guide that could not ask: %v", err)
	}
}

// A machine with no agent CLI is refused before anything is asked or touched, so
// the refusal is the same one a headless launch gets.
func TestFirstRunGuideRefusesWithoutADriverCLI(t *testing.T) {
	overrideGuideDrivers(t, []driverStatus{
		{Name: "agy", Reason: "the agy CLI is not on PATH"},
		{Name: "codex", Reason: "the codex CLI is not on PATH"},
		{Name: "opencode", Reason: "the opencode CLI is not on PATH"},
		{Name: "pi", Reason: "the pi CLI is not on PATH"},
	})
	overrideGuideTTY(t, func() (*os.File, error) {
		t.Fatal("the guide opened a terminal on a machine with no driver")
		return nil, nil
	})

	dir := t.TempDir()
	err := maybeGuideFirstRun(context.Background(), []string{dir}, false)
	if err == nil || !strings.Contains(err.Error(), "Install the latest version") {
		t.Fatalf("guide error = %v, want the install refusal", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(statErr) {
		t.Fatalf(".git exists after a driver refusal: %v", statErr)
	}
}

// A resume already has its repository and its configuration, so it is never
// asked anything - but it is still refused without a driver.
func TestFirstRunGuideDoesNotAskOnResume(t *testing.T) {
	overrideGuideDrivers(t, []driverStatus{{Name: "pi", CLIPath: "/usr/bin/pi"}})
	overrideGuideTTY(t, func() (*os.File, error) {
		t.Fatal("a resume opened the first-run guide")
		return nil, nil
	})
	if err := maybeGuideFirstRun(context.Background(), []string{"some-session"}, true); err != nil {
		t.Fatalf("resume guide: %v", err)
	}
}

// TestTheGlobalConfigTheGuideWritesIsTheOneTheLaunchReads pins the ordering
// between the guide and loadConfig: a global configuration created at the prompt
// must be what the very next loadConfig resolves, or the human has to start Duo
// twice to get the settings they just agreed to.
func TestTheGlobalConfigTheGuideWritesIsTheOneTheLaunchReads(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DUO_REPO", repo)
	t.Setenv("DUO_MODE", "")
	configureGitIdentity(t, home)

	// The global config must land in the real home loadConfig reads, so this test
	// builds the guide without the test home newTestOnboarding injects.
	var out strings.Builder
	o := newOnboarding(repo, strings.NewReader("1\n1\n1\n1\n"), &out)
	o.drivers = func(context.Context) ([]driverStatus, error) {
		return []driverStatus{{Name: "pi", CLIPath: "/usr/bin/pi"}}, nil
	}
	if err := o.onboard(context.Background()); err != nil {
		t.Fatalf("onboard: %v\n%s", err, out.String())
	}

	cfg, err := loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.language != "en" {
		t.Errorf("language = %q, want en", cfg.language)
	}
	if cfg.mode != project.ModeGoal {
		t.Errorf("mode = %q, want goal", cfg.mode)
	}
	if cfg.tuiMode != "timeline" {
		t.Errorf("tuiMode = %q, want timeline", cfg.tuiMode)
	}
}
