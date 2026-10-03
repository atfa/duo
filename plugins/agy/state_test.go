package agy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/protocol"
)

// These tests pin the persistence half of `resume: server`: prepare runs before
// the agent exists, so the conversation agy actually opens is discovered after
// the launch blob froze. Without State, that discovery died in the plugin, Core
// saved an empty identity, and every launch forked a new conversation — the
// exact failure class the resume capability exists to prevent.

// State offers nothing before the first prepare, and the empty object is the
// signal for Core to keep the blob it already has (a respawned plugin knows
// less than Core does, not more).
func TestStateOffersNothingBeforeAnythingIsLearned(t *testing.T) {
	raw, err := (&Plugin{}).State()
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "{}" {
		t.Errorf("state = %s, want an empty object so Core keeps its own blob", raw)
	}
}

// TestLearnedConversationReachesStateAndTheNextLaunch is the round trip the whole
// fix exists for: the id is learned from agy's log after launch, reported through
// State at save time, and replayed by the next prepare — first run included.
func TestLearnedConversationReachesStateAndTheNextLaunch(t *testing.T) {
	t.Setenv("GEMINI_APP_DATA_DIR", t.TempDir())
	const id = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	logFile := filepath.Join(t.TempDir(), "agy.log")
	if err := os.WriteFile(logFile, []byte("Created conversation "+id+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := &Plugin{}
	req := driver.LaunchRequest{
		Agent:       "Austin",
		Host:        "127.0.0.1",
		Port:        "1",
		BaseCommand: "agy",
	}
	// First launch: no conversation exists yet, only the log to learn from.
	first, err := prepareState(p, req, mustMarshalState(t, state{LogFile: logFile}))
	if err != nil {
		t.Fatal(err)
	}
	if first.Env["DUO_AGY_CONVERSATION_ID"] != "" {
		t.Fatalf("a first launch published an invented conversation id: %q", first.Env["DUO_AGY_CONVERSATION_ID"])
	}

	waitForConversation(t, p, id)
	raw, err := p.State()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"conversationId":"`+id+`"`) {
		t.Errorf("state = %s, want the learned conversation", raw)
	}
	if !strings.Contains(string(raw), `"logFile":"`+logFile+`"`) {
		t.Errorf("state = %s, want the log file the id was learned from", raw)
	}

	// The save/restart leg: Core passes the saved blob back, and the same
	// plugin (an in-run restart) replays what it learned.
	second, err := prepareState(p, req, raw)
	if err != nil {
		t.Fatal(err)
	}
	if second.Env["DUO_AGY_CONVERSATION_ID"] != id {
		t.Errorf("replayed conversation = %q, want %q", second.Env["DUO_AGY_CONVERSATION_ID"], id)
	}
	if !strings.Contains(second.Command, `--conversation "$DUO_AGY_CONVERSATION_ID"`) {
		t.Errorf("restarted agent would open a fresh conversation: %s", second.Command)
	}
	if !strings.Contains(string(second.State), `"conversationId":"`+id+`"`) {
		t.Errorf("state = %s, want the learned conversation persisted", second.State)
	}
	p.Close()
}

// TestPreparePrefersTheLearnedConversationOverTheBlob: on an in-run restart the
// blob Core sends is from the last save, while the log holds what agy did since.
// The learn must win or the agent is sent back to a conversation it has left.
func TestPreparePrefersTheLearnedConversationOverTheBlob(t *testing.T) {
	t.Setenv("GEMINI_APP_DATA_DIR", t.TempDir())
	p := &Plugin{conversation: "conv-learned", learned: "conv-learned"}
	plan, err := prepareState(p, driver.LaunchRequest{
		BaseCommand: "agy",
	}, []byte(`{"conversationId":"conv-stale","logFile":"/tmp/stale.log"}`))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Env["DUO_AGY_CONVERSATION_ID"] != "conv-learned" {
		t.Errorf("conversation = %q, want the learned one to beat the stale blob", plan.Env["DUO_AGY_CONVERSATION_ID"])
	}
	if plan.SessionIdentity != "conv-learned" {
		t.Errorf("session identity = %q, want the learned one", plan.SessionIdentity)
	}
	if !strings.Contains(string(plan.State), `"conversationId":"conv-learned"`) {
		t.Errorf("state = %s, want the learned conversation persisted", plan.State)
	}
	if strings.Contains(string(plan.State), "conv-stale") {
		t.Errorf("state = %s, must not keep the stale conversation", plan.State)
	}
}

// TestLearnStoresTheConversationEvenWithAWatcherRunning pins the ordering the
// watcher guard used to break: when agy opens a conversation other than the one
// being tailed, the id must still be recorded and the observer must follow the
// conversation that is actually running.
func TestLearnStoresTheConversationEvenWithAWatcherRunning(t *testing.T) {
	t.Setenv("GEMINI_APP_DATA_DIR", t.TempDir())
	const newID = "11111111-2222-3333-4444-555555555555"
	logFile := filepath.Join(t.TempDir(), "agy.log")
	if err := os.WriteFile(logFile, []byte("conversation="+newID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldPath := TranscriptPath("conv-old")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := NewAgyWatcher(protocol.Austin, oldPath, FuncActivitySink(func(protocol.AgentID, protocol.Message) {}))
	old.Start(context.Background())
	p := &Plugin{watcher: old, conversation: "conv-old"}

	p.startObserver(driver.LaunchRequest{
		Agent: "Austin",
		Host:  "127.0.0.1",
		Port:  "1",
	}, "", state{LogFile: logFile})

	waitForConversation(t, p, newID)
	if p.watcher == old {
		t.Fatal("the observer still tails the previous conversation")
	}
	if want := TranscriptPath(newID); p.watcher.transcriptPath != want {
		t.Errorf("observer tails %q, want %q", p.watcher.transcriptPath, want)
	}
	old.mu.Lock()
	stillRunning := old.running
	old.mu.Unlock()
	if stillRunning {
		t.Error("the previous observer was left running")
	}
	p.Close()
}

// TestABlobNeverMasqueradesAsALearn: a conversation Core passed in through state
// is replay material, not a discovery. If it set the preference, every later
// stateless prepare on the same process would inherit it — a fresh run would be
// sent a conversation agy never reported to this plugin.
func TestABlobNeverMasqueradesAsALearn(t *testing.T) {
	t.Setenv("GEMINI_APP_DATA_DIR", t.TempDir())
	p := &Plugin{}
	resumed, err := prepareState(p, driver.LaunchRequest{
		BaseCommand: "agy",
	}, []byte(`{"sessionId":"abc"}`))
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Env["DUO_AGY_CONVERSATION_ID"] != "abc" {
		t.Fatalf("a passed identity was not replayed: %v", resumed.Env)
	}
	bare, err := prepareState(p, driver.LaunchRequest{BaseCommand: "agy"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bare.Env["DUO_AGY_CONVERSATION_ID"] != "" {
		t.Errorf("a later stateless prepare inherited %q from a blob", bare.Env["DUO_AGY_CONVERSATION_ID"])
	}
	if strings.Contains(bare.Command, "--conversation") {
		t.Errorf("a fresh run was sent a conversation from an earlier blob: %s", bare.Command)
	}
}

// prepareState runs prepare on a specific plugin instance with the given blob,
// so a test can carry one instance across the prepare/learn/re-prepare cycle the
// way a real session does.
func prepareState(p *Plugin, req driver.LaunchRequest, stateRaw []byte) (*driver.LaunchPlan, error) {
	req.State = stateRaw
	return p.Prepare(req)
}

// waitForConversation waits for the learn goroutine to record an id. The learn
// polls agy's log on its own schedule, so a test that asserted immediately would
// race the very behavior it is checking.
func waitForConversation(t *testing.T, p *Plugin, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		p.mu.Lock()
		got = p.conversation
		p.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("conversation = %q, want %q; the learn never recorded it", got, want)
}

func mustMarshalState(t *testing.T, s state) []byte {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
