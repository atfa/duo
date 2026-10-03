package driver

import "testing"

func TestShellQuoteSurvivesTheShell(t *testing.T) {
	// The failure this prevents is not cosmetic: an unquoted value expands $VAR
	// or runs a command substitution inside the agent's own command line.
	cases := map[string]string{
		"plain":     "'plain'",
		"a$b":       "'a$b'",
		"x`id`y":    "'x`id`y'",
		"it's":      `'it'\''s'`,
		"two words": "'two words'",
	}
	for value, want := range cases {
		if got := ShellQuote(value); got != want {
			t.Errorf("ShellQuote(%q) = %s, want %s", value, got, want)
		}
	}
}

func TestHasFlagMatchesWholeTokensOnly(t *testing.T) {
	// A substring test for "-c" also matches "--config", which silently dropped
	// an agent's session identity once.
	if HasFlag("agy --config /tmp/agy.json", "-c") {
		t.Fatal("--config matched -c; a substring test here dropped an agent's session id")
	}
	if !HasFlag("agy -c abc", "-c") {
		t.Fatal("a real -c was not found")
	}
	if !HasFlag("pi --session-id=abc", "--session-id") {
		t.Fatal("the --flag=value spelling was not found")
	}
}

func TestSetFlagReplacesAndAppends(t *testing.T) {
	if got := SetFlag("agy run", "--model", "gemini-3.8-flash-high"); got != "agy run --model 'gemini-3.8-flash-high'" {
		t.Errorf("append = %q", got)
	}
	if got := SetFlag("agy --model old", "--model", "new"); got != "agy --model 'new'" {
		t.Errorf("replace = %q", got)
	}
	if got := SetFlag("agy --model=old", "--model", "new"); got != "agy --model='new'" {
		t.Errorf("replace =%q", got)
	}
	// An empty value must remove the flag rather than set it to nothing, because
	// "--model" with no argument swallows whatever comes next.
	if got := SetFlag("agy --model old", "--model", ""); got != "agy --model ''" {
		t.Errorf("empty value = %q", got)
	}
}

func TestAppendFlagRespectsAnOperatorsChoice(t *testing.T) {
	// An explicit operator choice wins: Duo must not override a flag a human
	// wrote on the command line or in config.
	if got := AppendFlag("agy --model mine", "--model", "theirs"); got != "agy --model mine" {
		t.Errorf("AppendFlag overrode an operator's flag: %q", got)
	}
	// An empty value adds nothing at all: "--model ''" is a flag whose argument is
	// the empty string, which a CLI that validates arguments will reject.
	if got := AppendFlag("agy run", "--model", ""); got != "agy run" {
		t.Errorf("AppendFlag with an empty value = %q, want no flag added", got)
	}
	if got := AppendFlag("agy run", "--auto", "1"); got != "agy run --auto '1'" {
		t.Errorf("AppendFlag with a valueless flag = %q", got)
	}
}

func TestBaseCommandIsQuoteAware(t *testing.T) {
	// Quoting is preserved, because the result is handed to a shell: a custom
	// binary path keeps working and a path containing a space is not cut in half.
	cases := map[string]string{
		"pi":                        "pi",
		"pi --session-id abc":       "pi",
		"/opt/my tools/pi --flag":   "/opt/my",
		"'/opt/my tools/pi' --flag": "'/opt/my tools/pi'",
		`"/opt/pi" --flag`:          `"/opt/pi"`,
		"":                          "",
	}
	for command, want := range cases {
		if got := BaseCommand(command); got != want {
			t.Errorf("BaseCommand(%q) = %q, want %q", command, got, want)
		}
	}
}

func TestBareModelID(t *testing.T) {
	cases := map[string]string{
		"anthropic/claude-sonnet-4-6": "claude-sonnet-4-6",
		"gemini-3.8-flash-high":       "gemini-3.8-flash-high",
		"vendor/model/v2":             "v2",
		"":                            "",
	}
	for model, want := range cases {
		if got := BareModelID(model); got != want {
			t.Errorf("BareModelID(%q) = %q, want %q", model, got, want)
		}
	}
}
