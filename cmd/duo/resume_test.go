package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/sessionstore"
)

func TestChooseResumeSession(t *testing.T) {
	items := []sessionstore.Summary{
		{SessionID: "first", Snapshot: sessionstore.Snapshot{Phase: "EXECUTE"}},
		{SessionID: "second", Snapshot: sessionstore.Snapshot{Phase: "DONE"}},
	}
	for _, tc := range []struct {
		input string
		want  string
		bad   bool
	}{
		{input: "1\n", want: "first"},
		{input: "second\n", want: "second"},
		{input: "3\n", bad: true},
		{input: "\n", bad: true},
	} {
		got, err := chooseResumeSession(items, strings.NewReader(tc.input), &strings.Builder{})
		if (err != nil) != tc.bad {
			t.Fatalf("chooseResumeSession(%q) error = %v, want error=%t", tc.input, err, tc.bad)
		}
		if got != tc.want {
			t.Fatalf("chooseResumeSession(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
	var listing strings.Builder
	if _, err := chooseResumeSession(items, strings.NewReader("2\n"), &listing); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"status=unfinished", "status=completed", "DONE"} {
		if !strings.Contains(listing.String(), want) {
			t.Errorf("session listing %q does not contain %q", listing.String(), want)
		}
	}
}

func TestResumeChoicesIncludeCompletedSessions(t *testing.T) {
	available, broken := resumeChoices([]sessionstore.Summary{
		{SessionID: "in-progress", Snapshot: sessionstore.Snapshot{Phase: "EXECUTE"}},
		{SessionID: "completed", Snapshot: sessionstore.Snapshot{Phase: "DONE"}},
	})
	if len(broken) != 0 || len(available) != 2 || available[1].SessionID != "completed" {
		t.Fatalf("resume choices = %#v, broken=%#v; want both sessions available", available, broken)
	}
}

func TestResumeChoicesReportBrokenWithoutMakingThemSelectable(t *testing.T) {
	items := []sessionstore.Summary{
		{SessionID: "bad", Err: errors.New("corrupt state")},
		{SessionID: "only-choice", Snapshot: sessionstore.Snapshot{Phase: "EXECUTE"}},
		{SessionID: "second-choice", Snapshot: sessionstore.Snapshot{Phase: "EXECUTE"}},
	}
	available, broken := resumeChoices(items)
	if len(available) != 2 || available[0].SessionID != "only-choice" || available[1].SessionID != "second-choice" {
		t.Fatalf("available = %#v; unreadable session changed choice numbering", available)
	}
	if len(broken) != 1 || broken[0].SessionID != "bad" {
		t.Fatalf("broken = %#v, want bad session diagnostics", broken)
	}
	for _, input := range []string{"bad\n", "3\n"} {
		if got, err := chooseResumeSession(available, strings.NewReader(input), &strings.Builder{}); err == nil || got != "" {
			t.Fatalf("unreadable session was selectable from %q: id=%q err=%v", input, got, err)
		}
	}
	var diagnostics strings.Builder
	writeResumeDiagnostics(&diagnostics, broken)
	if !strings.Contains(diagnostics.String(), "bad") || !strings.Contains(diagnostics.String(), "corrupt state") {
		t.Fatalf("broken session diagnostics = %q", diagnostics.String())
	}
}
