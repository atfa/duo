package main

import (
	"strings"
	"testing"

	"github.com/atfa/duo/internal/project"
)

func TestParseArgsModeFlags(t *testing.T) {
	for _, tc := range []struct {
		args     []string
		mode     string
		explicit bool
	}{
		{[]string{"--mode", "goal"}, "goal", true},
		{[]string{"-m", "fast"}, "fast", true},
		{[]string{"--mode=goal"}, "goal", true},
		{[]string{"-m=fast"}, "fast", true},
		{[]string{"."}, "", false},
	} {
		got, err := parseArgs(tc.args)
		if err != nil {
			t.Fatalf("parseArgs(%v): %v", tc.args, err)
		}
		if got.mode != tc.mode || got.modeExplicit != tc.explicit {
			t.Fatalf("parseArgs(%v) = %q/%t, want %q/%t", tc.args, got.mode, got.modeExplicit, tc.mode, tc.explicit)
		}
	}
	if _, err := parseArgs([]string{"--mode"}); err == nil {
		t.Fatal("--mode without a value must be an error")
	}
}

// TestResolveNewModePrecedence pins CLI > DUO_MODE > built-in default (fast).
func TestResolveNewModePrecedence(t *testing.T) {
	t.Setenv("DUO_MODE", "goal")
	mode, source, err := resolveNewMode(cliArgs{})
	if err != nil || mode != project.ModeGoal || source != "env" {
		t.Fatalf("env mode = %q/%q/%v, want goal/env", mode, source, err)
	}

	// An explicit flag wins over the ambient environment.
	mode, source, err = resolveNewMode(cliArgs{mode: "fast", modeExplicit: true})
	if err != nil || mode != project.ModeFast || source != "cli" {
		t.Fatalf("cli mode = %q/%q/%v, want fast/cli", mode, source, err)
	}

	// With nothing set, a brand-new session is Fast.
	t.Setenv("DUO_MODE", "")
	mode, source, err = resolveNewMode(cliArgs{})
	if err != nil || mode != project.DefaultMode || source != "default" {
		t.Fatalf("default mode = %q/%q/%v, want %q/default", mode, source, err, project.DefaultMode)
	}

	// An invalid explicit value is an error, never a silent fallback.
	if _, _, err := resolveNewMode(cliArgs{mode: "auto", modeExplicit: true}); err == nil {
		t.Fatal("an invalid --mode must be an error")
	}
	// An invalid ambient value is also an error for a new session.
	t.Setenv("DUO_MODE", "auto")
	if _, _, err := resolveNewMode(cliArgs{}); err == nil {
		t.Fatal("an invalid DUO_MODE must be an error for a new session")
	}
}

// TestResumeModeRules pins mode as a per-session property: the persisted mode
// always wins, an explicit mismatch is an operator error, and an ambient
// DUO_MODE can only ever warn.
func TestResumeModeRules(t *testing.T) {
	t.Setenv("DUO_MODE", "goal")

	if _, err := resumeMode(config{modeRaw: "fast", modeExplicit: true}, project.ModeGoal, "s1", func(string) {}); err == nil {
		t.Fatal("an explicit --mode that disagrees with the persisted mode must be an error")
	}
	mode, err := resumeMode(config{modeRaw: "goal", modeExplicit: true}, project.ModeGoal, "s1", func(string) {})
	if err != nil || mode != project.ModeGoal {
		t.Fatalf("matching explicit mode = %q/%v", mode, err)
	}

	// An exported environment must never break resume: warn and keep the session.
	var warnings []string
	mode, err = resumeMode(config{}, project.ModeFast, "s1", func(m string) { warnings = append(warnings, m) })
	if err != nil || mode != project.ModeFast {
		t.Fatalf("ambient conflict = %q/%v, want persisted fast", mode, err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "DUO_MODE") || !strings.Contains(warnings[0], "s1") {
		t.Fatalf("ambient conflict warnings = %v", warnings)
	}

	t.Setenv("DUO_MODE", "auto")
	warnings = nil
	if _, err := resumeMode(config{}, project.ModeGoal, "s1", func(m string) { warnings = append(warnings, m) }); err != nil {
		t.Fatalf("an invalid ambient DUO_MODE must not break resume: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected one warning for an invalid DUO_MODE, got %v", warnings)
	}

	t.Setenv("DUO_MODE", "")
	warnings = nil
	mode, err = resumeMode(config{}, project.ModeGoal, "s1", func(m string) { warnings = append(warnings, m) })
	if err != nil || mode != project.ModeGoal || len(warnings) != 0 {
		t.Fatalf("silent resume = %q/%v/%v", mode, err, warnings)
	}
}
