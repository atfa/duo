// Package clidoc is the single source of truth for the duo command line
// surface: one entry per command with its signature line and a one-line
// summary. `duo --help` and the in-app Help panel both render Signature, so the
// two surfaces cannot drift apart.
package clidoc

import "strings"

// Command is one command line entry point of the duo binary.
type Command struct {
	// Name is the token typed on the command line.
	Name string
	// Aliases are the other tokens that select this command.
	Aliases []string
	// Signature is the rendered signature line. It carries the flags and
	// aliases the command accepts, so it is the only place they are written
	// down.
	Signature string
	// Summary is the one-line description used by `duo --help`.
	Summary string
}

// Is reports whether token selects this command, by name or alias.
func (c Command) Is(token string) bool {
	if c.Name == token {
		return true
	}
	for _, alias := range c.Aliases {
		if alias == token {
			return true
		}
	}
	return false
}

// commands lists every duo command in the order `duo --help` presents them.
// A Signature carries every spelling the parser accepts — long flag, short flag
// and alias — so the usage test can require each of them to appear in both
// documentation surfaces.
var commands = []Command{
	{
		Name:      "duo",
		Signature: "duo [repository] [--mode fast|goal|-m] [--test-cmd <cmd>] [--agent|--driver pi|agy|opencode] [--resume [id]|-r] [--austin-driver <driver>] [--tony-driver <driver>] (aliases: --austin-agent, --tony-agent)",
		Summary:   "start a new session (Fast by default: Austin drives, Tony verifies)",
	},
	{
		Name:      "apply",
		Signature: "duo apply [session-id]   (also --session <id> | -s <id>)",
		Summary:   "deliver a pending final result to this repository",
	},
	{
		Name:      "sessions",
		Signature: "duo sessions [--all|-a] [repository]",
		Summary:   "list sessions for this repository (--all for every repository)",
	},
	{
		Name:      "logs",
		Signature: "duo logs [repository]",
		Summary:   "list the Markdown transcripts written to .duo/logs",
	},
	{
		Name:      "clean",
		Signature: "duo clean [session-id] [--all|-a] [--all-repos] [--force|-f] [--dry-run|-n]",
		Summary:   "clean completed sessions and worktrees (--force for unfinished)",
	},
	{
		Name:      "plugins",
		Aliases:   []string{"plugin"},
		Signature: "duo plugins   (alias: duo plugin)",
		Summary:   "list built-in and discovered external agent driver plugins",
	},
	{
		Name:      "mcp-server",
		Aliases:   []string{"mcp"},
		Signature: "duo mcp-server (alias: duo mcp) [--agent austin|tony] [--session <id>] [--token <token>] [--host <host>] [--port <port>] [--export-config|--export|--config]",
		Summary:   "serve Model Context Protocol (MCP) state machine tools over stdio",
	},
	{
		Name:      "help",
		Aliases:   []string{"-h", "--help"},
		Signature: "duo help | -h | --help",
		Summary:   "print this usage, including every flag and the DUO_* environment variables",
	},
	{
		Name:      "version",
		Aliases:   []string{"--version"},
		Signature: "duo version | --version",
		Summary:   "print the Duo version",
	},
}

// Commands returns every duo command, in usage order.
func Commands() []Command { return commands }

// SignatureLines returns the rendered signature of every command, one per
// line. Both documentation surfaces print exactly these lines.
func SignatureLines() []string {
	lines := make([]string, 0, len(commands))
	for _, command := range commands {
		lines = append(lines, command.Signature)
	}
	return lines
}

// Names returns every command name and alias.
func Names() []string {
	var out []string
	for _, command := range commands {
		out = append(out, command.Name)
		out = append(out, command.Aliases...)
	}
	return out
}

// Lookup finds the command selected by token, or false when it is not a
// command.
func Lookup(token string) (Command, bool) {
	for _, command := range commands {
		if command.Is(token) {
			return command, true
		}
	}
	return Command{}, false
}

// SummaryLines returns "  <signature> — <summary>" for every command, the form
// `duo --help` uses to pair each signature with its description.
func SummaryLines() []string {
	lines := make([]string, 0, len(commands))
	for _, command := range commands {
		lines = append(lines, "  "+command.Signature+" — "+command.Summary)
	}
	return lines
}

// Tokens returns every flag-shaped token mentioned in the signatures, with any
// `--flag=` suffix normalised away, so tests can check the signatures against
// what the parsers accept.
func Tokens() []string {
	var out []string
	for _, line := range SignatureLines() {
		for _, field := range strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '[' || r == ']' || r == '|' || r == '(' || r == ')' }) {
			if field == "--" || field == "-" {
				continue
			}
			if strings.HasPrefix(field, "-") {
				out = append(out, strings.TrimSuffix(strings.TrimSuffix(field, ","), "="))
			}
		}
	}
	return out
}
