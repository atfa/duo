package main

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/clidoc"
)

// parserCommands maps each argument parser to the clidoc command that owns its
// flags. parseArgs owns the top-level usage line, which clidoc records under the
// "duo" command; every other parser owns its subcommand. The mapping is written
// out rather than derived, because a parser's name does not say which command it
// serves, and guessing is how a flag ends up checked against the wrong
// signature.
//
// The scan is restricted to these functions on purpose: a whole-file scan would
// also collect the flags Duo composes into an agent's own command line
// (--model, --thinking, --variant, --session-id, --conversation), which belong
// to the agent CLIs and must never be documented as duo flags.
var parserCommands = map[string]string{
	"parseArgs":          "duo",
	"parseApplyArgs":     "apply",
	"parseCleanArgs":     "clean",
	"parseLogsArgs":      "logs",
	"parseMCPServerArgs": "mcp-server",
	"parseSessionsArgs":  "sessions",
	"runPlugins":         "plugins",
}

// flagToken matches a flag spelling and nothing else. After the "=" split it
// also rejects the error message strings ("--mode requires a value...") and the
// bare "-" of strings.HasPrefix.
var flagToken = regexp.MustCompile(`^(-[a-zA-Z]|--[a-zA-Z][a-zA-Z0-9-]*)$`)

// helpFlags are the help spellings every command accepts, and the usage footer
// documents them once instead of repeating them in every signature. They are
// the only flags a signature may carry without its own parser naming them.
var helpFlags = []string{"-h", "--help"}

// parsedFlagTokens is every flag spelling the duo parsers accept.
func parsedFlagTokens(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, flags := range parsedFlagsByCommand(t) {
		for flag := range flags {
			out = append(out, flag)
		}
	}
	return out
}

// parsedFlagsByCommand attributes each parsed flag to the command whose parser
// accepts it. Keeping the attribution is the point: a flat set cannot tell
// `duo sessions --all` from `duo clean --all`, so a flag dropped from one
// signature stayed green as long as another signature still spelled it.
func parsedFlagsByCommand(t *testing.T) map[string]map[string]bool {
	t.Helper()
	seen := map[string]map[string]bool{}
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
				if !ok || decl.Body == nil {
					return true
				}
				command, scanned := parserCommands[decl.Name.Name]
				if !scanned {
					return true
				}
				flags := seen[command]
				if flags == nil {
					flags = map[string]bool{}
					seen[command] = flags
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
						flags[token] = true
					}
					return true
				})
				return true
			})
		}
	}
	return seen
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

// TestSignaturesMatchTheFlagsEachCommandParses checks each command's signature
// against the parser that owns that command, in both directions.
//
// Before, the flags of every parser were flattened into one set and checked
// against every signature, so a flag had to appear somewhere and nothing more:
// dropping `sessions [--all|-a]` kept the test green because `duo clean`
// still spelled `--all|-a`, while `duo sessions --all` kept working undeclared.
//
// Scope, stated rather than implied: this only reaches the flags parsed by the
// functions listed in parserCommands. A parser missing from that map is
// unchecked here — TestEveryFlagParserIsMapped is what keeps the map honest.
// It compares flags, not positional arguments.
func TestSignaturesMatchTheFlagsEachCommandParses(t *testing.T) {
	parsed := parsedFlagsByCommand(t)
	if len(parsed) == 0 {
		t.Fatal("the parser scan found no flags; parserCommands is stale")
	}
	for command, flags := range parsed {
		signature, ok := clidoc.Lookup(command)
		if !ok {
			t.Errorf("internal/clidoc has no entry for the %q command", command)
			continue
		}
		documented := map[string]bool{}
		for _, token := range clidoc.TokensFor(command) {
			documented[token] = true
		}

		// Forward: what this parser accepts has to be written down here, not
		// only in some other command's signature.
		for flag := range flags {
			if documented[flag] || isHelpFlag(flag) {
				continue
			}
			t.Errorf("%s parses %s but its signature %q does not carry it", command, flag, signature.Signature)
		}

		// Reverse: the signature must not promise a flag this command rejects.
		for _, token := range clidoc.TokensFor(command) {
			if flags[token] || isHelpFlag(token) || acceptsFlag(t, command, token) == nil {
				continue
			}
			t.Errorf("%s advertises %s in %q but its parser rejects it", command, token, signature.Signature)
		}
	}
}

// TestEveryFlagParserIsMapped bounds TestSignaturesMatchTheFlagsEachCommandParses:
// a new parser that nobody added to parserCommands would be checked by no
// per-command test at all, which is exactly the gap this guard exists to close.
func TestEveryFlagParserIsMapped(t *testing.T) {
	parsers := argumentParserNames(t)
	if len(parsers) == 0 {
		t.Fatal("found no argument parsers in cmd/duo; the scan is stale")
	}
	for _, name := range parsers {
		command, ok := parserCommands[name]
		if !ok {
			t.Errorf("%s parses the command line but is absent from parserCommands, so its flags are documented by no per-command check", name)
			continue
		}
		if _, ok := clidoc.Lookup(command); !ok {
			t.Errorf("parserCommands maps %s to %q, which internal/clidoc does not document", name, command)
		}
	}
	for name := range parserCommands {
		if !slices.Contains(parsers, name) {
			t.Errorf("parserCommands lists %s, which no longer exists in cmd/duo", name)
		}
	}
}

func isHelpFlag(flag string) bool {
	for _, help := range helpFlags {
		if flag == help {
			return true
		}
	}
	return false
}

// argumentParserNames returns every function in cmd/duo that takes a command
// line apart: the parse*Args family plus runPlugins.
func argumentParserNames(t *testing.T) []string {
	t.Helper()
	var out []string
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
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				if fn.Name.Name == "runPlugins" || (strings.HasPrefix(fn.Name.Name, "parse") && strings.HasSuffix(fn.Name.Name, "Args")) {
					out = append(out, fn.Name.Name)
				}
			}
		}
	}
	return out
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
