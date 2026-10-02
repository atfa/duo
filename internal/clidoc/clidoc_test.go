package clidoc

import (
	"testing"
)

// parsedFlags is every flag spelling the duo parsers accept. The ast scan that
// derives it lives in cmd/duo/usage_test.go; this list is the same set, kept
// here so a signature cannot quietly drop one.
var parsedFlags = []string{
	"--agent", "--austin-agent", "--austin-driver", "--all", "--all-repos",
	"--config", "--driver", "--dry-run", "--export", "--export-config",
	"--force", "--help", "--host", "--mode", "--port", "--resume",
	"--session", "--test-cmd", "--token", "--tony-agent", "--tony-driver",
	"--version", "-a", "-f", "-h", "-m", "-n", "-r", "-s",
}

// TestSignaturesCarryEveryParsedFlag is the check that keeps a newly parsed flag
// from being prose-only: the token has to appear in a signature, which is what
// both `duo --help` and the in-app Help panel render.
func TestSignaturesCarryEveryParsedFlag(t *testing.T) {
	carried := map[string]bool{}
	for _, token := range Tokens() {
		carried[token] = true
	}
	for _, flag := range parsedFlags {
		if !carried[flag] {
			t.Errorf("clidoc signatures do not carry the parsed flag %s", flag)
		}
	}
}

func TestEveryCommandIsReachable(t *testing.T) {
	for _, name := range Names() {
		command, ok := Lookup(name)
		if !ok {
			t.Errorf("Lookup(%q) found nothing", name)
			continue
		}
		if command.Signature == "" || command.Summary == "" {
			t.Errorf("command %q has an empty signature or summary", name)
		}
	}
	if _, ok := Lookup("nope"); ok {
		t.Error("Lookup matched a token that is not a duo command")
	}
}
