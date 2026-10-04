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
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/tui"
	"github.com/atfa/duo/internal/version"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "version":
			fmt.Println("Duo " + version.Version)
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
			case errors.Is(err, errDeliveryPending):
				os.Exit(1)
			default:
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
		case "mcp-server", "mcp":
			if err := runMCPServer(ctx, os.Args[2:]); err != nil && ctx.Err() == nil {
				log.Fatal(err)
			}
			return
		case "plugins", "plugin":
			if err := runPlugins(ctx, os.Args[2:]); err != nil && ctx.Err() == nil {
				log.Fatal(err)
			}
			return
		}
	}

	cfg, err := loadConfig(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	if cfg.help {
		printUsage()
		return
	}
	if err := cfg.validate(); err != nil {
		log.Fatal(err)
	}

	if err := run(ctx, cfg); err != nil && ctx.Err() == nil {
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

	app := tui.New(svc)
	// Drivers are built before the interface exists, so any diagnostic one of them
	// reported is shown now, in its own words.
	app.Notices(svc.Notices())
	if err := app.Run(ctx); err != nil && ctx.Err() == nil {
		log.Printf("Duo TUI: %v", err)
	}

	end := svc.Close()

	what := "started"
	if cfg.resume {
		what = "resumed"
	}
	fmt.Printf("\nDuo stopped. Session %s was preserved; resume it with `duo --resume %s`.\nWorktrees preserved in %s\n", what, end.SessionID, svc.View().Worktrees.Root)
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
	fmt.Println("Duo " + version.Version)
	fmt.Println()
	fmt.Println("Usage:")
	for _, line := range clidoc.SummaryLines() {
		fmt.Println(line)
	}
	fmt.Println()
	fmt.Println("Starting a session:")
	fmt.Println("  duo                    start a new Fast session (Austin drives, Tony verifies)")
	fmt.Println("  duo --mode goal        start a new Goal session (shared plan + dual sign-off)")
	fmt.Println("  duo --test-cmd <cmd>   run automated test command before accepting verification")
	fmt.Println("  duo --agent <driver>")
	fmt.Println("                       select the driver for both agents (`duo plugins` lists them)")
	fmt.Println("  duo --austin-driver <driver>")
	fmt.Println("  duo --tony-driver <driver>")
	fmt.Println("                       select one agent's driver, overriding --agent")
	fmt.Println("  duo plugins            list every driver Duo can see")
	fmt.Println("  duo plugin test <path…> [--all|-a] [--verbose|-v]")
	fmt.Println("                       check Driver Plugins against the protocol; --all")
	fmt.Println("                       checks every driver Duo can see, -v lists each check")
	fmt.Println("  duo resume             resume this repository's unfinished session (same as -r / --resume)")
	fmt.Println("  duo --resume           resume this repository's unfinished session")
	fmt.Println("  duo --resume <id>      resume one specific session (required if several are unfinished)")
	fmt.Println()
	fmt.Println("Aliases:")
	fmt.Println("  --driver = --agent; --austin-agent / --tony-agent = --austin-driver / --tony-driver;")
	fmt.Println("  the bare word resume = --resume; duo plugin = duo plugins; duo mcp = duo mcp-server.")
	fmt.Println()
	fmt.Println("Environment:")
	fmt.Println("  DUO_MODE               default mode for new sessions: fast or goal (an explicit --mode wins)")
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
	fmt.Println("                         extra grace after --resume (default: 45)")
	fmt.Println("  DUO_HISTORY_FILE       composer task history file (default: ~/.duo/history)")
	fmt.Println("  DUO_AGENT, DUO_SESSION, DUO_TOKEN, DUO_HOST, DUO_PORT")
	fmt.Println("                         bridge connection for `duo mcp-server` (see duo mcp-server --help)")
	fmt.Println()
	fmt.Println("Mode is fixed for a session's lifetime. DUO_MODE sets the default for new sessions;")
	fmt.Println("an explicit --mode wins, and --resume always uses the session's persisted mode.")
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
