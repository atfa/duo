package sessionstore

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Event is one line of the JSONL journal: an append-only audit/diagnostics
// trail. The snapshot stays the authoritative checkpoint; events are recorded
// for humans and tests and are never replayed to rebuild state.
type Event struct {
	Time   time.Time      `json:"time"`
	Type   string         `json:"type"`
	Fields map[string]any `json:"fields,omitempty"`
}

// EventLog appends events to events.jsonl.
type EventLog struct {
	mu   sync.Mutex
	path string
}

func (s *Store) OpenEvents() *EventLog { return &EventLog{path: s.EventsPath()} }

func (l *EventLog) Path() string { return l.path }

// Append writes one JSON object followed by a newline. Each append opens the
// file with O_APPEND, so a crash cannot truncate earlier events.
func (l *EventLog) Append(event Event) error {
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	line = append(line, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()

	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open event log: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("append event log: %w", err)
	}
	return nil
}

// Record appends an event, ignoring write errors: the journal must never break
// the session it describes.
func (l *EventLog) Record(eventType string, fields map[string]any) {
	_ = l.Append(Event{Type: eventType, Fields: fields})
}
