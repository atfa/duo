package main

import "testing"

func TestFreshSessionDropsTheResumedSession(t *testing.T) {
	cfg := config{resume: true, resumeSession: "20261023-132844-4ae2523f", session: "old", repository: "/repo", testCommand: "make check"}
	got := cfg.freshSession()
	if got.resume || got.resumeSession != "" {
		t.Fatalf("fresh session still resumes: %+v", got)
	}
	if got.session == "" || got.session == "old" {
		t.Fatalf("fresh session id = %q, want a new one", got.session)
	}
	if got.repository != "/repo" || got.testCommand != "make check" {
		t.Fatalf("fresh session lost launch settings: %+v", got)
	}
	if !cfg.resume || cfg.session != "old" {
		t.Fatal("freshSession mutated the receiver")
	}
}
