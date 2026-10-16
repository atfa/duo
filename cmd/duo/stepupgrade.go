package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/version"
)

// upgradeExitCode is what `duo` exits with when the session asked to continue on
// a new binary. It is a sentinel, not an error: the session is preserved and
// resumable, and the process that should act on it is the launcher, because the
// agent that asked is a child of the Duo being replaced.
const upgradeExitCode = 77

// errUpgradeRequested is returned by run() so main can exit with the sentinel code
// without logging the request as a failure.
var errUpgradeRequested = errors.New("duo restart requested")

// upgradeRequest is the durable handoff from the Duo that stopped to the launcher
// that resumes the session. The session id is the part that cannot be guessed: a
// session id is generated per session, and `duo resume` without one only works in
// the repository it was started from.
type upgradeRequest struct {
	SessionID     string    `json:"sessionId"`
	Repository    string    `json:"repository"`
	Reason        string    `json:"reason"`
	Install       bool      `json:"install"`
	SchemaVersion int       `json:"schemaVersion"`
	DuoVersion    string    `json:"duoVersion"`
	Step          string    `json:"step,omitempty"`
	At            time.Time `json:"at"`
}

// upgradeRequestPath is where the request is written: the repository's .duo
// directory, which already exists and is gitignored.
func upgradeRequestPath(repo string) string {
	return filepath.Join(repo, ".duo", "upgrade.json")
}

// writeUpgradeRequest records why this Duo stopped and what the launcher must do.
// It is written before the process exits, so a launcher that sees the exit code
// always finds the session it must resume. A write failure is reported and does
// not become a second failure: the exit code alone still preserves the session.
func writeUpgradeRequest(repo, sessionID, reason, step string, install bool) error {
	req := upgradeRequest{
		SessionID:     sessionID,
		Repository:    repo,
		Reason:        reason,
		Install:       install,
		SchemaVersion: sessionstore.SchemaVersion,
		DuoVersion:    version.Version,
		Step:          step,
		At:            time.Now().UTC(),
	}
	data, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(upgradeRequestPath(repo)), 0o755); err != nil {
		return err
	}
	tmp := upgradeRequestPath(repo) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, upgradeRequestPath(repo))
}

func readUpgradeRequest(repo string) (upgradeRequest, error) {
	data, err := os.ReadFile(upgradeRequestPath(repo))
	if err != nil {
		return upgradeRequest{}, fmt.Errorf("no pending Duo upgrade request in %s: %w", repo, err)
	}
	var req upgradeRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return upgradeRequest{}, fmt.Errorf("read Duo upgrade request: %w", err)
	}
	if strings.TrimSpace(req.SessionID) == "" {
		return upgradeRequest{}, fmt.Errorf("Duo upgrade request %s has no session id", upgradeRequestPath(repo))
	}
	return req, nil
}

// runStepLoop is `duo step-loop`: it runs the session, and every time Duo exits
// with the upgrade code it installs the newly delivered binary and resumes the
// session on it, so one task can run as a queue of steps with no human between
// them.
//
// The loop is the only process that survives an upgrade. It must therefore be
// started by the human, not from inside the session: every agent is a child of the
// Duo being replaced and dies with it.
func runStepLoop(ctx context.Context, args []string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find the Duo binary to re-run: %w", err)
	}
	round, sessionArgs := 1, args
	for {
		fmt.Printf("duo step-loop: round %d — running `duo %s`\n", round, strings.Join(sessionArgs, " "))
		cmd := exec.CommandContext(ctx, self, sessionArgs...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		runErr := cmd.Run()
		code := exitCodeOf(runErr)

		switch {
		case code == 0:
			fmt.Println("duo step-loop: the session finished.")
			return nil
		case ctx.Err() != nil:
			return nil
		case code != upgradeExitCode:
			if runErr != nil {
				return runErr
			}
			return fmt.Errorf("duo exited with code %d", code)
		}

		repo, err := os.Getwd()
		if err != nil {
			return err
		}
		req, err := readUpgradeRequest(repo)
		if err != nil {
			return err
		}
		fmt.Printf("duo step-loop: upgrade requested (%s) at step %s\n", req.Reason, req.Step)

		if req.Install {
			if err := installFromRepo(ctx, req.Repository); err != nil {
				return fmt.Errorf("install the new Duo: %w", err)
			}
		}
		// The new binary must be able to read this session's state. A schema bump
		// makes the snapshot unreadable, and installing it anyway would leave a
		// session that neither binary can continue; the loop stops with a message
		// instead of doing that.
		if err := checkSchemaCompatible(req.Repository); err != nil {
			return err
		}

		sessionArgs = []string{"resume", req.SessionID}
		round++
	}
}

// installFromRepo builds and installs Duo from the repository's own checkout,
// which is the branch the steps are landing on. Installing from the delivered
// branch, never from an agent's feature worktree, is what keeps the running
// coordinator on code the user actually received.
func installFromRepo(ctx context.Context, repo string) error {
	script := filepath.Join(repo, "scripts", "install.sh")
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("no installer at %s; install Duo yourself and let step-loop restart it: %w", script, err)
	}
	fmt.Printf("duo step-loop: installing from %s\n", repo)
	cmd := exec.CommandContext(ctx, script)
	cmd.Dir = repo
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", script, err)
	}
	return nil
}

// schemaVersionInSource reads the persisted snapshot schema out of a checkout of
// the code the next binary will be built from. Reading the constant the new binary
// will compile is the only pre-install check that cannot itself need the new
// binary to run.
var schemaVersionInSource = regexp.MustCompile(`(?m)^\s*const SchemaVersion = (\d+)\s*$`)

func checkSchemaCompatible(repo string) error {
	path := filepath.Join(repo, "internal", "sessionstore", "snapshot.go")
	data, err := os.ReadFile(path)
	if err != nil {
		// No source to inspect: the loop cannot judge, and refusing to resume a
		// preserved session would be worse than trying. The new Duo reports a
		// schema mismatch loudly on its own.
		return nil
	}
	m := schemaVersionInSource.FindSubmatch(data)
	if m == nil {
		return nil
	}
	next, err := strconv.Atoi(string(m[1]))
	if err != nil {
		return nil
	}
	if next != sessionstore.SchemaVersion {
		return fmt.Errorf(
			"refusing to continue: the delivered code writes snapshot schema %d but this session is schema %d in %s; the session is preserved, so resume it with a Duo built from the schema it was written by",
			next, sessionstore.SchemaVersion, path,
		)
	}
	return nil
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}
