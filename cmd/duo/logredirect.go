package main

import (
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/atfa/duo/internal/sessionstore"
)

// redirectLog keeps the standard logger off the terminal for as long as the
// interface owns it. Components log through log.Printf — the remote client's
// room notices, for one — and a line written to stderr on top of the frame is
// wrapped by the terminal, which scrolls the whole interface up one row and
// leaves the text behind. The lines go to the repository's .duo/logs directory
// instead, beside the session transcripts, so they survive for diagnosis; when
// that file cannot be opened the output is discarded rather than handed back to
// stderr, because a stray log line is exactly what this function exists to
// prevent. The returned restore puts the previous writer back and closes the
// file.
func redirectLog(repoRoot, sessionID string) (restore func()) {
	out := log.Writer()
	file, err := openSessionLog(repoRoot, sessionID)
	if err != nil {
		log.SetOutput(io.Discard)
		return func() { log.SetOutput(out) }
	}
	log.SetOutput(file)
	return func() {
		log.SetOutput(out)
		_ = file.Close()
	}
}

// openSessionLog appends to .duo/logs/<session-id>.log. Appending keeps what an
// earlier run of the same session id logged, which is what a resumed session
// needs; the file sits next to the transcripts and never carries their name.
func openSessionLog(repoRoot, sessionID string) (*os.File, error) {
	repoRoot = strings.TrimSpace(repoRoot)
	if repoRoot == "" {
		return nil, errors.New("no repository root for the log file")
	}
	dir := filepath.Join(repoRoot, ".duo", sessionstore.LogsDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(dir, logFileName(sessionID)), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

// logFileName turns a session id into a file name. The id can arrive from
// `duo resume <id>`, so it is reduced to characters that cannot leave the log
// directory or name the transcripts' own files.
func logFileName(sessionID string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '-'
	}, strings.TrimSpace(sessionID))
	if name == "" {
		name = "duo"
	}
	return name + ".log"
}
