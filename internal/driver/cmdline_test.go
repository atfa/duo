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

// TestQuoteWindowsMatchesCommandLineToArgvW pins the Windows quoting rule: a value
// reaches the program as one argument with its backslashes and quotes intact. The
// escaping is not cosmetic — an unescaped quote ends the argument early, which is
// how a path with a space and a trailing backslash loses its last character.
func TestQuoteWindowsMatchesCommandLineToArgvW(t *testing.T) {
	cases := map[string]string{
		"plain":           `"plain"`,
		"two words":       `"two words"`,
		`a"b`:             `"a\"b"`,
		`C:\work\`:        `"C:\work\\"`,
		`C:\a b\file.txt`: `"C:\a b\file.txt"`,
		`x\y`:             `"x\y"`,
		`model="gpt 5"`:   `"model=\"gpt 5\""`,
	}
	for value, want := range cases {
		if got := quoteWindows(value); got != want {
			t.Errorf("quoteWindows(%q) = %s, want %s", value, got, want)
		}
	}
}

// TestWindowsShellLineKeepsTheCommandVerbatim pins the one command line cmd.exe
// receives: /d skips AutoRun, /s makes cmd strip exactly the outer quotes added
// here, and the driver's own quoting is left alone inside them. A command that
// starts with a quoted path is the case cmd's own quote heuristic would otherwise
// collapse.
func TestWindowsShellLineKeepsTheCommandVerbatim(t *testing.T) {
	command := `"C:\Program Files\Pi\pi.exe" --session-id "%DUO_PI_SESSION_ID%"`
	const prefix = `"C:\Windows\system32\cmd.exe" /d /s /c "`
	got := WindowsShellLine(`C:\Windows\system32\cmd.exe`, command)
	if got != prefix+command+`"` {
		t.Fatalf("WindowsShellLine = %s, want %s followed by the command", got, prefix)
	}
	// The driver's own quoting has to survive untouched, not be re-quoted here.
	if len(got) < len(prefix)+len(command)+1 || got[len(prefix):len(got)-1] != command {
		t.Fatalf("WindowsShellLine rewrote the command: %s", got)
	}
}

// TestEnvReferenceSpellsTheRunningShell keeps the reference a plugin hands its agent
// in the shell that will actually expand it: $NAME for /bin/sh, %NAME% for cmd.exe.
func TestEnvReferenceSpellsTheRunningShell(t *testing.T) {
	want := wantEnvReference("DUO_PI_SESSION_ID")
	if got := EnvReference("DUO_PI_SESSION_ID"); got != want {
		t.Fatalf("EnvReference = %s, want %s for this shell", got, want)
	}
}
