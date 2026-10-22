package gotui

import (
	"errors"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/session"
)

func TestSlashResumeSendsResumeWorkAndEchoesThePhase(t *testing.T) {
	fake := newFakeSession()
	fake.view.ResumePending = true
	app := New(fake)
	if !app.runSlash("/resume") {
		t.Fatal("/resume was not recognized")
	}
	commands := fake.snapshotCommands()
	if len(commands) != 1 {
		t.Fatalf("commands = %#v, want one ResumeWork", commands)
	}
	if _, ok := commands[0].(session.ResumeWork); !ok {
		t.Fatalf("command = %#v, want session.ResumeWork", commands[0])
	}
	if app.live.ResumePending {
		t.Fatal("/resume did not refresh the pending state")
	}
	var timeline strings.Builder
	for _, e := range app.entries {
		timeline.WriteString(e.text)
		timeline.WriteString("\n")
	}
	if !strings.Contains(timeline.String(), "asked to continue") {
		t.Fatalf("timeline does not echo the resume: %q", timeline.String())
	}
}

func TestSlashResumeReportsWhyItCannotResume(t *testing.T) {
	fake := newFakeSession()
	fake.doErr = errors.New("session is already resumed")
	app := New(fake)
	if !app.runSlash("/resume") {
		t.Fatal("/resume was not recognized")
	}
	if !app.statusErr || !strings.Contains(app.status, "already resumed") {
		t.Fatalf("status = %q (err=%t), want the session's reason", app.status, app.statusErr)
	}
}

func TestRestoredSessionExplainsSilenceOnce(t *testing.T) {
	fake := newFakeSession()
	fake.view.ResumePending = true
	app := New(fake)
	app.announceResumePending()
	app.announceResumePending()
	count := 0
	for _, e := range app.entries {
		if strings.Contains(e.text, "Nothing was sent to the agents") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("resume notice appeared %d times, want 1", count)
	}

	fresh := New(newFakeSession())
	fresh.announceResumePending()
	for _, e := range fresh.entries {
		if strings.Contains(e.text, "Nothing was sent to the agents") {
			t.Fatalf("fresh session explained a resume: %q", e.text)
		}
	}
}
