package driver

import "strings"

// The helpers in this file build the shell command line a plugin returns from
// prepare. They live in the SDK rather than in any one driver because every
// driver that composes flags needs them, and because the quoting rules are a
// correctness question rather than a per-agent preference: a value that is not
// quoted the shell's way is a command injection, and a value quoted the wrong way
// is a flag that silently loses its argument.

// ShellQuote quotes a value for /bin/sh. %q is deliberately not used: it produces
// Go escaping, not shell escaping, so "a$b" would expand $b and "x`id`y" would run
// id. Single quotes suppress both.
func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
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
