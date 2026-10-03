package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/atfa/duo/internal/clidoc"
	"github.com/atfa/duo/internal/driver"
)

// runPlugins implements `duo plugins` and `duo plugin test`.
//
// `duo plugin test <path>` exists so that writing a plugin does not require
// understanding Duo Core. A plugin author builds their executable, points this at
// it, and either passes or does not — which is the whole onboarding story for a
// driver.
func runPlugins(ctx context.Context, args []string) error {
	if len(args) == 0 {
		printPlugins()
		return nil
	}
	switch strings.TrimSpace(args[0]) {
	case "test":
		return runPluginTest(ctx, args[1:])
	case "list", "ls":
		printPlugins()
		return nil
	case "-h", "--help", "help":
		printPluginUsage()
		return nil
	default:
		if strings.HasPrefix(args[0], "-") {
			return fmt.Errorf("unknown flag %q (usage: %s)", args[0], commandUsage("plugins"))
		}
		// A bare path is a convenience: `duo plugin ./duo-plugin-foo`.
		return runPluginTest(ctx, args)
	}
}

// pluginTestArgs is the parsed `duo plugin test` command line.
type pluginTestArgs struct {
	paths   []string
	all     bool
	verbose bool
	help    bool
}

// parsePluginTestArgs understands `duo plugin test [<path|--driver-name>…] [--all] [-v]`.
// A bare driver name is resolved against the installed plugins, so a plugin author
// does not have to know where it ended up.
func parsePluginTestArgs(args []string) (pluginTestArgs, error) {
	var out pluginTestArgs
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		if helpRequested(arg) {
			out.help = true
			continue
		}
		switch arg {
		case "--all", "-a":
			out.all = true
		case "-v", "--verbose":
			out.verbose = true
		default:
			if strings.HasPrefix(arg, "-") {
				return out, fmt.Errorf("unknown flag %q (usage: %s)", arg, commandUsage("plugins"))
			}
			out.paths = append(out.paths, arg)
		}
	}
	return out, nil
}

func runPluginTest(ctx context.Context, args []string) error {
	parsed, err := parsePluginTestArgs(args)
	if err != nil {
		return err
	}
	if parsed.help || (len(parsed.paths) == 0 && !parsed.all) {
		printPluginUsage()
		return nil
	}
	paths, all, verbose := parsed.paths, parsed.all, parsed.verbose

	work, err := os.MkdirTemp("", "duo-plugin-test")
	if err != nil {
		return fmt.Errorf("create a scratch directory: %w", err)
	}
	defer os.RemoveAll(work)

	// The reference plugin is the positive control. Without a plugin known to work,
	// the crash-and-respawn check has nothing to verify against and would silently
	// stop testing anything.
	if _, err := driver.ReferencePluginBinary(ctx, work); err != nil {
		fmt.Fprintf(os.Stderr, "duo plugin test: %v\n", err)
		fmt.Fprintf(os.Stderr, "  the crash-recovery check will be skipped\n")
	}

	var reports []driver.Report
	if all {
		// Registration is idempotent, so listing and testing see the same table the
		// session uses.
		registerDrivers(nil)
		reports = driver.DiscoverAndRun(ctx, work)
	}
	for _, path := range paths {
		reports = append(reports, driver.RunContract(ctx, resolvePluginPath(path), work))
	}

	if len(reports) == 0 {
		printPluginUsage()
		return nil
	}

	failed := 0
	for i, report := range reports {
		if i > 0 {
			fmt.Println()
		}
		if verbose || len(reports) > 1 || report.Failed {
			fmt.Print(report.Format())
		} else {
			// One plugin and no failures: the summary line is enough.
			fmt.Printf("%s: %s\n", report.Name, verdict(report))
		}
		if !report.Passed() {
			failed++
		}
	}

	switch {
	case failed == 0:
		fmt.Printf("\n%d plugin(s) satisfy the Duo Driver Plugin Protocol v1.\n", len(reports))
		return nil
	default:
		return fmt.Errorf("%d of %d plugin(s) do not satisfy the Duo Driver Plugin Protocol v1", failed, len(reports))
	}
}

// resolvePluginPath accepts a path, a file name to be found on PATH, or a bare
// driver name, so `duo plugin test codex` works as well as a full path.
func resolvePluginPath(arg string) string {
	if strings.ContainsRune(arg, os.PathSeparator) || strings.HasPrefix(arg, ".") {
		return arg
	}
	if info, err := os.Stat(arg); err == nil && !info.IsDir() {
		return arg
	}
	if path, ok := driver.Lookup(arg); ok {
		return path
	}
	return arg
}

func verdict(report driver.Report) string {
	if report.Passed() {
		return fmt.Sprintf("%s — satisfies the protocol (%s)", report.Version, report.Duration.Round(1e6))
	}
	return "does NOT satisfy the protocol"
}

// printPlugins lists every driver Duo can see, with the executable that will
// actually run for it.
func printPlugins() {
	registerDrivers(nil)
	entries := driver.Discover()
	if len(entries) == 0 {
		fmt.Println("No driver plugins found.")
		fmt.Println()
		fmt.Println("Duo selects a driver with --agent <name>, or per agent with")
		fmt.Println("--austin-driver / --tony-driver.")
		return
	}
	fmt.Printf("%d driver plugin(s) available:\n", len(entries))
	for _, entry := range entries {
		origin := "external"
		if entry.Shipped {
			origin = "shipped"
		}
		fmt.Printf("  %-12s %-10s %s\n", entry.Name, "["+origin+"]", entry.Path)
	}
	fmt.Println()
	fmt.Println("Duo selects a driver with --agent <name>, or per agent with")
	fmt.Println("--austin-driver / --tony-driver. Check one against the protocol with")
	fmt.Println("`duo plugin test <name>`.")
}

func printPluginUsage() {
	fmt.Println("Usage:")
	for _, line := range clidoc.SummaryLines() {
		if strings.HasPrefix(line, "duo plugin") {
			fmt.Println(line)
		}
	}
	fmt.Println()
	fmt.Println("  duo plugins                      list every driver Duo can see")
	fmt.Println("  duo plugin test <path>           check one plugin against the protocol")
	fmt.Println("  duo plugin test --all            check every plugin Duo can see")
	fmt.Println("  duo plugin test <name>           check an installed plugin by driver name")
	fmt.Println()
	fmt.Println("A plugin passes when it describes itself correctly, probes, prepares a")
	fmt.Println("runnable command, keeps its capabilities honest, resumes its own session")
	fmt.Println("state, and survives being killed. See docs/plugin-development-guide.md.")
}
