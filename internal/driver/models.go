package driver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ModelListTimeout bounds a catalog read. Listing models starts another CLI,
// which can take seconds to launch, so this is generous for the slowest agent but
// short enough that a wedged plugin cannot freeze the model picker.
const ModelListTimeout = 20 * time.Second

// ModelParser turns a CLI's model-listing output into the protocol's catalog
// shape. Every driver prints a different format, which is why this is a
// plugin-side function and not something Core attempts to normalise.
type ModelParser func(output string) ([]Model, error)

// RunModelList runs a driver's own model-listing command and parses its output.
// It lives in the SDK because the process handling is identical for every driver
// and getting it wrong is the same failure each time: a hung picker, or an error
// with the CLI's own complaint nowhere in it.
func RunModelList(ctx context.Context, command string, parse ModelParser) (*ModelList, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, fmt.Errorf("list models: no model-listing command")
	}
	if parse == nil {
		return nil, fmt.Errorf("list models: no parser")
	}
	ctx, cancel := context.WithTimeout(ctx, ModelListTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-lc", command)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// A listing that ran out of time says the machine was busy, not that the
		// plugin is wrong, so it is reported as a timeout rather than as whatever
		// progress text happened to be on stderr.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("list models: %s did not finish within %s: %w", command, ModelListTimeout, context.DeadlineExceeded)
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("list models: %s", detail)
	}
	list, err := parse(stdout.String())
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("no models reported by %q", command)
	}
	return &ModelList{Models: list}, nil
}
