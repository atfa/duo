package main

import (
	"strings"
	"testing"

	"github.com/atfa/duo/internal/sessionstore"
)

func TestChooseResumeSession(t *testing.T) {
	items := []sessionstore.Summary{{SessionID: "first"}, {SessionID: "second"}}
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
}
