package observer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestTitleAsksForANameAndNormalizesIt covers the whole title path: the request
// carries the task the session was given, asks for a name rather than a report,
// and the answer is cleaned up before a frontend ever shows it.
func TestTitleAsksForANameAndNormalizesIt(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")

	var (
		mu      sync.Mutex
		system  string
		user    string
		tokens  float64
		request = 0
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			MaxTokens float64 `json:"max_tokens"`
			Messages  []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &payload)
		mu.Lock()
		request++
		tokens = payload.MaxTokens
		if len(payload.Messages) == 2 {
			system, user = payload.Messages[0].Content, payload.Messages[1].Content
		}
		mu.Unlock()
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"## \"Add session titles.\"\nsecond line"}}]}`)
	}))
	defer srv.Close()

	obs, _ := newTestObserver(t, testConfig(srv.URL+"/v1"), "")
	title, err := obs.Title(context.Background(), "  give sessions a title  ")
	if err != nil {
		t.Fatalf("Title: %v", err)
	}
	if title != "Add session titles" {
		t.Fatalf("Title = %q, want the cleaned first line", title)
	}
	mu.Lock()
	defer mu.Unlock()
	if request != 1 {
		t.Fatalf("model requests = %d, want 1", request)
	}
	if tokens <= 0 || tokens > 128 {
		t.Fatalf("title max_tokens = %v, want a small positive budget", tokens)
	}
	if strings.Contains(system, "status report") || !strings.Contains(system, "title") {
		t.Fatalf("title system prompt = %q, want a naming instruction, not the report prompt", system)
	}
	if !strings.Contains(user, "give sessions a title") || strings.Contains(user, "Current state") {
		t.Fatalf("title user prompt = %q, want just the human's task", user)
	}
}

// TestTitleFailureIsReportedNotInvented keeps the title advisory: an unusable
// answer is an error the session logs, never a made-up name or a stale report
// used as continuity for the next request.
func TestTitleFailureIsReportedNotInvented(t *testing.T) {
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"   "}}]}`)
	}))
	defer srv.Close()

	obs, _ := newTestObserver(t, testConfig(srv.URL+"/v1"), "")
	if title, err := obs.Title(context.Background(), "do the thing"); err == nil || title != "" {
		t.Fatalf("Title = (%q, %v), want an error and no title", title, err)
	}
	obs.mu.Lock()
	previous := obs.previous
	obs.mu.Unlock()
	if previous != "" {
		t.Fatalf("a title became report continuity: %q", previous)
	}
	if _, err := obs.Title(context.Background(), "   "); err == nil {
		t.Fatal("an empty task asked the model for a title")
	}
}

func TestNormalizeTitle(t *testing.T) {
	long := strings.Repeat("词", maxTitleRunes+20)
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "plain", raw: "Add session titles", want: "Add session titles"},
		{name: "quoted", raw: `"Add session titles"`, want: "Add session titles"},
		{name: "backticked with full stop", raw: "`Add session titles.`", want: "Add session titles"},
		{name: "markdown heading", raw: "### Session titles", want: "Session titles"},
		{name: "bullet", raw: "- 会话标题", want: "会话标题"},
		{name: "leading blank lines", raw: "\n\n  Add session titles  \nmore", want: "Add session titles"},
		{name: "whitespace collapsed", raw: "Add    session\ttitles", want: "Add session titles"},
		{name: "bounded", raw: long, want: clip(long, maxTitleRunes)},
		{name: "empty", raw: "  \n \n", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeTitle(tt.raw); got != tt.want {
				t.Fatalf("NormalizeTitle(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
