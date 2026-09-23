package main

import (
	"regexp"
	"testing"
	"time"

	"github.com/atfa/duo/internal/protocol"
)

func TestSessionID(t *testing.T) {
	t.Setenv("DUO_SESSION", "")
	first, err := loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	a, b := first.session, second.session
	if a == b || !regexp.MustCompile(`^\d{8}-\d{6}-[0-9a-f]{8}$`).MatchString(a) {
		t.Fatalf("session IDs %q %q", a, b)
	}
	t.Setenv("DUO_SESSION", "abc")
	got, err := loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.session != "abc" {
		t.Fatalf("explicit session = %q", got.session)
	}
}

func TestParseResumeArgs(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		repo      string
		resume    bool
		sessionID string
	}{
		{name: "plain", args: nil, resume: false},
		{name: "repo only", args: []string{"/tmp/repo"}, repo: "/tmp/repo"},
		{name: "resume latest", args: []string{"--resume"}, resume: true},
		{name: "resume latest with repo", args: []string{"/tmp/repo", "--resume"}, repo: "/tmp/repo", resume: true},
		{name: "resume id", args: []string{"--resume", "20260101-000000-abcdef01"}, resume: true, sessionID: "20260101-000000-abcdef01"},
		{name: "resume equals", args: []string{"--resume=abc"}, resume: true, sessionID: "abc"},
		{name: "short resume", args: []string{"-r", "abc"}, resume: true, sessionID: "abc"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseArgs(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if got.repository != tc.repo || got.resume != tc.resume || got.sessionID != tc.sessionID {
				t.Fatalf("parseArgs(%v) = %+v, want repo=%q resume=%t sessionID=%q",
					tc.args, got, tc.repo, tc.resume, tc.sessionID)
			}
		})
	}
}

func TestParseResumeArgsRejectsUnknownFlag(t *testing.T) {
	if _, err := parseArgs([]string{"--nope"}); err == nil {
		t.Fatal("expected an error for an unknown flag")
	}
}

func TestResumeAddsHarnessGrace(t *testing.T) {
	fresh, err := loadConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.harness.RecoveryGrace != 0 {
		t.Fatalf("fresh session should not have a recovery grace, got %s", fresh.harness.RecoveryGrace)
	}

	resumed, err := loadConfig([]string{"--resume"})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.harness.RecoveryGrace < 30*time.Second || resumed.harness.RecoveryGrace > 60*time.Second {
		t.Fatalf("resume grace %s is outside the required 30-60s window", resumed.harness.RecoveryGrace)
	}
}

func TestPiSessionIDsAreStableAndDistinct(t *testing.T) {
	first, err := piSessionIDs(nil)
	if err != nil {
		t.Fatal(err)
	}
	austin, tony := first[protocol.Austin], first[protocol.Tony]
	if austin == "" || tony == "" {
		t.Fatalf("generated ids must not be empty: %+v", first)
	}
	if austin == tony {
		t.Fatalf("Austin and Tony must never share a Pi session id: %q", austin)
	}

	reused, err := piSessionIDs(first)
	if err != nil {
		t.Fatal(err)
	}
	if reused[protocol.Austin] != austin || reused[protocol.Tony] != tony {
		t.Fatalf("persisted ids must be reused: got %+v want %+v", reused, first)
	}

	// A duplicated pair must be repaired rather than silently shared.
	repaired, err := piSessionIDs(map[protocol.AgentID]string{
		protocol.Austin: "same",
		protocol.Tony:   "same",
	})
	if err != nil {
		t.Fatal(err)
	}
	if repaired[protocol.Austin] == repaired[protocol.Tony] {
		t.Fatalf("duplicate ids were not repaired: %+v", repaired)
	}

	// A missing side is generated without disturbing the other.
	partial, err := piSessionIDs(map[protocol.AgentID]string{protocol.Austin: "keep-me"})
	if err != nil {
		t.Fatal(err)
	}
	if partial[protocol.Austin] != "keep-me" || partial[protocol.Tony] == "" || partial[protocol.Tony] == "keep-me" {
		t.Fatalf("partial ids handled incorrectly: %+v", partial)
	}
}
