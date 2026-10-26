package main

import (
	"context"
	"os"
	"strings"
	"testing"
)

// A headless round has no interface and no human, so its only first-run check
// is the driver refusal: with no agent CLI anywhere on PATH, `duo run` refuses
// before touching Git or configuration, and names what to install.
func TestRunHeadlessRefusesWithoutAnyDriverCLI(t *testing.T) {
	original := guideDrivers
	guideDrivers = func(context.Context) ([]driverStatus, error) {
		return []driverStatus{
			{Name: "pi", Reason: "exec: not found"},
			{Name: "codex", Reason: "exec: not found"},
			{Name: "opencode", Reason: "exec: not found"},
			{Name: "agy", Reason: "exec: not found"},
		}, nil
	}
	defer func() { guideDrivers = original }()

	// A task argument keeps parseRunArgs from treating this as --help: the
	// refusal must fire before any session work, in an empty directory.
	dir := t.TempDir()
	chdir(t, dir)
	err := runHeadless(context.Background(), []string{"do the thing"})
	if err == nil {
		t.Fatal("runHeadless without any driver CLI should refuse")
	}
	if !strings.Contains(err.Error(), "Install the latest version") {
		t.Errorf("refusal does not name the fix:\n%s", err.Error())
	}
	for _, name := range []string{"pi", "codex", "opencode", "agy"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("refusal does not list driver %q:\n%s", name, err.Error())
		}
	}
}

// One usable CLI is enough: the refusal is about having nobody to collaborate
// with, not about every driver being present.
func TestRunHeadlessProceedsWithOneDriverCLI(t *testing.T) {
	original := guideDrivers
	guideDrivers = func(context.Context) ([]driverStatus, error) {
		return []driverStatus{
			{Name: "pi", CLIPath: "/usr/bin/pi"},
			{Name: "codex", Reason: "exec: not found"},
			{Name: "opencode", Reason: "exec: not found"},
			{Name: "agy", Reason: "exec: not found"},
		}, nil
	}
	defer func() { guideDrivers = original }()

	// An empty directory stops the round right after the driver check with the
	// usual no-repository error: the point is that the refusal does not fire.
	dir := t.TempDir()
	chdir(t, dir)
	err := runHeadless(context.Background(), []string{"do the thing"})
	if err == nil || strings.Contains(err.Error(), "Install the latest version") {
		t.Fatalf("one driver CLI should pass the gate and fail later instead: %v", err)
	}
}

// chdir moves the test into dir and restores the old directory at cleanup.
func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
}
