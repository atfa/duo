package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/workspace"
)

// errDoctorUnhealthy says the report is printed and at least one check failed.
// The report is the output, so the exit code is the only thing left to say.
var errDoctorUnhealthy = errors.New("doctor found problems")

// Check statuses. A warn is a problem the session can run through; a fail is one
// that makes a launch fail, and the exit code says so.
const (
	doctorOK   = "ok"
	doctorWarn = "warn"
	doctorFail = "fail"
	doctorSkip = "skip"
)

// minGitVersion is the oldest git Duo works on: `git worktree` is how every Duo
// session is isolated, and the porcelain Duo reads has only been stable since the
// 2.x line Duo assumes.
var minGitVersion = [2]int{2, 20}

// doctorCheck is one line of the report. Code is stable text, not a formatted
// sentence: it is what an operator searches for, what a launcher can match, and
// what a test asserts on.
type doctorCheck struct {
	Code   string
	Status string
	Detail string
	Fix    string
}

// doctorEnv is what the checks look at. Every external probe is a field, because
// the failures worth diagnosing are the ones a developer's machine does not have:
// a driver CLI that is not installed, a repository with uncommitted work, a config
// file that does not parse.
type doctorEnv struct {
	repository   string
	findRoot     func(ctx context.Context, repository string) (string, error)
	git          func(ctx context.Context, dir string, args ...string) (string, error)
	loadConfig   func(launchDir string) (configFile, error)
	pluginLookup func(string) (string, bool)
	probe        func(ctx context.Context, path string) (driver.ProbeResult, error)
	contract     func(ctx context.Context, path, workDir string) driver.Report
	stat         func(string) (os.FileInfo, error)
	httpGet      func(ctx context.Context, url string) (int, error)
	home         func() string
	workDir      func() string
}

func newDoctorEnv(repository string) *doctorEnv {
	return &doctorEnv{
		repository:   repository,
		findRoot:     workspace.FindRoot,
		git:          gitText,
		loadConfig:   loadMergedConfigFile,
		pluginLookup: driver.Lookup,
		probe:        probePlugin,
		contract:     driver.RunContract,
		stat:         os.Stat,
		httpGet:      httpGetHead,
		home:         func() string { home, _ := os.UserHomeDir(); return home },
		workDir:      os.TempDir,
	}
}

func parseDoctorArgs(args []string) (string, error) {
	repository := ""
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		if helpRequested(arg) {
			return repository, nil
		}
		if strings.HasPrefix(arg, "-") {
			return "", fmt.Errorf("unknown flag %q (usage: %s)", arg, commandUsage("doctor"))
		}
		if repository != "" {
			return "", fmt.Errorf("unexpected argument %q (usage: %s)", arg, commandUsage("doctor"))
		}
		repository = arg
	}
	return repository, nil
}

func runDoctor(ctx context.Context, args []string) error {
	repository, err := parseDoctorArgs(args)
	if err != nil {
		return err
	}
	if helpRequestedFirst(args) {
		fmt.Println(commandUsage("doctor"))
		return nil
	}
	env := newDoctorEnv(repository)
	checks := env.run(ctx)
	printDoctorReport(checks)
	if doctorFailed(checks) {
		return errDoctorUnhealthy
	}
	return nil
}

func helpRequestedFirst(args []string) bool {
	for _, arg := range args {
		if helpRequested(arg) {
			return true
		}
	}
	return false
}

// doctorFailed reports whether any check failed, which is what the exit code says.
func doctorFailed(checks []doctorCheck) bool {
	for _, check := range checks {
		if check.Status == doctorFail {
			return true
		}
	}
	return false
}

func printDoctorReport(checks []doctorCheck) {
	fmt.Println("duo doctor")
	for _, check := range checks {
		fmt.Printf("  %-4s  %-18s %s\n", check.Status, check.Code, check.Detail)
		if check.Fix != "" {
			fmt.Printf("        %-18s %s\n", "", "fix: "+check.Fix)
		}
	}
	fails, warns := 0, 0
	for _, check := range checks {
		switch check.Status {
		case doctorFail:
			fails++
		case doctorWarn:
			warns++
		}
	}
	fmt.Printf("\n%d fail, %d warn\n", fails, warns)
}

// run answers the questions an operator asking "why will not it start" needs:
// is the tooling there, is the repository usable, is the configuration readable,
// and can the result be delivered.
func (e *doctorEnv) run(ctx context.Context) []doctorCheck {
	out := []doctorCheck{e.checkGit(ctx)}

	root, rootCheck := e.checkRepository(ctx)
	out = append(out, rootCheck)
	out = append(out, e.checkDirty(ctx, root))
	out = append(out, e.checkBaseBranch(ctx, root))
	out = append(out, e.checkWorktreeRoot())

	cfg, cfgCheck := e.checkConfig()
	out = append(out, cfgCheck)
	out = append(out, e.checkDrivers(ctx, cfg)...)
	out = append(out, e.checkExtensions(cfg)...)
	out = append(out, e.checkObserver(ctx, cfg))
	out = append(out, e.checkObserverReach(ctx, cfg))
	return out
}

func (e *doctorEnv) checkGit(ctx context.Context) doctorCheck {
	out, err := e.git(ctx, "", "version")
	if err != nil {
		return doctorCheck{
			Code:   "git.present",
			Status: doctorFail,
			Detail: "git is not usable: " + err.Error(),
			Fix:    "install git; every Duo session is isolated with git worktrees",
		}
	}
	major, minor, ok := parseGitVersion(out)
	if !ok {
		return doctorCheck{
			Code:   "git.version",
			Status: doctorWarn,
			Detail: "cannot read the git version from " + strconv.Quote(strings.TrimSpace(out)),
			Fix:    "check that git is the real git and not a wrapper that prints something else",
		}
	}
	if major < minGitVersion[0] || (major == minGitVersion[0] && minor < minGitVersion[1]) {
		return doctorCheck{
			Code:   "git.version",
			Status: doctorFail,
			Detail: fmt.Sprintf("git %d.%d is too old; Duo needs at least %d.%d", major, minor, minGitVersion[0], minGitVersion[1]),
			Fix:    "upgrade git: Duo isolates each agent in a git worktree",
		}
	}
	return doctorCheck{Code: "git.version", Status: doctorOK, Detail: strings.TrimSpace(out)}
}

func parseGitVersion(out string) (int, int, bool) {
	fields := strings.Fields(out)
	if len(fields) < 3 || fields[0] != "git" || fields[1] != "version" {
		return 0, 0, false
	}
	parts := strings.Split(fields[2], ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	digits := parts[1]
	if i := strings.IndexAny(digits, "-+"); i >= 0 {
		digits = digits[:i]
	}
	minor, err := strconv.Atoi(digits)
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// checkRepository resolves the repository Duo would run on. Everything after it
// is answered per repository, so a failure here stops those checks rather than
// reporting a pile of noise about a path that is not a repository.
func (e *doctorEnv) checkRepository(ctx context.Context) (string, doctorCheck) {
	root, err := e.findRoot(ctx, e.repository)
	if err != nil {
		return "", doctorCheck{
			Code:   "repo.git",
			Status: doctorFail,
			Detail: "no git repository at " + displayPath(e.repository) + ": " + err.Error(),
			Fix:    "run duo from inside a git repository (its first-run guide offers `git init`), or name one: duo doctor <path>",
		}
	}
	return root, doctorCheck{Code: "repo.git", Status: doctorOK, Detail: root}
}

func (e *doctorEnv) checkDirty(ctx context.Context, root string) doctorCheck {
	if root == "" {
		return doctorCheck{Code: "repo.dirty", Status: doctorSkip, Detail: "no repository to inspect"}
	}
	out, err := e.git(ctx, root, "status", "--porcelain")
	if err != nil {
		return doctorCheck{
			Code:   "repo.dirty",
			Status: doctorWarn,
			Detail: "git status failed: " + err.Error(),
			Fix:    "check that the repository is readable and that git can write its index",
		}
	}
	if strings.TrimSpace(out) == "" {
		return doctorCheck{Code: "repo.dirty", Status: doctorOK, Detail: "working tree is clean"}
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return doctorCheck{
		Code:   "repo.dirty",
		Status: doctorFail,
		Detail: fmt.Sprintf("%d uncommitted path(s) in %s", len(lines), root),
		Fix:    "commit or stash them: Duo delivers its result into this working tree and will not overwrite uncommitted work",
	}
}

// checkBaseBranch answers whether a finished session has anywhere to be delivered
// to. A base ref that does not resolve is the failure that costs the most: the
// work is done, signed, and cannot be handed over.
func (e *doctorEnv) checkBaseBranch(ctx context.Context, root string) doctorCheck {
	if root == "" {
		return doctorCheck{Code: "base.branch", Status: doctorSkip, Detail: "no repository to inspect"}
	}
	branch, err := e.git(ctx, root, "rev-parse", "--abbrev-ref", "HEAD")
	branch = strings.TrimSpace(branch)
	if err != nil || branch == "" || branch == "HEAD" {
		return doctorCheck{
			Code:   "base.branch",
			Status: doctorFail,
			Detail: "the repository is on a detached HEAD, so a finished session has no branch to deliver to",
			Fix:    "check out a branch before starting Duo",
		}
	}
	if _, err := e.git(ctx, root, "rev-parse", "--verify", "refs/heads/"+branch); err != nil {
		return doctorCheck{
			Code:   "base.branch",
			Status: doctorFail,
			Detail: "the current branch " + branch + " does not resolve to a commit",
			Fix:    "check the branch out, or start Duo with the branch that should receive the result",
		}
	}
	return doctorCheck{Code: "base.branch", Status: doctorOK, Detail: "results deliver to " + branch}
}

func (e *doctorEnv) checkWorktreeRoot() doctorCheck {
	home := e.home()
	if home == "" {
		return doctorCheck{
			Code:   "worktree.root",
			Status: doctorFail,
			Detail: "the home directory is unknown, so Duo has nowhere to create agent worktrees",
			Fix:    "set HOME",
		}
	}
	root := filepath.Join(home, ".duo", "worktrees")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return doctorCheck{
			Code:   "worktree.root",
			Status: doctorFail,
			Detail: root + " is not creatable: " + err.Error(),
			Fix:    "make the directory writable, or set DUO_WORKTREE_ROOT somewhere writable",
		}
	}
	probe := filepath.Join(root, ".duo-doctor-write")
	if err := os.WriteFile(probe, []byte("x"), 0o644); err != nil {
		return doctorCheck{
			Code:   "worktree.root",
			Status: doctorFail,
			Detail: root + " is not writable: " + err.Error(),
			Fix:    "make the directory writable, or set DUO_WORKTREE_ROOT somewhere writable",
		}
	}
	_ = os.Remove(probe)
	return doctorCheck{Code: "worktree.root", Status: doctorOK, Detail: root}
}

// checkConfig reads the same merged file a session starts from, so a diagnosis
// about the configuration is about the configuration Duo will actually use.
func (e *doctorEnv) checkConfig() (configFile, doctorCheck) {
	cfg, err := e.loadConfig(e.repository)
	if err != nil {
		return configFile{}, doctorCheck{
			Code:   "config.file",
			Status: doctorFail,
			Detail: err.Error(),
			Fix:    "fix ~/.duo/config.json or .duo/config.json: a session cannot start with a file Duo cannot read",
		}
	}
	// The interface view is validated where a launch resolves it, so a value it
	// cannot draw has to fail here too: a report that says the file parses is not
	// the answer to "why will not it start".
	if _, err := resolveTUIMode(cfg.TUIMode); err != nil {
		return configFile{}, doctorCheck{
			Code:   "config.file",
			Status: doctorFail,
			Detail: err.Error(),
			Fix:    "fix tuiMode in ~/.duo/config.json or .duo/config.json: it must be timeline or split",
		}
	}
	return cfg, doctorCheck{Code: "config.file", Status: doctorOK, Detail: "the user and project configuration files parse"}
}

// configuredDrivers names the drivers the two agents would launch, deduplicated:
// a session that runs the same driver on both sides is checked once.
func (e *doctorEnv) configuredDrivers(cfg configFile) []string {
	names := []string{}
	seen := map[string]bool{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
	}
	for _, agent := range []string{"austin", "tony"} {
		add(cfg.Agents[agent].Driver)
	}
	add(cfg.Driver)
	add(defaultDriver)
	return names
}

func (e *doctorEnv) checkDrivers(ctx context.Context, cfg configFile) []doctorCheck {
	var out []doctorCheck
	for _, name := range e.configuredDrivers(cfg) {
		path, found := e.pluginLookup(name)
		if !found {
			out = append(out, doctorCheck{
				Code:   "driver.plugin",
				Status: doctorFail,
				Detail: fmt.Sprintf("no Driver Plugin named %q", name),
				Fix:    fmt.Sprintf("install duo-plugin-%s into %s (see docs/plugin-development-guide.md)", name, driver.PluginDir()),
			})
			continue
		}
		out = append(out, doctorCheck{Code: "driver.plugin", Status: doctorOK, Detail: fmt.Sprintf("%s: %s", name, path)})

		probe, err := e.probe(ctx, path)
		switch {
		case err != nil:
			out = append(out, doctorCheck{
				Code:   "driver.cli",
				Status: doctorFail,
				Detail: fmt.Sprintf("the %s plugin did not answer probe: %v", name, err),
				Fix:    fmt.Sprintf("run `duo plugins test %s -v` and read the protocol section of the report", name),
			})
		case !probe.Available:
			out = append(out, doctorCheck{
				Code:   "driver.cli",
				Status: doctorFail,
				Detail: fmt.Sprintf("%s: %s", name, probe.Reason),
				Fix:    fmt.Sprintf("install the %s CLI, or point agents.austin.command / agents.tony.command at it", name),
			})
		default:
			out = append(out, doctorCheck{Code: "driver.cli", Status: doctorOK, Detail: fmt.Sprintf("%s: %s", name, probe.AgentPath)})
		}

		report := e.contract(ctx, path, e.workDir())
		if report.Passed() {
			out = append(out, doctorCheck{
				Code:   "driver.contract",
				Status: doctorOK,
				Detail: fmt.Sprintf("%s passed the protocol contract", name),
			})
			continue
		}
		out = append(out, doctorCheck{
			Code:   "driver.contract",
			Status: doctorFail,
			Detail: fmt.Sprintf("%s failed the protocol contract: %s", name, strings.Join(failedChecks(report), ", ")),
			Fix:    fmt.Sprintf("run `duo plugins test %s -v` for the failing checks and their causes", name),
		})
	}
	return out
}

func failedChecks(report driver.Report) []string {
	var out []string
	for _, result := range report.Results {
		if !result.OK && !result.Optional {
			out = append(out, result.Name)
		}
	}
	return out
}

// extensionDirs are where the installer scripts put the Agent Adapter each driver
// needs. The driver is a separate binary and installs cleanly; the adapter runs
// inside the agent, and a missing one is the failure that shows up as an agent
// that starts, answers nothing, and never attaches to the bridge.
func (e *doctorEnv) checkExtensions(cfg configFile) []doctorCheck {
	home := e.home()
	if home == "" {
		return []doctorCheck{{
			Code:   "extension.pi",
			Status: doctorSkip,
			Detail: "the home directory is unknown",
		}}
	}
	drivers := strings.Join(e.configuredDrivers(cfg), " ")
	check := func(code, driverName, dir string) doctorCheck {
		if !strings.Contains(drivers, driverName) {
			return doctorCheck{Code: code, Status: doctorSkip, Detail: "the " + driverName + " driver is not configured"}
		}
		if info, err := e.stat(dir); err != nil || !info.IsDir() {
			return doctorCheck{
				Code:   code,
				Status: doctorFail,
				Detail: "the Duo " + driverName + " bridge is not installed at " + dir,
				Fix:    "run scripts/install-" + driverName + "-extension.sh, then restart any running " + driverName + " process",
			}
		}
		return doctorCheck{Code: code, Status: doctorOK, Detail: dir}
	}
	return []doctorCheck{
		check("extension.pi", "pi", filepath.Join(home, ".pi", "agent", "extensions", "duo")),
		check("extension.opencode", "opencode", filepath.Join(home, ".config", "opencode", "plugin", "duo")),
	}
}

func (e *doctorEnv) checkObserver(ctx context.Context, cfg configFile) doctorCheck {
	if cfg.Observer == nil {
		return doctorCheck{Code: "observer.config", Status: doctorSkip, Detail: "no observer is configured"}
	}
	settings := cfg.Observer.Config()
	if !settings.Enabled {
		return doctorCheck{Code: "observer.config", Status: doctorSkip, Detail: "the observer is switched off"}
	}
	if err := settings.Validate(); err != nil {
		return doctorCheck{
			Code:   "observer.config",
			Status: doctorFail,
			Detail: err.Error(),
			Fix:    "fix the observer section of the configuration, or switch it off",
		}
	}
	return doctorCheck{Code: "observer.config", Status: doctorOK, Detail: settings.Endpoint}
}

func (e *doctorEnv) checkObserverReach(ctx context.Context, cfg configFile) doctorCheck {
	if cfg.Observer == nil || !cfg.Observer.Config().Enabled {
		return doctorCheck{Code: "observer.reach", Status: doctorSkip, Detail: "the observer is not in use"}
	}
	endpoint := cfg.Observer.Config().Endpoint
	status, err := e.httpGet(ctx, endpoint)
	if err != nil {
		return doctorCheck{
			Code:   "observer.reach",
			Status: doctorWarn,
			Detail: endpoint + " did not answer: " + err.Error(),
			Fix:    "the session runs without the observer; check the endpoint and DUO_OBSERVER_API_KEY",
		}
	}
	// Any HTTP answer proves the endpoint is reachable. The observer posts to
	// <endpoint>/chat/completions, so a 404 on the base URL is the expected answer
	// from a gateway that speaks OpenAI paths, and calling it unreachable would
	// send the operator looking for a problem that is not there.
	return doctorCheck{Code: "observer.reach", Status: doctorOK, Detail: fmt.Sprintf("%s answered %d", endpoint, status)}
}

// gitText, probePlugin and httpGetHead are the real probes behind the doctorEnv
// fields.

func gitText(ctx context.Context, dir string, args ...string) (string, error) {
	cmdArgs := args
	if strings.TrimSpace(dir) != "" {
		cmdArgs = append([]string{"-C", dir}, args...)
	}
	out, err := exec.CommandContext(ctx, "git", cmdArgs...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func probePlugin(ctx context.Context, path string) (driver.ProbeResult, error) {
	client, err := driver.StartWithTimeout(ctx, filepath.Base(path), path, nil, nil, 10*time.Second)
	if err != nil {
		return driver.ProbeResult{}, err
	}
	defer client.Close()
	probe, err := client.Probe(ctx)
	if err != nil {
		return driver.ProbeResult{}, err
	}
	return *probe, nil
}

func httpGetHead(ctx context.Context, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// displayPath keeps a report line readable when the operator named nothing.
func displayPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return "the current directory"
	}
	return path
}
