package sessionstore

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// LockHolder describes the process recorded in a lock file. It is informational
// only: the kernel advisory lock, not the file contents, decides ownership.
type LockHolder struct {
	PID      int
	Started  string
	Hostname string
}

// LockedError reports that another live Duo process owns this session.
type LockedError struct {
	Path   string
	Holder LockHolder
}

func (e *LockedError) Error() string {
	if e.Holder.PID == 0 {
		return fmt.Sprintf("Duo session is already active (lock %s)", e.Path)
	}
	return fmt.Sprintf(
		"Duo session is already active: PID %d on %s since %s",
		e.Holder.PID, e.Holder.Hostname, e.Holder.Started,
	)
}

// Lock is a held exclusive session lock.
type Lock struct {
	file *os.File
	path string
}

// Lock takes an exclusive advisory lock on the session lock file. The operating
// system releases the flock when the process exits, so a crashed Duo never
// leaves a blocking stale lock and PID reuse cannot forge ownership. The PID
// metadata written into the file is used only to produce a helpful message.
func (s *Store) Lock() (*Lock, error) {
	path := s.LockPath()
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open session lock: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		holder := readHolder(path)
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, &LockedError{Path: path, Holder: holder}
		}
		return nil, fmt.Errorf("lock session: %w", err)
	}

	host, _ := os.Hostname()
	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	_, _ = fmt.Fprintf(f, "%d\n%s\n%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339), host)
	_ = f.Sync()

	return &Lock{file: f, path: path}, nil
}

func (l *Lock) Path() string { return l.path }

// Release drops the lock. It is safe to call on a nil lock.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func readHolder(path string) LockHolder {
	data, err := os.ReadFile(path)
	if err != nil {
		return LockHolder{}
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var holder LockHolder
	if len(lines) > 0 {
		holder.PID, _ = strconv.Atoi(strings.TrimSpace(lines[0]))
	}
	if len(lines) > 1 {
		holder.Started = strings.TrimSpace(lines[1])
	}
	if len(lines) > 2 {
		holder.Hostname = strings.TrimSpace(lines[2])
	}
	return holder
}
