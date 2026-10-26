package driver

import (
	"os"
	"runtime"
	"strings"
)

// The helpers in this file build the shell command line a plugin returns from
// prepare. They live in the SDK rather than in any one driver because every
// driver that composes flags needs them, and because the quoting rules are a
// correctness question rather than a per-agent preference: a value that is not
// quoted the shell's way is a command injection, and a value quoted the wrong way
// is a flag that silently loses its argument.

// ShellQuote quotes a value for the shell that will run the command line: /bin/sh
// on Unix and cmd.exe on Windows. %q is deliberately not used: it produces Go
// escaping, not shell escaping, so "a$b" would expand $b and "x`id`y" would run
// id. The quoting is per platform because a single quote is not a quote character
// to cmd.exe, and a Windows command line is parsed by the program it starts, not
// just by the shell.
func ShellQuote(value string) string {
	if runtime.GOOS == "windows" {
		return quoteWindows(value)
	}
	return quotePOSIX(value)
}

// quotePOSIX quotes a value for /bin/sh. Single quotes suppress expansion and
// command substitution; an embedded single quote is closed, escaped and reopened.
func quotePOSIX(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// quoteWindows quotes a value so cmd.exe passes it through and the program it
// starts reads it back as one argument. The escaping is the CommandLineToArgvW
// rule every Windows program and C runtime uses: backslashes are literal except
// immediately before a quote, where they are doubled and the quote is escaped.
// Always quoting is safe: cmd.exe strips a quote pair it finds around a token.
func quoteWindows(value string) string {
	var out strings.Builder
	out.Grow(len(value) + 2)
	out.WriteByte('"')
	backslashes := 0
	for _, r := range value {
		switch r {
		case '\\':
			backslashes++
		case '"':
			out.WriteString(strings.Repeat(`\`, backslashes*2+1))
			out.WriteByte('"')
			backslashes = 0
		default:
			out.WriteString(strings.Repeat(`\`, backslashes))
			backslashes = 0
			out.WriteRune(r)
		}
	}
	// Backslashes before the closing quote are literal, so they are doubled.
	out.WriteString(strings.Repeat(`\`, backslashes*2))
	out.WriteByte('"')
	return out.String()
}

// ComSpec is the command interpreter WindowsShellLine starts: %COMSPEC% when the
// environment supplies it, cmd.exe otherwise.
func ComSpec() string {
	if comspec := strings.TrimSpace(os.Getenv("COMSPEC")); comspec != "" {
		return comspec
	}
	return "cmd.exe"
}

// WindowsShellLine renders command as the one command line cmd.exe receives.
//
// The command a driver composes is a shell line, and cmd.exe is not reached
// through an argument vector: it parses the rest of its own command line. /d skips
// AutoRun registry commands and /s makes cmd strip exactly the outer quote pair
// added here, which is what keeps a command that starts with a quoted path — the
// shape every custom binary under "C:\Program Files" takes — intact instead of
// collapsing it into cmd's first-quote/last-quote heuristic.
func WindowsShellLine(program, command string) string {
	return quoteWindows(program) + ` /d /s /c "` + command + `"`
}

// EnvReference returns a quoted shell token that expands to the value of the named
// environment variable when the launch command runs: $NAME on Unix, %NAME% on
// Windows. It is how a driver passes a generated identity to its agent without
// interpolating it, so no quoting mistake in the value can change the command.
func EnvReference(name string) string {
	if runtime.GOOS == "windows" {
		return `"%` + name + `%"`
	}
	return `"$` + name + `"`
}

// HasFlag reports whether a command line already carries one of the named flags.
// Detection matches whole tokens only: a substring test for "-c" also matches
// "--config", which once cost an agent its session identity.
func HasFlag(command string, names ...string) bool {
	for _, token := range strings.Fields(command) {
		for _, name := range names {
			if token == name || strings.HasPrefix(token, name+"=") {
				return true
			}
		}
	}
	return false
}

// ReadFlag returns the value of flag in a command string, accepting both the
// "--flag value" and "--flag=value" spellings.
func ReadFlag(command, flag string) (string, bool) {
	tokens := strings.Fields(command)
	for i, token := range tokens {
		if token == flag && i+1 < len(tokens) {
			return strings.Trim(tokens[i+1], `"'`), true
		}
		if strings.HasPrefix(token, flag+"=") {
			return strings.Trim(strings.TrimPrefix(token, flag+"="), `"'`), true
		}
	}
	return "", false
}

// SetFlag applies flag=value to a command string, replacing an existing
// occurrence and appending when absent. An empty value removes the flag rather
// than setting it to nothing, because "--model " would swallow the next argument.
func SetFlag(command, flag, value string) string {
	tokens := strings.Fields(command)
	prefix := flag + "="
	for i := 0; i < len(tokens); i++ {
		if tokens[i] == flag && i+1 < len(tokens) {
			tokens[i+1] = ShellQuote(value)
			return strings.Join(tokens, " ")
		}
		if strings.HasPrefix(tokens[i], prefix) {
			tokens[i] = prefix + ShellQuote(value)
			return strings.Join(tokens, " ")
		}
	}
	if value == "" {
		return command
	}
	return command + " " + flag + " " + ShellQuote(value)
}

// AppendFlag adds flag value unless the command already carries flag. It is how a
// plugin injects a flag without fighting an operator who already supplied one.
//
// An empty value adds nothing: "--model ”" is not the same as no flag at all, it is
// a flag whose argument is the empty string, and a CLI that validates its arguments
// will reject it.
func AppendFlag(command, flag, value string) string {
	if HasFlag(command, flag) || strings.TrimSpace(value) == "" {
		return command
	}
	return strings.TrimSpace(command) + " " + flag + " " + ShellQuote(value)
}

// AppendLiteral adds a token unless the command already carries flag. It is for
// values a plugin requires verbatim, such as an identity it passes through an
// environment variable so no re-quoting can corrupt it.
func AppendLiteral(command, flag, literal string) string {
	if HasFlag(command, flag) {
		return command
	}
	return strings.TrimSpace(command) + " " + flag + " " + literal
}

// BareModelID strips a provider prefix from a model reference. A model ID may
// itself contain a slash, so the last segment is the model.
func BareModelID(model string) string {
	model = strings.TrimSpace(model)
	if idx := strings.LastIndex(model, "/"); idx != -1 {
		return model[idx+1:]
	}
	return model
}

// BaseCommand returns a command line's executable, quote-aware, because the
// result is handed to a shell: a custom binary path keeps working and a path
// containing a space is not cut in half.
func BaseCommand(command string) string {
	trimmed := strings.TrimSpace(command)
	var quote byte
	for i := 0; i < len(trimmed); i++ {
		c := trimmed[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == ' ' || c == '\t':
			return trimmed[:i]
		}
	}
	return trimmed
}
