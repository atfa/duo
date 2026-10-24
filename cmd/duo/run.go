package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
)

// Exit codes of `duo run`: a script needs to tell a delivered artifact from one
// that is still pending, and both from a round the timeout stopped.
const (
	runExitDelivered = 0
	runExitRefused   = 3
	runExitTimeout   = 124
)

var (
	// errRunRefused reports that the round finished without delivering: the
	// artifact is pending on a human, or the session stopped before DONE.
	errRunRefused = errors.New("duo run finished without delivering")
	// errRunTimeout reports that the round was stopped by its own timeout.
	errRunTimeout = errors.New("duo run timed out")
)

// runPollInterval is how often a headless round re-reads the session view.
const runPollInterval = 250 * time.Millisecond

type runArgs struct {
	task    string
	mode    project.Mode
	modeSet bool
	json    bool
	timeout time.Duration
}

func parseRunArgs(args []string) (runArgs, error) {
	var out runArgs
	var words []string
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "":
		case helpRequested(arg):
			out.task = ""
			return runArgs{}, errUsageRequested
		case arg == "--json":
			out.json = true
		case arg == "-m" || arg == "--mode":
			if i+1 >= len(args) {
				return runArgs{}, fmt.Errorf("%s needs a mode (usage: %s)", arg, commandUsage("run"))
			}
			i++
			mode, err := project.ParseMode(args[i])
			if err != nil {
				return runArgs{}, err
			}
			out.mode, out.modeSet = mode, true
		case strings.HasPrefix(arg, "--mode="):
			mode, err := project.ParseMode(strings.TrimPrefix(arg, "--mode="))
			if err != nil {
				return runArgs{}, err
			}
			out.mode, out.modeSet = mode, true
		case arg == "--timeout":
			if i+1 >= len(args) {
				return runArgs{}, fmt.Errorf("--timeout needs a duration, for example 90m (usage: %s)", commandUsage("run"))
			}
			i++
			d, err := time.ParseDuration(args[i])
			if err != nil || d <= 0 {
				return runArgs{}, fmt.Errorf("--timeout %q is not a positive duration, for example 90m", args[i])
			}
			out.timeout = d
		case strings.HasPrefix(arg, "--timeout="):
			value := strings.TrimPrefix(arg, "--timeout=")
			d, err := time.ParseDuration(value)
			if err != nil || d <= 0 {
				return runArgs{}, fmt.Errorf("--timeout %q is not a positive duration, for example 90m", value)
			}
			out.timeout = d
		case strings.HasPrefix(arg, "-"):
			return runArgs{}, fmt.Errorf("unknown flag %q (usage: %s)", arg, commandUsage("run"))
		default:
			words = append(words, arg)
		}
	}
	out.task = strings.Join(words, " ")
	if strings.TrimSpace(out.task) == "" {
		return runArgs{}, fmt.Errorf("duo run needs a task (usage: %s)", commandUsage("run"))
	}
	return out, nil
}

// roundSession is the part of a session a headless round drives. It is an
// interface so the waiting and the exit codes can be tested without starting
// agents, while production passes the real *session.Service.
type roundSession interface {
	View() session.View
	Do(ctx context.Context, cmd session.Command) error
}

// runStop is why a headless round stopped waiting.
type runStop int

const (
	runStopDone runStop = iota
	runStopRestart
	runStopNewSession
	runStopCanceled
	runStopTimeout
)

// runExitCode maps how a round ended to the process exit code: 0 when the
// artifact was delivered, 3 when it was not, and 124 when the timeout stopped it.
func runExitCode(stop runStop, delivered bool) int {
	switch {
	case stop == runStopTimeout:
		return runExitTimeout
	case stop == runStopDone && delivered:
		return runExitDelivered
	default:
		return runExitRefused
	}
}

// runHeadless implements `duo run -m <mode> "task" [--json] [--timeout <d>]`: one
// full round without a terminal, ending when the session reaches DONE or the
// timeout stops it. The session is closed either way, so its record can be
// resumed.
func runHeadless(ctx context.Context, args []string) error {
	parsed, err := parseRunArgs(args)
	if errors.Is(err, errUsageRequested) {
		printUsage()
		return nil
	}
	if err != nil {
		return err
	}

	cfg, err := loadConfig(nil)
	if err != nil {
		return err
	}
	if parsed.modeSet {
		cfg.mode = parsed.mode
		cfg.modeRaw = parsed.mode.String()
		cfg.modeSource = "--mode"
		cfg.modeExplicit = true
	}
	if err := cfg.validate(); err != nil {
		return err
	}

	svc := session.New(cfg.sessionOptions())
	result, err := svc.Start(ctx)
	if err != nil {
		return err
	}
	// A session that is already finished needs no round: the artifact is in the
	// repository, or delivery is pending and only a human can unblock it.
	if result.Finished {
		stop := runStopDone
		view := svc.View()
		svc.Close()
		return reportRun(os.Stdout, view, stop, parsed.json)
	}

	view, stop, err := runRound(ctx, svc, parsed.task, parsed.timeout)
	end := svc.Close()
	if err != nil {
		return err
	}
	if stop == runStopTimeout {
		fmt.Printf("Duo run timed out after %s. Session %s was preserved; resume it with `duo resume %s`.\n",
			parsed.timeout, end.SessionID, end.SessionID)
	}
	return reportRun(os.Stdout, view, stop, parsed.json)
}

// runRound submits the task and waits for the round to end. It is the whole
// headless loop: no terminal is opened and nothing is drawn.
func runRound(ctx context.Context, svc roundSession, task string, timeout time.Duration) (session.View, runStop, error) {
	if err := svc.Do(ctx, session.SubmitTask{Text: task}); err != nil {
		return svc.View(), runStopCanceled, err
	}

	var deadline <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		deadline = timer.C
	}
	ticker := time.NewTicker(runPollInterval)
	defer ticker.Stop()

	for {
		view := svc.View()
		switch {
		case view.Project.Phase == project.PhaseDone:
			return view, runStopDone, nil
		case view.Restart.Reason != "":
			return view, runStopRestart, nil
		case view.NewSession:
			return view, runStopNewSession, nil
		}
		select {
		case <-ctx.Done():
			return view, runStopCanceled, nil
		case <-deadline:
			return view, runStopTimeout, nil
		case <-ticker.C:
		}
	}
}

// reportRun prints how the round ended and returns the sentinel that carries the
// exit code: a delivered artifact is a success, anything else is not.
func reportRun(out io.Writer, view session.View, stop runStop, asJSON bool) error {
	delivered := view.Project.Phase == project.PhaseDone && view.Delivery.Applied()
	code := runExitCode(stop, delivered)

	if asJSON {
		report := struct {
			SessionID string                `json:"sessionId"`
			Mode      string                `json:"mode"`
			Phase     string                `json:"phase"`
			Delivered bool                  `json:"delivered"`
			Delivery  sessionstore.Delivery `json:"delivery"`
			ExitCode  int                   `json:"exitCode"`
		}{
			SessionID: view.SessionID,
			Mode:      string(view.EffectiveMode()),
			Phase:     string(view.Project.Phase),
			Delivered: delivered,
			Delivery:  view.Delivery,
			ExitCode:  code,
		}
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(out, "Duo run finished: session %s, phase %s\n", view.SessionID, view.Project.Phase)
		if delivered {
			fmt.Fprintf(out, "Delivered %s to %s@%s\n", view.Delivery.AppliedHead, view.Delivery.TargetRepo, view.Delivery.TargetBranch)
		} else if reason := strings.TrimSpace(view.Delivery.Reason); reason != "" {
			fmt.Fprintf(out, "Delivery is pending: %s\n", reason)
		}
		if stop == runStopRestart {
			fmt.Fprintf(out, "Session %s asked to continue on a new Duo binary; resume it with `duo resume %s`.\n", view.SessionID, view.SessionID)
		}
	}

	switch {
	case stop == runStopTimeout:
		return errRunTimeout
	case code == runExitDelivered:
		return nil
	default:
		return errRunRefused
	}
}
