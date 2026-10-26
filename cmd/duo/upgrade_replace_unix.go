//go:build !windows

package main

import (
	"fmt"
	"os"
)

// replaceBinary puts the fully written and fsynced temporary binary in place of the
// installed one. On Unix a rename over the running executable is atomic and leaves
// the running process on its old image, which is exactly the handoff `duo upgrade`
// documents.
func replaceBinary(tmp, target string) error {
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("replace %s: %w", target, err)
	}
	return nil
}
