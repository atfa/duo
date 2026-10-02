package main

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/clidoc"
)

// parserFuncs are the functions that decide which command line tokens Duo
// accepts. The scan is restricted to them on purpose: a whole-file scan would
// also collect the flags Duo composes into an agent's own command line
// (--model, --thinking, --variant, --session-id, --conversation), which belong
// to the agent CLIs and must never be documented as duo flags.
var parserFuncs = map[string]bool{
	"parseArgs":          true,
	"parseApplyArgs":     true,
	"parseCleanArgs":     true,
	"parseLogsArgs":      true,
	"parseMCPServerArgs": true,
	"parseSessionsArgs":  true,
}

// flagToken matches a flag spelling and nothing else. After the "=" split it
// also rejects the error message strings ("--mode requires a value...") and the
// bare "-" of strings.HasPrefix.
var flagToken = regexp.MustCompile(`^(-[a-zA-Z]|--[a-zA-Z][a-zA-Z0-9-]*)$`)

// parsedFlagTokens is every flag spelling the duo parsers accept.
func parsedFlagTokens(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/duo: %v", err)
	}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			if strings.HasSuffix(file.Name.Name, "_test.go") {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				decl, ok := n.(*ast.FuncDecl)
				if !ok || decl.Body == nil || !parserFuncs[decl.Name.Name] {
					return true
				}
				ast.Inspect(decl.Body, func(m ast.Node) bool {
					lit, ok := m.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						return true
					}
					value, err := strconv.Unquote(lit.Value)
					if err != nil {
						return true
					}
					token, _, _ := strings.Cut(value, "=")
					if flagToken.MatchString(token) {
						seen[token] = true
					}
					return true
				})
				return true
			})
		}
	}
	var out []string
	for name := range seen {
		out = append(out, name)
	}
	return out
}

// TestUsageCoversEveryFlagAndCommand is the two-way check that keeps the printed
// usage honest: a flag Duo parses must appear in `duo --help`, and the usage
// text must not advertise a flag no parser accepts.
func TestUsageCoversEveryFlagAndCommand(t *testing.T) {
	parsed := parsedFlagTokens(t)
	if len(parsed) == 0 {
		t.Fatal("the parser scan found no flags; the FuncDecl whitelist is stale")
	}
	usage := captureStdout(t, printUsage)

	documented := map[string]bool{}
	for _, token := range clidoc.Tokens() {
		documented[token] = true
	}

	for _, token := range parsed {
		if !strings.Contains(usage, token) {
			t.Errorf("duo parses %s but --help never mentions it", token)
		}
		if !documented[token] {
			t.Errorf("duo parses %s but no internal/clidoc signature carries it", token)
		}
	}
	for _, token := range clidoc.Tokens() {
		if !strings.Contains(usage, token) {
			t.Errorf("a clidoc signature advertises %s but --help never prints it", token)
		}
	}
}

// TestUsageNamesEverySubcommand checks that every command the binary dispatches
// is listed by `duo --help`, aliases included.
func TestUsageNamesEverySubcommand(t *testing.T) {
	usage := captureStdout(t, printUsage)
	for _, name := range clidoc.Names() {
		if !strings.Contains(usage, name) {
			t.Errorf("duo dispatches %q but --help never names it", name)
		}
	}
	for _, name := range []string{"apply", "sessions", "logs", "clean", "mcp-server", "plugins"} {
		command, ok := clidoc.Lookup(name)
		if !ok {
			t.Errorf("internal/clidoc has no entry for the %q subcommand", name)
			continue
		}
		if !strings.Contains(usage, command.Summary) {
			t.Errorf("--help prints no summary for %q", name)
		}
	}
}

// TestUsageAdvertisesOnlyAcceptedFlags is the reverse direction: a flag printed
// by `duo --help` must be accepted by at least one parser. Without this, dropping
// a spelling from a parser would silently leave the usage advertising it.
func TestUsageAdvertisesOnlyAcceptedFlags(t *testing.T) {
	// main dispatches --version itself, before any parser runs.
	dispatched := dispatchedTokens(t)
	for _, flag := range clidoc.Tokens() {
		accepted := dispatched[flag]
		for command := range parsersByCommand {
			if acceptsFlag(t, command, flag) == nil {
				accepted = true
				break
			}
		}
		if !accepted {
			t.Errorf("--help advertises %s but no duo parser accepts it", flag)
		}
	}
}

// dispatchedTokens returns the flag-shaped literals of main's own subcommand
// switch, which handles --version before any parser runs.
func dispatchedTokens(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(".", "main.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		clause, ok := n.(*ast.CaseClause)
		if !ok {
			return true
		}
		for _, expr := range clause.List {
			lit, ok := expr.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				continue
			}
			token, _, _ := strings.Cut(value, "=")
			if flagToken.MatchString(token) {
				out[token] = true
			}
		}
		return true
	})
	return out
}

// parsersByCommand maps each documented command to the parser that owns it.
var parsersByCommand = map[string]bool{
	"duo": true, "apply": true, "sessions": true, "logs": true, "clean": true, "mcp-server": true,
}

// TestUsageAcceptsEveryDocumentedFlag feeds each documented flag to the parser
// that owns it, so the usage text cannot document a flag Duo would reject.
func TestUsageAcceptsEveryDocumentedFlag(t *testing.T) {
	perCommand := map[string][]string{
		"duo":        {"--mode", "-m", "--test-cmd", "--agent", "--driver", "--resume", "-r", "--austin-driver", "--austin-agent", "--tony-driver", "--tony-agent", "-h", "--help"},
		"apply":      {"--session", "-s", "-h", "--help"},
		"sessions":   {"--all", "-a", "-h", "--help"},
		"logs":       {"-h", "--help"},
		"clean":      {"--all", "-a", "--all-repos", "--force", "-f", "--dry-run", "-n", "-h", "--help"},
		"mcp-server": {"--agent", "--session", "--token", "--host", "--port", "--export-config", "--export", "--config", "-h", "--help"},
	}
	for command, flags := range perCommand {
		for _, flag := range flags {
			if err := acceptsFlag(t, command, flag); err != nil {
				t.Errorf("duo --help documents %s for %q but the parser rejects it: %v", flag, command, err)
			}
		}
	}
}

// ignoreUsageRequested treats a help request as the success it is.
func ignoreUsageRequested(err error) error {
	if errors.Is(err, errUsageRequested) {
		return nil
	}
	return err
}

// valuelessFlags are the boolean flags that take no value.
var valuelessFlags = map[string]bool{
	"--all": true, "-a": true, "--all-repos": true, "--force": true, "-f": true,
	"--dry-run": true, "-n": true, "--resume": true, "-r": true, "-h": true,
	"--help": true, "--export-config": true, "--export": true, "--config": true,
}

// acceptsFlag runs one flag through the parser that owns the command. A help
// request is a success, not a rejection: it prints usage and exits 0.
func acceptsFlag(t *testing.T, command, flag string) error {
	if valuelessFlags[flag] {
		return accepts(t, command, flag)
	}
	value := "x"
	switch flag {
	case "--mode":
		value = "fast"
	case "--agent":
		if command == "mcp-server" {
			value = "austin"
		}
	}
	return accepts(t, command, flag, value)
}

func accepts(t *testing.T, command string, args ...string) error {
	switch command {
	case "duo":
		_, err := parseArgs(args)
		return err
	case "apply":
		_, err := parseApplyArgs(args)
		return ignoreUsageRequested(err)
	case "sessions":
		_, err := parseSessionsArgs(args)
		return err
	case "logs":
		_, err := parseLogsArgs(args)
		return err
	case "clean":
		_, err := parseCleanArgs(args)
		return err
	case "mcp-server":
		_, err := parseMCPServerArgs(args)
		return err
	}
	t.Fatalf("no parser for command %q", command)
	return nil
}

// TestUsageSatisfiesEverySubcommandHelp checks B1: --help after any command
// prints usage and is not an "unknown flag" failure.
func TestUsageSatisfiesEverySubcommandHelp(t *testing.T) {
	for _, flag := range []string{"-h", "--help", "help"} {
		for _, args := range [][]string{{flag}, {flag, "some-repo"}} {
			parsed, err := parseArgs(args)
			if err != nil {
				t.Errorf("parseArgs(%v) rejected a help request: %v", args, err)
				continue
			}
			if !parsed.help {
				t.Errorf("parseArgs(%v) did not record a help request", args)
			}
		}
	}
	for command, parse := range map[string]func([]string) error{
		"apply": func(a []string) error {
			_, err := parseApplyArgs(a)
			return ignoreUsageRequested(err)
		},
		"sessions":   func(a []string) error { _, err := parseSessionsArgs(a); return err },
		"logs":       func(a []string) error { _, err := parseLogsArgs(a); return err },
		"clean":      func(a []string) error { _, err := parseCleanArgs(a); return err },
		"mcp-server": func(a []string) error { _, err := parseMCPServerArgs(a); return err },
	} {
		for _, flag := range []string{"-h", "--help", "help"} {
			if err := parse([]string{flag}); err != nil {
				t.Errorf("%s %s was rejected: %v", command, flag, err)
			}
		}
	}
	if err := runPlugins([]string{"--help"}); err != nil {
		t.Errorf("duo plugins --help was rejected: %v", err)
	}
	if err := runPlugins([]string{"--bogus"}); err == nil {
		t.Error("duo plugins --bogus was accepted; an unknown flag must fail")
	}
}

// TestUsageExcludesAgentFacingFlags guards D2: the flags Duo hands to an agent
// CLI are not duo flags and must stay out of the user-facing usage.
func TestUsageExcludesAgentFacingFlags(t *testing.T) {
	usage := captureStdout(t, printUsage)
	for _, flag := range []string{"--model", "--thinking", "--variant", "--session-id", "--conversation"} {
		if strings.Contains(usage, flag) {
			t.Errorf("--help documents %s, which belongs to an agent CLI, not to duo", flag)
		}
	}
}

// captureStdout renders fn and returns what it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stdout
	os.Stdout = write
	done := make(chan string, 1)
	go func() {
		var out strings.Builder
		buffer := make([]byte, 4096)
		for {
			n, err := read.Read(buffer)
			out.Write(buffer[:n])
			if err != nil {
				break
			}
		}
		done <- out.String()
	}()
	fn()
	os.Stdout = saved
	_ = write.Close()
	out := <-done
	_ = read.Close()
	if out == "" {
		t.Fatalf("%s printed nothing; it should be captured", filepath.Base("printUsage"))
	}
	return out
}
