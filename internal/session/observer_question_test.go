package session

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atfa/duo/internal/observer"
)

// answeringServer answers report requests with one canned reply and records the
// user prompts it received. blocking decides whether a request waits for the
// test to release it, which is how an in-flight call is observed.
type answeringServer struct {
	mu       sync.Mutex
	prompts  []string
	release  chan struct{}
	blockOne bool
}

func newAnsweringServer(t *testing.T, reply string, blockFirst bool) (*answeringServer, *httptest.Server) {
	t.Helper()
	rec := &answeringServer{release: make(chan struct{}), blockOne: blockFirst}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		rec.mu.Lock()
		rec.prompts = append(rec.prompts, payload.Messages[1].Content)
		first := len(rec.prompts) == 1
		rec.mu.Unlock()
		if rec.blockOne && first {
			select {
			case <-rec.release:
			case <-r.Context().Done():
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"`+reply+`"}}]}`)
	}))
	t.Cleanup(server.Close)
	return rec, server
}

func (s *answeringServer) prompt(t *testing.T, index int) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if index >= len(s.prompts) {
		t.Fatalf("only %d request(s) were made, wanted #%d", len(s.prompts), index+1)
	}
	return s.prompts[index]
}

func (s *answeringServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.prompts)
}

// questionSession starts a session whose observer talks to server.
func questionSession(t *testing.T, server *httptest.Server) *Service {
	t.Helper()
	repo := gitRepo(t)
	opts := headlessOptions(t, repo, t.TempDir(), "127.0.0.1:0")
	opts.Observer = observer.Config{
		Enabled: true, Endpoint: server.URL + "/v1", Model: "exec",
		MaxEvents: 4, Timeout: 5 * time.Second,
	}
	svc := New(opts)
	if _, err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
	return svc
}

// waitObserverView waits until cond holds for the observer's view.
func waitObserverView(t *testing.T, svc *Service, what string, cond func(ObserverView) bool) ObserverView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		view := svc.View().Observer
		if cond(view) {
			return view
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; notice=%q", what, svc.View().Observer.LastNotice)
	return ObserverView{}
}

// A typed question reaches the model as its own prompt block and comes back into
// the panel with the question attached, so the panel can show what was asked.
func TestObserverAnswerCarriesTheQuestionIntoTheView(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	rec, server := newAnsweringServer(t, "Tony is stuck on the same failing test.", false)
	svc := questionSession(t, server)

	svc.RequestObserverAnswer("  why is Tony\n stuck? ")

	view := waitObserverView(t, svc, "the answer", func(v ObserverView) bool {
		return len(v.Reports) > 0 && !v.Analyzing
	})
	report := view.Reports[0]
	if report.Question != "why is Tony stuck?" {
		t.Fatalf("report question = %q, want the normalized question", report.Question)
	}
	if report.Text != "Tony is stuck on the same failing test." {
		t.Fatalf("report text = %q", report.Text)
	}
	prompt := rec.prompt(t, 0)
	if want := "Question from the human (answer this instead of writing the report):\nwhy is Tony stuck?"; !strings.Contains(prompt, want) {
		t.Fatalf("prompt is missing %q:\n%s", want, prompt)
	}
}

// A question already asked stays visible while the model works: the panel says
// what it is waiting for instead of looking like a frozen status report.
func TestObserverPendingQuestionIsVisibleWhileAnswering(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	rec, server := newAnsweringServer(t, "answer", true)
	svc := questionSession(t, server)

	svc.RequestObserverAnswer("what changed since the last report?")
	inFlight := waitObserverView(t, svc, "the request to be in flight", func(v ObserverView) bool {
		return v.Analyzing
	})
	if inFlight.PendingQuestion != "what changed since the last report?" {
		t.Fatalf("pending question = %q while answering", inFlight.PendingQuestion)
	}

	close(rec.release)
	idle := waitObserverView(t, svc, "the answer", func(v ObserverView) bool {
		return len(v.Reports) > 0 && !v.Analyzing
	})
	if idle.PendingQuestion != "" {
		t.Fatalf("pending question = %q after the answer landed", idle.PendingQuestion)
	}
}

// A blank question is not a request, and it must not look like one: no model call
// and no notice for the human to dismiss.
func TestObserverBlankQuestionIsNotARequest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	rec, server := newAnsweringServer(t, "answer", false)
	svc := questionSession(t, server)

	svc.RequestObserverAnswer("   \n\t ")
	time.Sleep(100 * time.Millisecond)

	if got := rec.count(); got != 0 {
		t.Fatalf("a blank question made %d model request(s)", got)
	}
	view := svc.View().Observer
	if len(view.Reports) != 0 || view.Analyzing || view.PendingQuestion != "" || view.LastNotice != "" {
		t.Fatalf("a blank question changed the panel: %+v", view)
	}
}

// Pressing the key twice while one answer is in flight must not fan out calls,
// and the newest question is the one that runs next: the human typed it last.
func TestObserverNewestQueuedQuestionRunsNext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER_API_KEY", "sk-test")
	rec, server := newAnsweringServer(t, "answer", true)
	svc := questionSession(t, server)

	svc.RequestObserverAnswer("first question")
	waitObserverView(t, svc, "the first request to be in flight", func(v ObserverView) bool {
		return v.Analyzing && v.PendingQuestion == "first question"
	})
	svc.RequestObserverAnswer("second question")
	svc.RequestObserverAnswer("third question")

	close(rec.release)
	waitObserverView(t, svc, "both answers", func(v ObserverView) bool {
		return len(v.Reports) >= 2 && !v.Analyzing
	})

	if got := rec.count(); got != 2 {
		t.Fatalf("%d model request(s) were made, want 2: one in flight plus the newest queued", got)
	}
	if prompt := rec.prompt(t, 1); !strings.Contains(prompt, "third question") || strings.Contains(prompt, "second question") {
		t.Fatalf("the replaced question still ran:\n%s", prompt)
	}
	view := svc.View().Observer
	if view.Reports[0].Question != "third question" || view.Reports[1].Question != "first question" {
		t.Fatalf("panel history = %+v, want the third answer newest", view.Reports)
	}
}
