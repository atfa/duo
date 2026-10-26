//go:build !windows

package sessionstore

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes an advisory lock on the session lock file: exclusive for the
// owning Duo, shared for a reader asking whether a session is active. It reports
// errLockBusy when another process holds a conflicting lock.
func lockFile(f *os.File, exclusive bool) error {
	op := unix.LOCK_SH
	if exclusive {
		op = unix.LOCK_EX
	}
	err := unix.Flock(int(f.Fd()), op|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return errLockBusy
	}
	return err
}

// unlockFile releases the flock. Closing the file would also do it, but an
// explicit unlock keeps Release honest about which step failed.
func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
