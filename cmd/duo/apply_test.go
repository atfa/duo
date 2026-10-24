package main

import "testing"

// TestParseApplyArgsBranchAndKeep pins the command line: the no-flag default is
// unchanged, and a new-branch delivery accepts the branch in both spellings.
func TestParseApplyArgsBranchAndKeep(t *testing.T) {
	got, err := parseApplyArgs([]string{"sess-1", "--branch", "feat/x", "--keep"})
	if err != nil {
		t.Fatal(err)
	}
	if got.sessionID != "sess-1" || got.branch != "feat/x" || !got.keep {
		t.Fatalf("parsed %+v", got)
	}

	got, err = parseApplyArgs([]string{"--branch=feat/x"})
	if err != nil {
		t.Fatal(err)
	}
	if got.sessionID != "" || got.branch != "feat/x" || got.keep {
		t.Fatalf("parsed %+v", got)
	}

	if _, err := parseApplyArgs([]string{"--branch"}); err == nil {
		t.Fatal("--branch without a name must be rejected")
	}
	if _, err := parseApplyArgs([]string{"--branch="}); err == nil {
		t.Fatal("an empty --branch must be rejected")
	}
	if _, err := parseApplyArgs([]string{"--nope"}); err == nil {
		t.Fatal("an unknown flag must be rejected")
	}
}
