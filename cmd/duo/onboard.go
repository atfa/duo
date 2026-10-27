package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/observer"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/workspace"
)

// errOnboardingDeclined reports that the human chose not to change anything.
// The launch stops with the reason instead of starting a session Duo already
// knows cannot work.
var errOnboardingDeclined = errors.New("first-run setup declined")

// onboarding is the first-run guide: it makes this machine able to start a Duo
// session and changes nothing the human did not approve.
//
// It is a plain-terminal wizard on purpose. Every problem it answers - no Git
// repository, no commit, uncommitted work, no configuration, no driver CLI -
// is a reason Start would fail or deliver badly, and all of them are known
// before the interface opens. It therefore runs before the session exists and
// owns no session state.
//
// Every probe and every write is a field, because the situations worth testing
// are the ones a developer's machine does not have: a directory with no
// repository, a driver whose CLI is missing, a home directory with no Duo
// configuration. A test builds them instead of hoping the host has them.
type onboarding struct {
	launchDir string

	in  io.Reader
	out io.Writer
	// reader is kept across questions: a fresh bufio.Reader per prompt would
	// drop the answers already buffered from a piped terminal.
	reader *bufio.Reader

	findRoot  func(ctx context.Context, dir string) (string, error)
	git       func(ctx context.Context, dir string, args ...string) (string, error)
	drivers   func(ctx context.Context) ([]driverStatus, error)
	stat      func(string) (os.FileInfo, error)
	mkdirAll  func(string, os.FileMode) error
	writeFile func(string, []byte, os.FileMode) error
	home      func() string

	// The observer is the one optional part of the guide: whether it was ever
	// configured, and where a choice is stored, are separate probes because the
	// answer comes from files and the environment a session would read too.
	loadMergedConfig func(launchDir string) (configFile, error)
	keyFileExists    func() bool
	envOverrides     func() (enabled, endpoint, model bool)
	saveObserver     func(root string, settings observer.Settings) error
	saveAPIKey       func(key string) (string, error)
}

// driverStatus is one shipped driver's answer to "can this machine run me?".
// The CLI path comes from the driver's own probe rather than a list of binary
// names here, so adding a fifth driver needs nothing in this file.
type driverStatus struct {
	Name    string
	CLIPath string
	Reason  string
}

func newOnboarding(launchDir string, in io.Reader, out io.Writer) *onboarding {
	return &onboarding{
		launchDir:        launchDir,
		in:               in,
		out:              out,
		findRoot:         workspace.FindRoot,
		git:              gitText,
		drivers:          probeShippedDrivers,
		stat:             os.Stat,
		mkdirAll:         os.MkdirAll,
		writeFile:        os.WriteFile,
		home:             func() string { home, _ := os.UserHomeDir(); return home },
		loadMergedConfig: loadMergedConfigFile,
		keyFileExists:    observer.KeyFileExists,
		envOverrides:     observer.EnvOverrides,
		saveObserver:     workspace.SaveProjectObserver,
		saveAPIKey:       observer.SaveAPIKey,
	}
}

// onboard runs the whole first-run guide: the driver refusal and, when a terminal
// is available, the questions about this machine.
func (o *onboarding) onboard(ctx context.Context) error {
	if err := o.ensureDrivers(ctx); err != nil {
		return err
	}
	return o.onboardLocalState(ctx)
}

// onboardLocalState answers the questions about the repository and the settings,
// in the order a session needs them: a repository to work in, a commit to branch
// from, a clean tree to deliver into, configuration for the round, and the
// optional observer.
func (o *onboarding) onboardLocalState(ctx context.Context) error {
	root, err := o.ensureRepository(ctx)
	if err != nil {
		return err
	}
	if err := o.ensureCommit(ctx, root); err != nil {
		return err
	}
	if err := o.ensureClean(ctx, root); err != nil {
		return err
	}
	if err := o.ensureConfig(o.launchDir, root); err != nil {
		return err
	}
	return o.ensureObserver(root)
}

// maybeGuideFirstRun is the launch path's entry point. It refuses a machine with
// no agent CLI at all, then, only when a terminal can answer, asks about the
// repository and the configuration of a fresh session.
//
// It runs before loadConfig so a global configuration created here is the one
// this launch reads: asking and then starting on the old settings would make the
// human answer twice. A resume is not asked anything - its repository and
// configuration already exist - but it is still refused when no driver can run.
func maybeGuideFirstRun(ctx context.Context, args []string, resume bool) error {
	for _, arg := range args {
		if helpRequested(arg) {
			return nil // Usage, not a launch: nothing to check yet.
		}
	}
	if err := checkDrivers(ctx); err != nil {
		return err
	}
	if resume {
		return nil
	}
	parsed, err := parseArgs(args)
	if err != nil {
		// loadConfig reports the argument error in its own words, with the usage.
		return nil
	}
	dir := strings.TrimSpace(parsed.repository)
	if dir == "" {
		dir = "."
	}
	o := newOnboarding(dir, nil, nil)
	tty, err := openGuideTTY()
	if err != nil {
		// No terminal to ask on. A script or a CI run must keep the errors it has
		// always seen rather than stop at a prompt nobody can answer.
		return nil
	}
	defer tty.Close()
	o.in, o.out = tty, tty
	return o.onboardLocalState(ctx)
}

// openGuideTTY is a variable so a test can prove the guide asks nothing when no
// terminal can be opened.
var openGuideTTY = func() (*os.File, error) {
	return os.OpenFile("/dev/tty", os.O_RDWR, 0)
}

// guideDrivers is the probe the launch path asks about installed driver CLIs. It
// is a variable so a test can describe a machine with no agent CLI at all, which
// is exactly the machine that must be refused.
var guideDrivers = probeShippedDrivers

// checkDrivers refuses a launch when none of the driver CLIs Duo ships is on
// PATH. It is the driver half of the guide, without any questions, so a headless
// launch refuses in exactly the same words as an interactive one.
func checkDrivers(ctx context.Context) error {
	statuses, err := guideDrivers(ctx)
	if err != nil {
		return err
	}
	return requireDriver(statuses)
}

// ensureDrivers is the one check with no fix: Duo is two coding agents, so with
// no agent CLI installed there is nothing for it to collaborate with. It refuses
// here, before Git is touched, rather than starting a session that reports a
// missing CLI agent by agent after the interface is already up.
func (o *onboarding) ensureDrivers(ctx context.Context) error {
	statuses, err := o.drivers(ctx)
	if err != nil {
		return err
	}
	return requireDriver(statuses)
}

func requireDriver(statuses []driverStatus) error {
	names := make([]string, 0, len(statuses))
	for _, status := range statuses {
		if strings.TrimSpace(status.CLIPath) != "" {
			return nil
		}
		names = append(names, status.Name)
	}
	return fmt.Errorf(
		"no coding-agent CLI is installed: Duo found none of %s on PATH.\n\n"+
			"Install the latest version of one of them and start Duo again; `duo doctor` reports what each driver needs.",
		strings.Join(names, ", "))
}

// ensureRepository answers whether Duo has a repository to isolate its agent
// worktrees in. `git init` is offered rather than performed automatically,
// because initialising a repository is a decision about the human's directory.
func (o *onboarding) ensureRepository(ctx context.Context) (string, error) {
	root, err := o.findRoot(ctx, o.launchDir)
	if err == nil {
		return root, nil
	}
	dir := strings.TrimSpace(o.launchDir)
	if dir == "" {
		dir = "."
	}
	choice := o.askChoice(
		"No Git repository at "+displayPath(dir)+".\nDuo isolates each agent in a git worktree, so it needs one.",
		[]string{"run `git init` here", "quit without changing anything"})
	if choice != 1 {
		return "", fmt.Errorf("%w\n\n%s", errOnboardingDeclined, err)
	}
	if _, err := o.git(ctx, dir, "init"); err != nil {
		return "", err
	}
	return o.findRoot(ctx, o.launchDir)
}

// ensureCommit answers whether there is a commit to deliver onto: a worktree
// cannot be created from a ref that does not exist, and a repository that
// `git init` just created has none. The initial commit honours .gitignore
// because it is an ordinary `git add -A`.
func (o *onboarding) ensureCommit(ctx context.Context, root string) error {
	if o.hasCommit(ctx, root) {
		return nil
	}
	choice := o.askChoice(
		"The repository has no commit yet, and Duo needs one to branch from.",
		[]string{"create an initial commit with everything in the working tree (honours .gitignore)", "quit without changing anything"})
	if choice != 1 {
		return fmt.Errorf("%w: the repository has no commit, so there is nothing to deliver onto", errOnboardingDeclined)
	}
	if _, err := o.git(ctx, root, "add", "-A"); err != nil {
		return err
	}
	// An empty repository (only ignored files, or nothing at all) has nothing to
	// stage; an empty commit is what still gives the session a base.
	if o.isClean(ctx, root) {
		if _, err := o.git(ctx, root, "commit", "--allow-empty", "-m", "Initial commit"); err != nil {
			return err
		}
	} else if _, err := o.git(ctx, root, "commit", "-m", "Initial commit"); err != nil {
		return err
	}
	if !o.hasCommit(ctx, root) {
		return errors.New("git commit reported success but the repository still has no commit")
	}
	return nil
}

// ensureClean answers whether the working tree is free of work Duo must not
// overwrite. Committing and stashing are offered side by side because which one
// is right belongs to the human: a stash keeps the change out of history, and a
// commit records it as the base the session delivers on top of.
func (o *onboarding) ensureClean(ctx context.Context, root string) error {
	dirty, err := o.git(ctx, root, "status", "--porcelain")
	if err != nil {
		return err
	}
	paths := countPaths(dirty)
	if paths == 0 {
		return nil
	}
	choice := o.askChoice(
		fmt.Sprintf("The working tree has %d uncommitted path(s).\nDuo delivers into this tree, so it will not start on top of uncommitted work.", paths),
		[]string{
			"commit everything (`git add -A` then `git commit`)",
			"stash everything, including untracked files",
			"quit without changing anything",
		})
	switch choice {
	case 1:
		if _, err := o.git(ctx, root, "add", "-A"); err != nil {
			return err
		}
		if _, err := o.git(ctx, root, "commit", "-m", "Commit work in progress before starting Duo"); err != nil {
			return err
		}
	case 2:
		if _, err := o.git(ctx, root, "stash", "push", "--include-untracked", "-m", "duo: work in progress before the session"); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: the working tree still has %d uncommitted path(s); commit or stash them before starting Duo", errOnboardingDeclined, paths)
	}
	// Ask Git again instead of trusting the command: a commit that missed an
	// ignored-but-tracked path, or a stash of nothing, would otherwise surface
	// only as Duo's own dirty-tree refusal a few lines later.
	dirty, err = o.git(ctx, root, "status", "--porcelain")
	if err != nil {
		return err
	}
	if left := countPaths(dirty); left > 0 {
		return fmt.Errorf("%d uncommitted path(s) are still in %s; commit or stash them before starting Duo:\n%s", left, root, strings.TrimSpace(dirty))
	}
	return nil
}

// ensureConfig answers which settings the round starts from. Nothing is written
// into the repository: a project configuration belongs to the project, so the
// human only confirms that the defaults from ~/.duo/config.json are what they
// want. A human with no global file is offered one, which is the only file this
// wizard creates outside Git.
func (o *onboarding) ensureConfig(launchDir, root string) error {
	home := strings.TrimSpace(o.home())
	if home == "" {
		// Without a home directory there is no global configuration to offer and
		// nothing to ask about: the session uses Duo's built-in defaults.
		return nil
	}
	globalPath := filepath.Join(home, ".duo", "config.json")
	if _, err := o.stat(globalPath); errors.Is(err, fs.ErrNotExist) {
		if o.askChoice(
			"No global configuration at "+globalPath+".\n"+
				"Create one with English and GOAL mode? (The interface opens on the TIMELINE view; SPLIT is a toggle there.)",
			[]string{"create " + globalPath, "skip, use Duo's built-in defaults"}) == 1 {
			if err := o.writeDefaultConfig(globalPath); err != nil {
				return err
			}
			fmt.Fprintln(o.out, "Wrote "+globalPath)
		}
	}

	for _, path := range projectConfigPaths(launchDir, root) {
		if _, err := o.stat(path); err == nil {
			return nil
		}
	}
	if o.askChoice(
		"This repository has no project configuration, so the round starts on the defaults from "+globalPath+".",
		[]string{"start with those defaults", "quit without starting a session"}) != 1 {
		return fmt.Errorf("%w: no project configuration, so no session was started", errOnboardingDeclined)
	}
	return nil
}

// ensureObserver offers the optional observer once, here, rather than only in the
// dashboard a moment later. The dashboard's own setup panel appears just when the
// observer was never configured, so a human who answers here is not asked twice;
// skipping leaves the same state the panel's "Later" button leaves behind.
func (o *onboarding) ensureObserver(root string) error {
	if o.observerConfigured() {
		return nil
	}
	choice := o.askChoice(
		"Duo's optional observer writes status reports on request (Ctrl+E) and names sessions.\n"+
			"It is not configured yet, and nothing it does changes the workflow.",
		[]string{"set it up now", "later (stays off; /observer opens the settings at any time)"})
	switch choice {
	case 1:
		return o.setUpObserver(root)
	case 2:
		// Record the dismissal where the dashboard records its own, so the setup
		// panel does not ask again on the next start.
		if err := o.saveObserver(root, observer.Settings{Enabled: false}); err != nil {
			return err
		}
		fmt.Fprintln(o.out, "The observer stays off. /observer opens the settings at any time.")
	}
	// A blank or unreadable answer changes nothing: the dashboard panel will offer
	// the same choice once the session is up.
	return nil
}

// setUpObserver collects the three settings a working observer needs, keeping the
// documented defaults when the human just presses Enter.
func (o *onboarding) setUpObserver(root string) error {
	endpoint := o.askText("Observer base URL", observer.DefaultEndpoint)
	model := o.askText("Observer model", observer.DefaultModel)
	key := o.askText("Observer API key (blank keeps DUO_OBSERVER_API_KEY or the saved key)", "")

	chosen := observer.Config{Enabled: true, Endpoint: endpoint, Model: model}.Normalized()
	if err := chosen.Validate(); err != nil {
		// The observer is optional, so a typo must not stop the session: say why it
		// was not saved and leave the choice to the settings panel.
		fmt.Fprintf(o.out, "Observer not saved: %v. The session starts without it; /observer opens the settings.\n", err)
		return nil
	}
	if strings.TrimSpace(key) != "" {
		if _, err := o.saveAPIKey(key); err != nil {
			return err
		}
	}
	if err := o.saveObserver(root, chosen.FileSettings()); err != nil {
		return err
	}
	fmt.Fprintln(o.out, "Observer saved. Press Ctrl+E in the dashboard to ask for a report.")
	return nil
}

// observerConfigured answers the question the dashboard's setup panel answers
// from its view: has this human ever configured the observer? It reads the same
// files and the same environment, so the two fronts cannot disagree about
// whether to ask.
func (o *onboarding) observerConfigured() bool {
	if o.keyFileExists() {
		return true
	}
	if enabled, endpoint, model := o.envOverrides(); enabled || endpoint || model {
		return true
	}
	cfg, err := o.loadMergedConfig(o.launchDir)
	return err == nil && cfg.Observer != nil
}

// writeDefaultConfig writes the file the wizard offered. configFile is marshalled
// rather than a literal so the key names cannot drift from what loadConfigFile
// reads. The TUI view is spelled out because the human asked for it by name:
// TIMELINE is already the view Duo opens on, so this key states the default
// rather than changing it.
func (o *onboarding) writeDefaultConfig(path string) error {
	data, err := json.MarshalIndent(configFile{Language: "en", Mode: project.ModeGoal.String(), TUIMode: "timeline"}, "", "  ")
	if err != nil {
		return err
	}
	if err := o.mkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return o.writeFile(path, append(data, '\n'), 0o644)
}

// projectConfigPaths are the files loadMergedConfigFile would read for a project,
// in the same order: the launch directory first, then the repository root.
func projectConfigPaths(launchDir, root string) []string {
	dirs := []string{launchDir}
	if strings.TrimSpace(root) != "" && !workspace.SamePath(root, launchDir) {
		dirs = append(dirs, root)
	}
	out := make([]string, 0, len(dirs)*2)
	for _, dir := range dirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		out = append(out, filepath.Join(dir, ".duo", "config.json"), filepath.Join(dir, ".duo.json"))
	}
	return out
}

func (o *onboarding) hasCommit(ctx context.Context, root string) bool {
	_, err := o.git(ctx, root, "rev-parse", "--verify", "--quiet", "HEAD")
	return err == nil
}

func (o *onboarding) isClean(ctx context.Context, root string) bool {
	out, err := o.git(ctx, root, "status", "--porcelain")
	return err == nil && countPaths(out) == 0
}

// countPaths counts the entries of `git status --porcelain` output.
func countPaths(porcelain string) int {
	trimmed := strings.TrimSpace(porcelain)
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}

// askText reads one free-form answer. A blank answer keeps the offered default,
// which is how the endpoint and model keep the values Duo documents; a question
// without a default (the API key) treats a blank answer as "nothing".
func (o *onboarding) askText(question, fallback string) string {
	if fallback != "" {
		fmt.Fprintf(o.out, "%s [%s]: ", question, fallback)
	} else {
		fmt.Fprintf(o.out, "%s: ", question)
	}
	answer, err := o.readAnswer()
	if err != nil || answer == "" {
		return fallback
	}
	fmt.Fprintln(o.out)
	return answer
}

// askChoice prints a numbered question and returns the 1-based option the human
// picked. Anything else - a blank line, a stray word, a closed terminal - returns
// 0, which every caller treats as "change nothing": a wizard that cannot read the
// human must not decide on their behalf.
func (o *onboarding) askChoice(question string, options []string) int {
	fmt.Fprintln(o.out, question)
	for i, option := range options {
		fmt.Fprintf(o.out, "  %d) %s\n", i+1, option)
	}
	fmt.Fprintf(o.out, "Choose 1-%d: ", len(options))
	answer, err := o.readAnswer()
	if err != nil {
		return 0
	}
	choice, err := strconv.Atoi(answer)
	if err != nil || choice < 1 || choice > len(options) {
		fmt.Fprintln(o.out)
		return 0
	}
	fmt.Fprintln(o.out)
	return choice
}

func (o *onboarding) readAnswer() (string, error) {
	if o.reader == nil {
		o.reader = bufio.NewReader(o.in)
	}
	line, err := o.reader.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// probeShippedDrivers asks the drivers Duo ships whether their agent CLI is on
// PATH, through the same probe the session uses. The names come from the driver
// registry and the binary from each manifest, so this check is right for a
// driver added later without being edited.
func probeShippedDrivers(ctx context.Context) ([]driverStatus, error) {
	registerDrivers()
	names := driver.BuiltinNames()
	out := make([]driverStatus, 0, len(names))
	for _, name := range names {
		status := driverStatus{Name: name}
		caller, err := driver.Resolve(ctx, name, nil, func(error) {})
		if err != nil {
			status.Reason = err.Error()
			out = append(out, status)
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		probe, probeErr := caller.Probe(probeCtx)
		cancel()
		caller.Close()
		switch {
		case probeErr != nil:
			status.Reason = probeErr.Error()
		case probe == nil:
			status.Reason = "the driver returned no probe result"
		case !probe.Available:
			status.Reason = probe.Reason
		default:
			status.CLIPath = probe.AgentPath
		}
		out = append(out, status)
	}
	return out, nil
}
