package sessionstore

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Logger appends human-readable runtime diagnostics to duo.log. Values
// registered with AddSecret are masked, so tokens and API keys never reach the
// log even if they are accidentally formatted into a message.
type Logger struct {
	mu      sync.Mutex
	path    string
	secrets []string
}

func (s *Store) OpenLog() *Logger { return &Logger{path: s.LogPath()} }

func (l *Logger) Path() string { return l.path }

// AddSecret registers a literal value that must never appear in the log.
func (l *Logger) AddSecret(secret string) {
	if strings.TrimSpace(secret) == "" {
		return
	}
	l.mu.Lock()
	l.secrets = append(l.secrets, secret)
	l.mu.Unlock()
}

// Printf appends a timestamped, secret-redacted line. Write failures are
// swallowed: logging must never break the session.
func (l *Logger) Printf(format string, args ...any) {
	message := fmt.Sprintf(format, args...)

	l.mu.Lock()
	defer l.mu.Unlock()

	for _, secret := range l.secrets {
		message = strings.ReplaceAll(message, secret, "[redacted]")
	}
	line := time.Now().UTC().Format(time.RFC3339) + "  " + message + "\n"

	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}
