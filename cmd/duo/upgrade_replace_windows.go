//go:build windows

package main

import (
	"fmt"
	"os"
)

// replaceBinary puts the fully written and fsynced temporary binary in place of the
// installed one.
//
// Windows refuses to overwrite the image of a running process, but it does allow
// that image to be renamed. The running binary is therefore moved aside first, and
// the next upgrade removes the leftover — by then nothing holds it open. If the new
// binary cannot be moved into place, the old one is put back, so a failed upgrade
// never leaves an installation without a working duo.exe.
func replaceBinary(tmp, target string) error {
	stale := target + ".old"
	_ = os.Remove(stale)
	if err := os.Rename(target, stale); err != nil {
		return fmt.Errorf("move the running binary aside: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Rename(stale, target)
		return fmt.Errorf("replace %s: %w", target, err)
	}
	return nil
}
