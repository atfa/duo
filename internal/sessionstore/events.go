package sessionstore

import (
	"bufio"
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

// TUIEntry is one persisted pane entry. It is intentionally separate from
// audit events: only tui_entry records are ever restored into the interface.
type TUIEntry struct {
	Time  time.Time `json:"time"`
	Pane  string    `json:"pane"`
	Text  string    `json:"text"`
	Error bool      `json:"error"`
	// Warning marks a notable but non-failing entry, such as a verifier's
	// issue_found verdict. It is an optional field: older logs decode as false.
	Warning bool `json:"warning,omitempty"`
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

// RecordTUIEntry records exactly what was added to a visible pane. It shares
// the session journal so there is no second session history format.
func (l *EventLog) RecordTUIEntry(entry TUIEntry) {
	if entry.Time.IsZero() {
		entry.Time = time.Now().UTC()
	}
	_ = l.Append(Event{Time: entry.Time, Type: "tui_entry", Fields: map[string]any{
		"pane":    entry.Pane,
		"text":    entry.Text,
		"error":   entry.Error,
		"warning": entry.Warning,
	}})
}

// TUIEntries returns the most recent 200 records for each pane. Older event
// logs and individual malformed lines are ignored so a damaged transcript
// cannot prevent a session from resuming.
func (l *EventLog) TUIEntries() []TUIEntry {
	f, err := os.Open(l.path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var out []TUIEntry
	counts := map[string]int{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4<<10), 1<<20)
	for scanner.Scan() {
		var raw struct {
			Time   time.Time `json:"time"`
			Type   string    `json:"type"`
			Fields struct {
				Pane    string `json:"pane"`
				Text    string `json:"text"`
				Error   bool   `json:"error"`
				Warning bool   `json:"warning"`
			} `json:"fields"`
		}
		if json.Unmarshal(scanner.Bytes(), &raw) != nil || raw.Type != "tui_entry" || !validTUIPane(raw.Fields.Pane) {
			continue
		}
		entry := TUIEntry{Time: raw.Time, Pane: raw.Fields.Pane, Text: raw.Fields.Text, Error: raw.Fields.Error, Warning: raw.Fields.Warning}
		out = append(out, entry)
		counts[entry.Pane]++
		if counts[entry.Pane] > 200 {
			for i, candidate := range out {
				if candidate.Pane == entry.Pane {
					out = append(out[:i], out[i+1:]...)
					break
				}
			}
			counts[entry.Pane]--
		}
	}
	return out
}

func validTUIPane(pane string) bool {
	return pane == "Austin" || pane == "Tony" || pane == "Duo"
}
