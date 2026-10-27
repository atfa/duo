package sessionstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RecallNoteName is the file one recalled session leaves behind, beside the
// transcripts in the repository's .duo/logs directory.
//
// It is deliberately not one of the paths LogWriter.Remove deletes: the session
// it describes removes its own transcripts and record, and this note records the
// branch heads that removal deletes. Without it, the only pointer left to those
// commits is the local reflog.
func RecallNoteName(sessionID string) string { return sessionID + "-recall.txt" }

// WriteRecallNote writes the record of a recalled session, replacing an earlier
// one for the same id so a repeated recall cannot leave two notes that disagree
// about which branch heads were discarded.
func WriteRecallNote(repoRoot, sessionID, note string) error {
	repoRoot = strings.TrimSpace(repoRoot)
	sessionID = strings.TrimSpace(sessionID)
	if repoRoot == "" || sessionID == "" {
		return errors.New("a recall note needs a repository and a session id")
	}
	// A session id names a file, so a value that could leave the logs directory is
	// refused here rather than trusted: the store itself only accepts plain names.
	if !validName(sessionID) {
		return fmt.Errorf("recall note: %q is not a usable session id", sessionID)
	}
	dir := filepath.Join(repoRoot, ".duo", LogsDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, RecallNoteName(sessionID)), []byte(note), 0o644)
}
