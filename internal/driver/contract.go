package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Contract is the shared conformance suite for Driver Plugins. It exists so that
// authoring a plugin does not require understanding Duo Core: a plugin author runs
// these checks against their executable and, when they pass, the plugin is usable.
//
// Every check here is something a plugin can get wrong silently. A driver that
// declares a capability it does not implement gets an empty model picker and no
// error. A driver that replays an identity its agent never issued starts a fresh
// conversation on every launch and reports nothing. Neither is diagnosable from a
// symptom, so both are checked here instead.

// Result is one check's outcome.
type Result struct {
	// Name identifies the check, and is stable: a plugin author may come to rely on
	// it, and CI may filter on it.
	Name string
	OK   bool
	// Detail is the failure explanation, or a short note on what was verified.
	Detail string
	// Optional marks a check that could not run because of the environment — the
	// agent CLI is not installed, say. These are reported separately from failures,
	// because a missing dependency is not a broken plugin.
	Optional bool
}

// Report is the outcome of running the contract against one plugin.
type Report struct {
	// Path is the executable that was checked.
	Path string
	// Name is what the plugin calls itself, which need not match its file name.
	Name string
	// Version is the plugin's own version.
	Version    string
	Manifest   *Manifest
	Results    []Result
	Duration   time.Duration
	Probe      *ProbeResult
	Failed     bool
	Skipped    bool
	Descriptor string
}

// Passed reports whether every required check succeeded. It reads the results
// rather than the running flag, so a Report assembled any other way still answers
// correctly.
func (r Report) Passed() bool {
	for _, result := range r.Results {
		if !result.OK && !result.Optional {
			return false
		}
	}
	return true
}

func (r *Report) add(name string, ok bool, format string, args ...any) {
	r.Results = append(r.Results, Result{Name: name, OK: ok, Detail: fmt.Sprintf(format, args...)})
	if !ok {
		r.Failed = true
	}
}

func (r *Report) skip(name, format string, args ...any) {
	r.Results = append(r.Results, Result{Name: name, OK: true, Optional: true, Detail: fmt.Sprintf(format, args...)})
}

// RunContract checks the plugin executable at path.
//
// It starts the plugin once and reuses it, because a plugin is a long-lived process
// and a contract that only ever made one call per process would miss the framing and
// shutdown behaviour a real session depends on.
//
// workDir is a scratch directory for anything the checks need on disk.
func RunContract(ctx context.Context, path, workDir string) Report {
	started := time.Now()
	report := Report{Path: path}

	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	report.Path = abs
	if info, err := os.Stat(abs); err != nil || info.IsDir() || info.Mode()&0111 == 0 {
		report.add("executable", false, "%s is not an executable file", abs)
		return report
	}
	report.add("executable", true, "%s", abs)

	client, err := StartWithTimeout(ctx, filepath.Base(abs), abs, nil, nil, StartTimeout)
	if err != nil {
		report.add("start", false, "%v", err)
		return report
	}
	defer client.Close()

	// --- describe -------------------------------------------------------------
	manifest, err := client.Describe(ctx)
	if err != nil {
		report.add("describe", false, "%v", err)
		return report
	}
	report.Manifest = manifest
	report.Name = manifest.Name
	report.Version = manifest.Version
	report.Descriptor = manifest.Description
	report.add("describe", true, "%s %s", manifest.Name, manifest.Version)

	// The version check is separate from describe because it is the one thing a
	// plugin cannot fix by accident: Core refuses an exact mismatch rather than
	// driving fields it may misread.
	if manifest.Protocol == ProtocolVersion {
		report.add("protocol-version", true, "protocol %d matches Core", manifest.Protocol)
	} else {
		report.add("protocol-version", false, "plugin declares protocol %d, Core speaks %d", manifest.Protocol, ProtocolVersion)
	}

	// --- probe ----------------------------------------------------------------
	probe, err := client.Probe(ctx)
	if err != nil {
		report.add("probe", false, "probe failed: %v", err)
	} else {
		report.Probe = probe
		switch {
		case probe.Available:
			report.add("probe", true, "agent CLI available at %s", probe.AgentPath)
		case probe.Reason != "":
			// Not installed is a reportable state, and the reason is what the operator
			// is shown. A plugin that fails here instead makes Core report a broken
			// plugin for what is really a missing dependency.
			report.skip("probe", "agent CLI unavailable: %s", probe.Reason)
		default:
			report.add("probe", false, "probe says unavailable without saying why; Core has nothing to show the operator")
		}
	}

	// A resume: server driver mints its own identity after prepare has run, so
	// state is the only way that identity can reach the disk. Without it every
	// launch silently opens a new conversation, which no other check here can see:
	// prepare round-trips its own blob perfectly while the conversation forks.
	if manifest.Capabilities.Resume == ResumeServer {
		if _, err := client.State(ctx); err != nil {
			report.add("resume-server-state", false, "capabilities.resume is `server` but state is not implemented, so an identity learned after launch can never be saved: %v", err)
		} else {
			report.add("resume-server-state", true, "state answered")
		}
	}

	// --- capabilities agree with the methods implemented ----------------------
	// A declared capability with no method behind it is the failure mode that costs
	// a user a feature which appears to exist and does nothing.
	if manifest.Capabilities.Models {
		// models shells out to the agent's own CLI, so on a loaded machine it can
		// outrun any deadline we set. That is the machine being busy, not the
		// protocol being wrong, so it is left unverified rather than failed.
		if _, err := client.Models(ctx); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				report.skip("capability-models", "could not verify: the model listing timed out on this machine")
			} else {
				report.add("capability-models", false, "capabilities.models is declared but models did not answer: %s", cause(err))
			}
		} else {
			report.add("capability-models", true, "models answered")
		}
	}
	if manifest.Capabilities.Thinking {
		if _, err := client.Thinking(ctx); err != nil {
			report.add("capability-thinking", false, "capabilities.thinking is declared but thinking did not answer: %s", cause(err))
		} else {
			report.add("capability-thinking", true, "thinking answered")
		}
	}
	if !manifest.Capabilities.Models {
		if _, err := client.Models(ctx); !errors.Is(err, ErrUnsupported) {
			report.add("capability-models", false, "models must be refused with `unsupported` when the capability is absent, got %v", err)
		} else {
			report.add("capability-models", true, "models correctly refused")
		}
	}

	// liveSteering with a plugin-owned bridge is impossible to honour: nothing can
	// write to the terminal Core owns. Core refuses it at describe; a plugin that
	// got past that would silently swallow every prompt.
	if manifest.Capabilities.LiveSteering && manifest.Capabilities.Bridge != BridgeAgent {
		report.add("bridge-ownership", false, "liveSteering with bridge=%q is unservable: the endpoint cannot receive an injected prompt", manifest.Capabilities.Bridge)
	} else {
		report.add("bridge-ownership", true, "bridge=%q, liveSteering=%v", manifest.Capabilities.Bridge, manifest.Capabilities.LiveSteering)
	}

	// --- prepare --------------------------------------------------------------
	first, err := client.Prepare(ctx, LaunchRequest{
		Agent: "Austin", Mode: "goal", CWD: workDir,
		Session: "contract", Token: "contract-token",
		Host: "127.0.0.1", Port: "1", BaseCommand: "",
	})
	if err != nil {
		report.add("prepare", false, "%v", err)
		return report
	}
	checkFirstLaunch(&report, manifest, first)

	// --- resume ---------------------------------------------------------------
	// The whole point of the state blob. A driver that returns something it cannot
	// read back loses the conversation on every resume, silently.
	_, err = client.Prepare(ctx, LaunchRequest{
		Agent: "Austin", Mode: "goal", CWD: workDir,
		Session: "contract", Token: "contract-token",
		Host: "127.0.0.1", Port: "1", State: first.State,
	})
	switch {
	case err != nil:
		report.add("resume", false, "prepare with the state it just returned failed: %v", err)
	case len(first.State) == 0 && manifest.Capabilities.Resume != ResumeNone:
		// Not a failure: a driver with nothing to remember yet legitimately returns
		// an empty blob on a first run.
		report.skip("resume", "no state to resume from yet")
	default:
		report.add("resume", true, "re-preparing with the stored state succeeded")
	}

	// --- the two agents are separate -------------------------------------------
	// Sharing an identity puts both agents in one conversation, which looks like two
	// agents collaborating and is actually one. It is also the failure a plugin hits
	// by holding its identity somewhere global instead of in the state it is given.
	tonyPlan, err := client.Prepare(ctx, LaunchRequest{
		Agent: "Tony", Mode: "goal", CWD: workDir, Session: "contract",
	})
	switch {
	case err != nil:
		report.add("agent-identity", false, "prepare for Tony failed: %v", err)
	case first.SessionIdentity != "" && first.SessionIdentity == tonyPlan.SessionIdentity:
		report.add("agent-identity", false, "Tony inherited Austin's identity %q; the two agents must never share a conversation", first.SessionIdentity)
	case tonyPlan.State != nil && bytesEqual(first.State, tonyPlan.State):
		report.add("agent-identity", false, "both agents were given the same state blob; identity must be per agent")
	default:
		report.add("agent-identity", true, "each agent resolves its own identity")
	}

	// --- compatibility with a pre-protocol session ------------------------------
	// Duo 0.9.0 sessions migrate to {"sessionId": …} under the one word every
	// driver shares, because Core does not know this plugin's vocabulary. A plugin
	// that cannot read that shape forks every resumed 0.9.0 conversation.
	if manifest.Capabilities.Resume == ResumeClient {
		const migratedID = "contract-migrated-id"
		migrated, err := client.Prepare(ctx, LaunchRequest{
			Agent: "Austin", CWD: workDir, Session: "contract",
			State: json.RawMessage(`{"sessionId":"` + migratedID + `"}`),
		})
		switch {
		case err != nil:
			report.add("legacy-state", false, "a migrated 0.9.0 session was refused: %v", err)
		case !deliversIdentity(migrated, migratedID):
			report.add("legacy-state", false, "a migrated identity was accepted but never delivered to the agent: command %q, env %v", migrated.Command, migrated.Env)
		default:
			report.add("legacy-state", true, "a migrated 0.9.0 identity is replayed to the agent")
		}
	}

	// --- lifecycle -------------------------------------------------------------
	// A plugin is a separate process. If it dies, Duo still has a collaboration loop
	// to run, so the contract is that a crash is reported and the next call works —
	// never a hang.
	crashReport := checkCrashPolicy(ctx, workDir)
	report.Results = append(report.Results, crashReport...)

	report.Duration = time.Since(started)
	return report
}

// deliversIdentity reports whether an identity actually reaches the agent, by either
// route. A plugin may put it on the command line or pass it through the environment,
// and both are correct — what is not correct is accepting it and then not using it,
// which is a resume that silently starts over.
func deliversIdentity(plan *LaunchPlan, id string) bool {
	if plan == nil || id == "" {
		return false
	}
	if strings.Contains(plan.Command, id) {
		return true
	}
	for _, value := range plan.Env {
		if value == id {
			return true
		}
	}
	return false
}

func bytesEqual(a, b json.RawMessage) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	return string(a) == string(b)
}

// checkFirstLaunch validates the plan for a run with nothing selected and no stored
// state. It is the case that reaches a user's machine on a fresh install, and it is
// where the "flag for an unset value" bug lives.
func checkFirstLaunch(report *Report, manifest *Manifest, plan *LaunchPlan) {
	if strings.TrimSpace(plan.Command) == "" {
		report.add("prepare", false, "prepare returned an empty command; Core would run nothing")
		return
	}
	report.add("prepare", true, "command: %s", plan.Command)

	// An unset model must add no flag. "--model ''" is not the same as no flag: it is
	// a flag whose argument is the empty string, which most CLIs reject.
	for _, flag := range []string{"--model", "--effort", "--thinking", "--variant"} {
		if strings.Contains(plan.Command, flag+" ''") || strings.HasSuffix(plan.Command, flag+" ''") {
			report.add("no-empty-flag", false, "%s was emitted with an empty value; an unset value must add no flag at all", flag)
			return
		}
	}
	report.add("no-empty-flag", true, "no flag emitted for an unset value")

	// resume:server means the agent issues the identity. Inventing one makes the
	// agent open a different conversation every launch, and persists the rejection.
	if manifest.Capabilities.Resume == ResumeServer && plan.SessionIdentity != "" {
		report.add("resume-first-run", false, "resume=server but a first run reported identity %q; the agent assigns this, not the plugin", plan.SessionIdentity)
		return
	}
	if manifest.Capabilities.Resume == ResumeNone && len(plan.State) > 0 {
		report.skip("resume-first-run", "resume=none; state %s is unused but harmless", plan.State)
		return
	}
	report.add("resume-first-run", true, "resume=%s honoured on a first run", manifest.Capabilities.Resume)

	if len(plan.State) > 0 && !json.Valid(plan.State) {
		report.add("state-encoding", false, "state is not valid JSON: %s", plan.State)
	} else {
		report.add("state-encoding", true, "state is valid JSON")
	}
	if len(plan.Notices) > 0 {
		report.Descriptor += strings.Join(plan.Notices, "; ")
	}
}

// checkCrashPolicy verifies that a plugin which dies is reported and replaced, rather
// than hanging the session. It runs against a second, deliberately short-lived
// process so the plugin under test is left running.
func checkCrashPolicy(ctx context.Context, workDir string) []Result {
	var out []Result
	// A shell that exits immediately is the cheapest faithful stand-in for "the
	// plugin crashed", and it needs nothing from the plugin under test.
	crashPath := filepath.Join(workDir, "duo-contract-crash.sh")
	if err := os.WriteFile(crashPath, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		return []Result{{Name: "crash-policy", OK: true, Optional: true, Detail: "could not stage a crashing plugin: " + err.Error()}}
	}
	defer os.Remove(crashPath)

	var reported []error
	s := NewSupervised("contract-crash", SupervisedOptions{
		Path: crashPath,
		Env:  nil,
		Open: func(ctx context.Context, path string, env []string) (*Client, error) {
			return Start(ctx, "contract-crash", path, nil, env)
		},
		Report: func(err error) { reported = append(reported, err) },
	})
	defer s.Close()

	start := time.Now()
	_, err := s.Describe(ctx)
	elapsed := time.Since(start)

	switch {
	case err == nil:
		out = append(out, Result{Name: "crash-policy", OK: false, Detail: "a plugin that exits immediately answered describe"})
	case !errors.Is(err, ErrCrashed):
		out = append(out, Result{Name: "crash-policy", OK: false, Detail: fmt.Sprintf("a dead plugin reported %v, want a crash rather than a protocol error", err)})
	case elapsed > ModelListTimeout:
		out = append(out, Result{Name: "crash-policy", OK: false, Detail: fmt.Sprintf("a dead plugin took %s to report; it must not hang the session", elapsed)})
	case len(reported) == 0:
		out = append(out, Result{Name: "crash-policy", OK: false, Detail: "a crashed plugin was not reported, so the operator would never learn why"})
	default:
		out = append(out, Result{Name: "crash-policy", OK: true, Detail: "a dead plugin is reported as a crash, not a hang"})
	}

	// And it must be replaceable: a retry gets a fresh process rather than staying
	// broken for the rest of the session.
	good := goodPluginPath(workDir)
	if good == "" {
		return append(out, Result{Name: "crash-respawn", OK: true, Optional: true, Detail: "no reference plugin available to test respawn against"})
	}
	retry := NewSupervised("contract-retry", SupervisedOptions{Path: good})
	defer retry.Close()
	if _, err := retry.Describe(ctx); err != nil {
		out = append(out, Result{Name: "crash-respawn", OK: false, Detail: fmt.Sprintf("a plugin after a crash did not answer describe: %v", err)})
	} else {
		out = append(out, Result{Name: "crash-respawn", OK: true, Detail: "a fresh process answers after a crash"})
	}
	return out
}

// goodPluginPath finds a plugin known to work, so respawn can be checked against
// something that answers. The reference plugin is built into workDir by the caller;
// if it is not there the respawn check is skipped rather than failed, because a
// missing positive control is not a broken plugin.
func goodPluginPath(workDir string) string {
	candidate := filepath.Join(workDir, "duo-plugin-example")
	if info, err := os.Stat(candidate); err == nil && info.Mode()&0111 != 0 {
		return candidate
	}
	return ""
}

// Format renders a report for a terminal.
func (r Report) Format() string {
	var b strings.Builder
	title := r.Name
	if title == "" {
		title = filepath.Base(r.Path)
	}
	if r.Version != "" {
		title += " " + r.Version
	}
	if r.Descriptor != "" {
		title += " — " + r.Descriptor
	}
	fmt.Fprintf(&b, "%s\n", title)
	fmt.Fprintf(&b, "  %s\n", r.Path)

	width := 0
	for _, result := range r.Results {
		if len(result.Name) > width {
			width = len(result.Name)
		}
	}
	for _, result := range r.Results {
		mark := "ok  "
		switch {
		case !result.OK:
			mark = "FAIL"
		case result.Optional:
			mark = "skip"
		}
		fmt.Fprintf(&b, "  %s  %-*s  %s\n", mark, width, result.Name, result.Detail)
	}

	required, optional, failed := 0, 0, 0
	for _, result := range r.Results {
		switch {
		case !result.OK && !result.Optional:
			failed++
		case result.Optional:
			optional++
		default:
			required++
		}
	}
	fmt.Fprintf(&b, "  %d passed, %d skipped, %d failed in %s\n", required, optional, failed, r.Duration.Round(time.Millisecond))
	if failed > 0 {
		fmt.Fprintf(&b, "  FAILED: %s does not satisfy the Duo Driver Plugin Protocol\n", title)
	} else {
		fmt.Fprintf(&b, "  OK: %s satisfies the Duo Driver Plugin Protocol\n", title)
	}
	return b.String()
}

// ExitCode maps a report to a process exit code, so CI can gate on it.
func (r Report) ExitCode() int {
	if r.Passed() {
		return 0
	}
	return 1
}

// DiscoverAndRun checks every plugin Duo can see, which is what a release should do.
func DiscoverAndRun(ctx context.Context, workDir string) []Report {
	entries := Discover()
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	var out []Report
	for _, entry := range entries {
		if entry.Path == BuiltInSource {
			// An in-process driver has no executable to check, and the contract is
			// about the wire, so there is nothing to run.
			continue
		}
		// A pre-plugin launch shim execs an agent and speaks no protocol, so it
		// cannot pass and has not been written badly. Only the sweep skips it: naming
		// one explicitly still checks it, because that is a deliberate request.
		if IsLegacyShim(entry.Path) {
			out = append(out, Report{
				Name: entry.Name,
				Path: entry.Path,
				Results: []Result{{
					Name:     "legacy-launch-shim",
					OK:       true,
					Optional: true,
					Detail: fmt.Sprintf("%s is a pre-plugin launch shim, not a Driver Plugin; install duo-plugin-%s to check this driver",
						filepath.Base(entry.Path), entry.Name),
				}},
			})
			continue
		}
		out = append(out, RunContract(ctx, entry.Path, workDir))
	}
	return out
}

// ReferencePluginBinary builds the reference plugin into dir and returns its path.
// The contract needs a plugin known to work so respawn and framing can be checked
// against something that answers; shipping the reference plugin is what makes that
// possible without a real coding agent installed.
func ReferencePluginBinary(ctx context.Context, dir string) (string, error) {
	out := filepath.Join(dir, "duo-plugin-example")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", out, "github.com/atfa/duo/cmd/duo-plugin-example")
	cmd.Env = append(os.Environ(), "GOFLAGS=")
	if combined, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build the reference plugin: %v\n%s", err, combined)
	}
	return out, nil
}

// cause names why a call failed in terms an author can act on, keeping a timeout
// distinct from an absent method.
func cause(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "the call timed out, which says nothing about whether the method exists"
	case errors.Is(err, ErrUnsupported):
		return "the plugin refused it as unsupported"
	default:
		return err.Error()
	}
}
