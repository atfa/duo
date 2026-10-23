package main

import (
	"slices"
	"testing"
)

func TestNewSessionArgvStartsFresh(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []string
		want []string
	}{
		{"plain", []string{"duo"}, []string{"duo"}},
		{"flags kept", []string{"duo", "--mode", "step", "--driver", "pi"}, []string{"duo", "--mode", "step", "--driver", "pi"}},
		{"repo path kept", []string{"duo", "/home/me/project"}, []string{"duo", "/home/me/project"}},
		{"resume id dropped", []string{"duo", "resume", "20261023-132844-4ae2523f"}, []string{"duo"}},
		{"resume bare", []string{"duo", "resume"}, []string{"duo"}},
		{"resume id and flags", []string{"duo", "resume", "20261023-132844-4ae2523f", "--driver", "pi"}, []string{"duo", "--driver", "pi"}},
		{"resume with flag only", []string{"duo", "resume", "--driver", "pi"}, []string{"duo", "--driver", "pi"}},
		{"not a subcommand", []string{"duo", "sessions"}, []string{"duo", "sessions"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := slices.Clone(tc.in)
			got := newSessionArgv(tc.in)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("newSessionArgv(%v) = %v, want %v", tc.in, got, tc.want)
			}
			if !slices.Equal(tc.in, before) {
				t.Fatal("input mutated")
			}
		})
	}
}

func TestNewSessionEnvDropsPinnedSession(t *testing.T) {
	in := []string{"PATH=/usr/bin", "DUO_SESSION=20261023-132844-4ae2523f", "DUO_MODE=step"}
	got := newSessionEnv(in)
	want := []string{"PATH=/usr/bin", "DUO_MODE=step"}
	if !slices.Equal(got, want) {
		t.Fatalf("newSessionEnv = %v, want %v", got, want)
	}
	if len(in) != 3 {
		t.Fatal("input mutated")
	}
}
