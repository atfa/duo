package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// errNewSessionRequested is returned by runWith when the human used /new: quit
// preserving this session, then run duo again as a fresh session. main answers
// it by replacing this process with a new duo, so the terminal returns to a
// running Duo instead of a shell prompt.
var errNewSessionRequested = errors.New("duo new session requested")

// newSessionArgv rewrites the current command line for the re-run. `duo resume
// [id]` would reopen the session /new just preserved, which is the opposite of
// a new session, so the subcommand and its session id are dropped; flags and
// every other argument carry over, so the fresh session starts the way this one
// was launched.
func newSessionArgv(argv []string) []string {
	out := append([]string(nil), argv...)
	if len(out) > 1 && out[1] == "resume" {
		out = append(out[:1], out[2:]...)
		if len(out) > 1 && !strings.HasPrefix(out[1], "-") {
			out = append(out[:1], out[2:]...)
		}
	}
	return out
}

// newSessionEnv drops DUO_SESSION from the re-run's environment: it pins the
// session id, and a re-run that kept it would reuse the session /new preserved.
func newSessionEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "DUO_SESSION=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// relaunchDuo replaces this process with a fresh duo — the "run duo again" half
// of /new. Everything that had to be saved was saved by runWith before it
// returned, so exec needs no cleanup of its own. syscall.Exec wants a path, and
// os.Args[0] may be a bare name found through PATH, so the executable's own
// path is preferred and the name is resolved only as a fallback. On platforms
// without exec (Windows) the caller prints what to run and exits cleanly.
func relaunchDuo() error {
	exe, err := os.Executable()
	if err != nil {
		exe, err = exec.LookPath(os.Args[0])
		if err != nil {
			return err
		}
	}
	return syscall.Exec(exe, newSessionArgv(os.Args), newSessionEnv(os.Environ()))
}
