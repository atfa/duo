//go:build windows

package sessionstore

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockRangeOffset is the byte range the Windows lock is taken on. It sits past any
// holder metadata readHolder reads, because LockFileEx on Windows locks byte ranges
// for writers *and* readers: a lock on offset 0 would stop the Duo that lost the
// race from reading the holder it is about to report. Unix flock has no such
// limitation, so this constant lives with the implementation that needs it.
const lockRangeOffset = 4096

func lockRange() *windows.Overlapped {
	return &windows.Overlapped{Offset: lockRangeOffset, OffsetHigh: 0}
}

// lockFile takes an advisory lock on the session lock file: exclusive for the
// owning Duo, shared for a reader asking whether a session is active. It reports
// errLockBusy when another process holds a conflicting lock.
func lockFile(f *os.File, exclusive bool) error {
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	// One byte is enough: every Duo locks the same byte, so the range is the whole
	// protocol. Its size says nothing about what is protected.
	err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, lockRange())
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errLockBusy
	}
	return err
}

// unlockFile releases the lock. Closing the file would also do it, but an explicit
// unlock keeps Release honest about which step failed.
func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, lockRange())
}
