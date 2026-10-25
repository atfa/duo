package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/atfa/duo/internal/clidoc"
	gotui "github.com/atfa/duo/internal/gotui"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/version"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "version":
			fmt.Println("Duo " + version.Current().Describe())
			return
		case "-h", "--help", "help":
			printUsage()
			return
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Subcommands that never start agents are handled before the interactive configuration.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "apply":
			err := runApply(ctx, os.Args[2:])
			switch {
			case err == nil || ctx.Err() != nil:
			case errors.Is(err, errDeliveryPending), errors.Is(err, errPushIncomplete), errors.Is(err, errPRIncomplete):
				os.Exit(1)
			default:
				log.Fatal(err)
			}
			return
		case "run":
			err := runHeadless(ctx, os.Args[2:])
			switch {
			case err == nil || ctx.Err() != nil:
			case errors.Is(err, errRunTimeout):
				os.Exit(runExitTimeout)
			case errors.Is(err, errRunRefused):
				os.Exit(runExitRefused)
			default:
				log.Fatal(err)
			}
			return
		case "status":
			if err := runStatus(ctx, os.Args[2:]); err != nil && ctx.Err() == nil {
				log.Fatal(err)
			}
			return
		case "sessions":
			if err := runSessions(ctx, os.Args[2:]); err != nil && ctx.Err() == nil {
				log.Fatal(err)
			}
			return
		case "logs":
			if err := runLogs(ctx, os.Args[2:]); err != nil && ctx.Err() == nil {
				log.Fatal(err)
			}
			return
		case "clean":
			if err := runClean(ctx, os.Args[2:]); err != nil && ctx.Err() == nil {
				log.Fatal(err)
			}
			return
		case "mcp-server":
			if err := runMCPServer(ctx, os.Args[2:]); err != nil && ctx.Err() == nil {
				log.Fatal(err)
			}
			return
		case "plugins":
			if err := runPlugins(ctx, os.Args[2:]); err != nil && ctx.Err() == nil {
				log.Fatal(err)
			}
			return
		case "doctor":
			// Doctor reports its own findings, so a failing check exits non-zero
			// without a Go error message on top of the report.
			if err := runDoctor(ctx, os.Args[2:]); err != nil {
				if errors.Is(err, errDoctorUnhealthy) {
					os.Exit(1)
				}
				if ctx.Err() == nil {
					log.Fatal(err)
				}
			}
			return
		case "step-loop":
			if err := runStepLoop(ctx, os.Args[2:]); err != nil && ctx.Err() == nil {
				log.Fatal(err)
			}
			return
		}
	}

	args := os.Args[1:]
	resume := len(args) > 0 && args[0] == "resume"
	if resume {
		args = args[1:]
	}
	var cfg config
	var err error
	if resume {
		cfg, err = loadResumeConfig(args)
	} else {
		cfg, err = loadConfig(args)
	}
	if err != nil {
		log.Fatal(err)
	}
	if cfg.help {
		printUsage()
		return
	}
	if err := maybeChooseResumeSession(ctx, &cfg); err != nil {
		log.Fatal(err)
	}
	if err := cfg.validate(); err != nil {
		log.Fatal(err)
	}

	if err := run(ctx, cfg); err != nil && ctx.Err() == nil {
		// A self-upgrade request is not a failure: the session is preserved and the
		// exit code is the signal a launcher acts on. It has to leave by exactly this
		// code, so it cannot go through log.Fatal.
		if errors.Is(err, errUpgradeRequested) {
			os.Exit(upgradeExitCode)
		}
		// A first run fails on a machine problem the operator can fix: a driver CLI
		// that is not installed, a config file that does not parse, a repository that
		// cannot receive the result. Naming the command that says which one is the
		// difference between a self-service fix and a bug report.
		fmt.Fprintln(os.Stderr, "Duo could not start this session. Run `duo doctor` in this repository to find out why.")
		log.Fatal(err)
	}
}

// run starts a session and attaches the terminal interface to it.
//
// It owns no session state. The whole composition root — repository discovery,
// worktrees, the durable store, recovery, delivery and the agents — lives in
// internal/session, so this function differs from any other frontend only in the
// interface it attaches and the lines it prints afterwards.
func run(ctx context.Context, cfg config) error {
	// The terminal is opened once, on first use, and kept across /new: a new
	// session paints over the old one instead of leaving the alternate screen.
	var term *gotui.Terminal
	defer func() { _ = term.Close() }()
	for {
		err := runWith(ctx, cfg, func(ctx context.Context, svc *session.Service) error {
			if term == nil {
				opened, openErr := gotui.OpenTerminal()
				if openErr != nil {
					return openErr
				}
				term = opened
			}
			// The interface owns the terminal from here on. A line logged through the
			// standard logger would land on top of the frame, wrap, and scroll the whole
			// interface up one row, so it goes to the session's log file instead.
			view := svc.View()
			restoreLog := redirectLog(view.Worktrees.Repository, view.SessionID)
			defer restoreLog()
			app := gotui.New(svc)
			// Remote access belongs to this session, not to the process: a pairing
			// that is still live resumes here, and cancelling the context when the
			// interface closes stops the relay connection without revoking the
			// pairing, so /new can take the same room over.
			if mgr, err := newRemoteManager(cfg, svc); err != nil {
				log.Printf("Duo remote: %v", err)
			} else {
				app.SetRemote(mgr)
				remoteCtx, stopRemote := context.WithCancel(ctx)
				defer stopRemote()
				mgr.Resume(remoteCtx)
			}
			// Drivers are built before the interface exists, so any diagnostic one
			// of them reported is shown now, in its own words.
			app.Notices(svc.Notices())
			if err := app.RunTerminal(ctx, term); err != nil && ctx.Err() == nil {
				log.Printf("Duo gotui: %v", err)
			}
			return nil
		})
		var req *newSessionRequest
		if errors.As(err, &req) && ctx.Err() == nil {
			cfg = cfg.freshSession(req)
			continue
		}
		var resumeReq *resumeSessionRequest
		if errors.As(err, &resumeReq) && ctx.Err() == nil {
			cfg = cfg.resumedSession(resumeReq.SessionID)
			continue
		}
		return err
	}
}

// runWith starts a session, attaches the interface this frontend chose, and owns
// the one exit path every frontend shares.
//
// attach is where frontends differ and nothing else is: the restart request, the
// session that never started, and the report the human reads afterwards live here
// rather than in each frontend, because a second copy of them is how one frontend
// ends up resuming a session the other already discarded. attach returning an
// error is logged and does not change that path — a window that failed to open
// still has to close the session it opened.
func runWith(ctx context.Context, cfg config, attach func(context.Context, *session.Service) error) error {
	svc := session.New(cfg.sessionOptions())

	result, err := svc.Start(ctx)
	if err != nil {
		return err
	}
	// A session that is already finished needs no interface: the artifact is in the
	// user's repository, or delivery is pending and only a human can unblock it.
	if result.Finished {
		reportDelivery(result.Outcome)
		return nil
	}

	if attach != nil {
		if err := attach(ctx, svc); err != nil && ctx.Err() == nil {
			log.Printf("Duo interface: %v", err)
		}
	}

	// The settings in force are read before Close: a /new request carries them to
	// the next session, and they must not depend on what Close leaves behind.
	live := svc.View()
	end := svc.Close()
	view := svc.View()

	// A restart request is Duo's own instruction to stop so the session can
	// continue on a new binary. It is written down before the process exits, so the
	// launcher that sees the exit code always finds the session to resume.
	if view.Restart.Reason != "" {
		step := ""
		if len(view.Project.Steps) > 0 {
			step = fmt.Sprintf("%d/%d", view.Project.Step, len(view.Project.Steps))
		}
		repo := view.Worktrees.Repository
		if err := writeUpgradeRequest(repo, view.SessionID, view.Restart.Reason, step, view.Restart.Install); err != nil {
			fmt.Fprintf(os.Stderr, "Duo could not record the upgrade request: %v\n", err)
		}
		fmt.Printf("\nDuo stopped for a self-upgrade: %s\nSession %s was preserved at step %s; resume it with `duo resume %s`.\nAn upgrade launcher (scripts/duo-step-loop.sh) installs the new binary and continues the queue.\n", view.Restart.Reason, view.SessionID, step, view.SessionID)
		return errUpgradeRequested
	}

	// /new keeps the terminal: the next session paints over this one, so nothing is
	// printed here — text written now would land on the interface's screen. The
	// session is preserved (or, when no task was submitted, removed) exactly as
	// Ctrl+Q would have left it.
	if view.NewSession {
		return newSessionFromView(live)
	}
	// /sessions picked another session: this one is closed and preserved, and the
	// caller resumes the chosen id, so a switch in the dashboard is the same path
	// `duo resume <id>` takes.
	if view.ResumeSession != "" {
		return &resumeSessionRequest{SessionID: view.ResumeSession}
	}
	// A session that never started has been removed, so telling the human to
	// resume it would hand them a command that fails and a session that `duo
	// sessions` will never list again.
	if end.Discarded {
		fmt.Printf("\nDuo stopped. No task was submitted, so the session left nothing behind: its worktrees and record were removed. Nothing to resume.\n")
		return nil
	}

	what := "started"
	if cfg.resume {
		what = "resumed"
	}
	fmt.Printf("\nDuo stopped. Session %s was preserved; resume it with `duo resume %s`.\nWorktrees preserved in %s\n", what, end.SessionID, svc.View().Worktrees.Root)
	return nil
}

// reportDelivery prints the outcome of a session that finished without an
// interface. The session reports the facts; the wording is the CLI's.
func reportDelivery(outcome session.DeliveryOutcome) {
	if outcome.Applied {
		printDeliverySuccess(outcome)
		return
	}
	printDeliveryPending(outcome)
}

// printUsage is `duo --help`. The command lines come from internal/clidoc, the
// same table the in-app Help panel renders, so the two cannot disagree.
func printUsage() {
	fmt.Println("Duo " + version.Current().Describe())
	fmt.Println()
	fmt.Println("Usage:")
	for _, line := range clidoc.SummaryLines() {
		fmt.Println(line)
	}
	fmt.Println()
	fmt.Println("Starting a session:")
	fmt.Println("  duo                    start a new Fast session (Austin drives, Tony verifies)")
	fmt.Println("  duo --mode goal        start a new Goal session (shared plan + dual sign-off)")
	fmt.Println("  duo --mode step        start a new Step session (a queue tagged [fast] or [goal], landed step by step)")
	fmt.Println("  duo --language zh        set the language (or -l zh; values: zh, en)")
	fmt.Println("  duo --test-cmd \"go test ./...\"  run the test command before accepting verification (short: -t)")
	fmt.Println("  duo --agent pi           select both agents' driver (short: -a)")
	fmt.Println("  duo --austin-driver pi   select Austin's driver (short: -A)")
	fmt.Println("  duo --tony-driver pi     select Tony's driver (short: -T)")
	fmt.Println("                       select one agent's driver, overriding --agent")
	fmt.Println("  duo plugins            list every driver Duo can see")
	fmt.Println("  duo plugins test <path…> [--all|-a] [--verbose|-v]")
	fmt.Println("                       check Driver Plugins against the protocol; --all")
	fmt.Println("                       checks every driver Duo can see, -v lists each check")
	fmt.Println("  duo resume [id]")
	fmt.Println("                       resume a session here (completed sessions start a follow-up round)")
	fmt.Println()
	fmt.Println("Environment:")
	fmt.Println("  DUO_MODE               default mode for new sessions: fast, goal or step (an explicit --mode wins)")
	fmt.Println("  .duo/config.json       may set \"language\": \"zh\" or \"en\" and \"tuiMode\": \"timeline\" or \"split\"")
	fmt.Println("  DUO_TEST_COMMAND       automated test command run before accepting verification")
	fmt.Println("  DUO_REPO               repository or subdirectory to launch against (a path argument wins)")
	fmt.Println("  DUO_SESSION            session id to use (default: timestamp + random hex)")
	fmt.Println("  DUO_WORKTREE_ROOT      where the Austin/Tony worktrees are created")
	fmt.Println("  DUO_BASE_REF           ref the worktrees are branched from (default: HEAD)")
	fmt.Println("  DUO_PI_COMMAND         command used to launch a Pi agent (default: pi)")
	fmt.Println("  DUO_DRIVER             default driver for both agents (`duo plugins` lists them)")
	fmt.Println("  DUO_LISTEN             bridge listen address (default: 127.0.0.1:0)")
	fmt.Println("  DUO_HARNESS            enable the idle/stall watchdog (default: true)")
	fmt.Println("  DUO_HARNESS_IDLE_SECONDS / _STALL_SECONDS / _COOLDOWN_SECONDS")
	fmt.Println("                         watchdog thresholds")
	fmt.Println("  DUO_HARNESS_RESUME_GRACE_SECONDS")
	fmt.Println("                         extra grace after duo resume (default: 45)")
	fmt.Println("  DUO_HISTORY_FILE       composer task history file (default: ~/.duo/history)")
	fmt.Println("  DUO_AGENT, DUO_SESSION, DUO_TOKEN, DUO_HOST, DUO_PORT")
	fmt.Println("                         bridge connection for `duo mcp-server` (see duo mcp-server --help)")
	fmt.Println()
	fmt.Println("The mode can be switched at any time with /mode fast|goal|step, or from the")
	fmt.Println("dashboard's Ctrl+S mode panel; a switch that would reset the round asks first,")
	fmt.Println("while Fast → Goal escalation keeps the round. F3 or /plan reads the shared plan")
	fmt.Println("and the Step queue. DUO_MODE sets the default for new sessions; an explicit --mode")
	fmt.Println("wins, and resume always uses the session's persisted mode.")
	fmt.Println()
	fmt.Println("Every command prints its own usage with --help, for example: duo apply --help")
	fmt.Println("Run with no path from inside a Git repository: cd project && duo")
}

// commandUsage is the signature clidoc records for one command, for the
// "unknown flag" messages of the individual parsers.
func commandUsage(name string) string {
	if command, ok := clidoc.Lookup(name); ok {
		return command.Signature
	}
	return "duo " + name
}
