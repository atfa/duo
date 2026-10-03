package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/driver"
)

// waitFor polls until cond holds, so a test never depends on the watch interval.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func prepareFor(t *testing.T, p *Plugin, agent driver.AgentID, state string) *driver.LaunchPlan {
	t.Helper()
	plan, err := p.Prepare(driver.LaunchRequest{Agent: agent, CWD: t.TempDir(), State: json.RawMessage(state)})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return plan
}

// TestLearnedSessionSurvivesToDisk is the regression test for the fork: opencode
// mints its session id after prepare has run, so the only way it reaches the disk
// is State(). prepare alone can never carry it, which is why both halves are
// exercised here rather than unit-tested apart.
func TestLearnedSessionSurvivesToDisk(t *testing.T) {
	file := filepath.Join(t.TempDir(), "opencode.session")
	p := &Plugin{}

	// A first launch knows no id, so it launches without --session. The session
	// file is seeded through the state because it is stable across runs.
	seed, err := json.Marshal(state{SessionFile: file})
	if err != nil {
		t.Fatal(err)
	}
	first := prepareFor(t, p, driver.AgentID("austin"), string(seed))
	if strings.Contains(first.Command, "--session") {
		t.Fatalf("first launch must not invent a session: %q", first.Command)
	}

	// opencode reports the id it assigned by rewriting the session file.
	if err := os.WriteFile(file, []byte("ses_learned123"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the session id to be learned", func() bool {
		raw, err := p.State()
		return err == nil && strings.Contains(string(raw), "ses_learned123")
	})

	// What Core persists is State(), not the plan, so that is what the next
	// launch has to resume from.
	raw, err2 := p.State()
	if err2 != nil {
		t.Fatalf("state: %v", err2)
	}
	var saved state
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("state is not valid JSON: %v", err)
	}

	second := prepareFor(t, p, driver.AgentID("austin"), string(raw))
	if !strings.Contains(second.Command, "--session 'ses_learned123'") {
		t.Fatalf("second launch did not resume: %q", second.Command)
	}
	if second.SessionIdentity != "ses_learned123" {
		t.Fatalf("identity not reported: %q", second.SessionIdentity)
	}
	if got := second.Env["DUO_OPENCODE_SESSION_ID"]; got != "ses_learned123" {
		t.Fatalf("env not handed to the agent: %q", got)
	}
}

// TestLearnedWinsOverStaleBlob covers a restart inside one run: the stored blob
// still describes the conversation from before, and the id opencode actually used
// is the newer truth.
func TestLearnedWinsOverStaleBlob(t *testing.T) {
	p := &Plugin{}
	p.learned = "ses_current"
	plan := prepareFor(t, p, driver.AgentID("austin"), `{"sessionId":"ses_stale","sessionFile":"/tmp/x.session"}`)
	if !strings.Contains(plan.Command, "ses_current") {
		t.Fatalf("stale blob won over the learned id: %q", plan.Command)
	}
	if strings.Contains(plan.Command, "ses_stale") {
		t.Fatalf("stale id was passed to the agent: %q", plan.Command)
	}
}

// TestStateBeforePrepareIsEmptyNotABareObject keeps a pre-launch save from
// reporting a session file that no launch chose.
func TestStateBeforePrepareIsEmptyNotABareObject(t *testing.T) {
	raw, err := (&Plugin{}).State()
	if err != nil {
		t.Fatal(err)
	}
	var got state
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("state is not valid JSON: %v", err)
	}
	if got.SessionID != "" || got.SessionFile != "" {
		t.Fatalf("expected an empty blob before any launch, got %+v", got)
	}
}
