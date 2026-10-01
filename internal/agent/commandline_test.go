package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/protocol"
)

// A substring test for "-c" also matches "--config". When it did, Duo silently
// stopped injecting --conversation and the agy agent lost its stable identity
// across restarts.
func TestAgyConversationInjectedDespiteUnrelatedFlags(t *testing.T) {
	for _, command := range []string{
		"agy",
		"agy --config /etc/agy.json",
		"agy --continue",
		"agy --model google/gemini-3.8-flash-low",
	} {
		s := NewAgySession(Config{
			Agent:             protocol.Austin,
			Command:           command,
			AgyConversationID: "conv-1",
		})
		out := s.commandLine()
		if !strings.Contains(out, `--conversation "$DUO_AGY_CONVERSATION_ID"`) {
			t.Errorf("command %q: conversation id was not injected: %s", command, out)
		}
	}
}

// An operator who already supplies a conversation flag keeps their choice.
func TestAgyRespectsExistingConversationFlag(t *testing.T) {
	for _, command := range []string{`agy --conversation mine`, "agy -c mine"} {
		s := NewAgySession(Config{Agent: protocol.Austin, Command: command, AgyConversationID: "conv-1"})
		out := s.commandLine()
		if strings.Contains(out, "$DUO_AGY_CONVERSATION_ID") {
			t.Errorf("command %q: Duo should not override the operator's conversation: %s", command, out)
		}
	}
}

// %q is Go escaping, not shell escaping: "a$b" expands $b and "x`id`y" runs id.
func TestShellQuotedValuesSurviveTheShell(t *testing.T) {
	for _, model := range []string{"gemini-3.8-flash-low", "a$b", "x`id`y", "with space", "it's"} {
		s := NewAgySession(Config{Agent: protocol.Austin, Command: "agy", Model: model})
		out := s.commandLine()
		if got := shellArgValue(t, out, "--model"); got != model {
			t.Errorf("model %q reached the shell as %q (command: %s)", model, got, out)
		}
	}
}

func TestShellQuoteNeutralizesCommandSubstitution(t *testing.T) {
	quoted := shellQuote("x`touch /tmp/duo-should-not-exist`y")
	if strings.Contains(quoted, ";") || !strings.HasPrefix(quoted, "'") {
		t.Fatalf("shellQuote did not produce a single quoted word: %s", quoted)
	}
}

// The model id may itself contain a slash, so the provider prefix is the first
// segment only when there is exactly one. Truncating at the first slash made the
// command line and .duo/config.json disagree about the same model.
func TestProviderPrefixStrippedConsistently(t *testing.T) {
	cases := map[string]string{
		"google/gemini-3.8-flash-low":   "gemini-3.8-flash-low",
		"openrouter/anthropic/claude-4": "claude-4",
		"bare-model":                    "bare-model",
	}
	for input, want := range cases {
		if got := stripProviderPrefix(input); got != want {
			t.Errorf("stripProviderPrefix(%q) = %q, want %q", input, got, want)
		}
		// The command line and the persisted config must agree.
		if got := setAgyModel("agy", input); !strings.Contains(got, want) {
			t.Errorf("setAgyModel(%q) = %q, want it to carry %q", input, got, want)
		}
		if prefix, rest, found := strings.Cut(input, "/"); found {
			if got := setAgyModel("agy", input); strings.Contains(got, prefix) {
				t.Errorf("setAgyModel(%q) = %q still carries the provider prefix %q", input, got, prefix)
			} else if rest == "" {
				t.Errorf("stripProviderPrefix(%q) dropped everything", input)
			}
		}
	}
}

func TestOpencodeCommandLine(t *testing.T) {
	// A first run has no session id yet, so none is injected, but the agent must
	// still run unattended.
	s := NewOpencodeSession(Config{Agent: protocol.Austin, Command: "opencode"})
	out := s.commandLine()
	if !strings.Contains(out, "--auto") {
		t.Errorf("expected --auto so tool calls do not block: %s", out)
	}
	if strings.Contains(out, "--session") {
		t.Errorf("no session id is known yet, so none should be injected: %s", out)
	}

	// Once the id is known it is passed so a restart resumes the same session.
	s = NewOpencodeSession(Config{
		Agent:               protocol.Austin,
		Command:             "opencode",
		OpencodeSessionID:   "ses_abc123",
		OpencodeSessionFile: filepath.Join(t.TempDir(), "s.session"),
	})
	out = s.commandLine()
	if got := shellArgValue(t, out, "--session"); got != "ses_abc123" {
		t.Errorf("--session = %q, want ses_abc123 (command: %s)", got, out)
	}
	if strings.Count(out, "--auto") != 1 {
		t.Errorf("--auto must be injected exactly once: %s", out)
	}

	// An operator's own --session wins.
	s = NewOpencodeSession(Config{Agent: protocol.Austin, Command: "opencode --session theirs", OpencodeSessionID: "ses_abc123"})
	if out := s.commandLine(); strings.Contains(out, "ses_abc123") {
		t.Errorf("Duo should not override the operator's --session: %s", out)
	}
}

func TestOpencodeSessionIDLearnedFromPlugin(t *testing.T) {
	file := filepath.Join(t.TempDir(), "session.id")
	s := NewOpencodeSession(Config{Agent: protocol.Austin, Command: "opencode", OpencodeSessionFile: file})
	if s.OpencodeSessionID() != "" {
		t.Fatalf("expected no session id before the plugin reports one, got %q", s.OpencodeSessionID())
	}

	ctx, cancel := contextWithTimeout(5 * time.Second)
	defer cancel()
	go s.watchOpencodeSession(ctx)

	// Garbage must be ignored; only a real opencode id is accepted.
	if err := os.WriteFile(file, []byte("not-a-session\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	if s.OpencodeSessionID() != "" {
		t.Fatalf("garbage must not be accepted as a session id, got %q", s.OpencodeSessionID())
	}

	if err := os.WriteFile(file, []byte("ses_reported123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if s.OpencodeSessionID() == "ses_reported123" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session id was not picked up, got %q", s.OpencodeSessionID())
}

// shellArgValue runs a generated command line through /bin/sh and returns the
// argv value that follows flag, so tests assert what the agent actually receives
// rather than how Duo happened to spell the quoting.
func shellArgValue(t *testing.T, command, flag string) string {
	t.Helper()
	// Replace the agent binary with a printf that echoes the flag's value.
	script := "set -- " + command + "\nfor a in \"$@\"; do\n  if [ \"$prev\" = \"" + flag + "\" ]; then printf '%s' \"$a\"; exit 0; fi\n  prev=\"$a\"\ndone\nexit 1"
	out, err := exec.Command("sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("could not evaluate %q: %v", command, err)
	}
	return string(out)
}

func contextWithTimeout(d time.Duration) (ctx context.Context, cancel context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
