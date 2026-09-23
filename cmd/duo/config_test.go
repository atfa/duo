package main

import (
	"regexp"
	"testing"
)

func TestSessionID(t *testing.T) {
	t.Setenv("DUO_SESSION", "")
	a, b := loadConfig(nil).session, loadConfig(nil).session
	if a == b || !regexp.MustCompile(`^\d{8}-\d{6}-[0-9a-f]{8}$`).MatchString(a) {
		t.Fatalf("session IDs %q %q", a, b)
	}
	t.Setenv("DUO_SESSION", "abc")
	if got := loadConfig(nil).session; got != "abc" {
		t.Fatalf("explicit session = %q", got)
	}
}
