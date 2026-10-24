package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentpkg "github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/remote"
	"github.com/atfa/duo/internal/session"
)

func writeRemoteConfig(t *testing.T, repo, body string) {
	t.Helper()
	dir := filepath.Join(repo, ".duo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Remote access is on by default, but it costs nothing until a pairing exists:
// the manager only connects when ~/.duo/remote.json is live.
func TestRemoteConfigDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_REMOTE", "")
	t.Setenv("DUO_REMOTE_URL", "")
	cfg, err := loadConfig([]string{t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.remoteURL != remote.DefaultServer {
		t.Errorf("remoteURL = %q, want %q", cfg.remoteURL, remote.DefaultServer)
	}
	if !cfg.remoteEnabled {
		t.Error("remote access is off by default")
	}
}

func TestRemoteConfigFromProjectFile(t *testing.T) {
	repo := t.TempDir()
	writeRemoteConfig(t, repo, `{"remote":{"url":"https://relay.example","enabled":false}}`)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_REMOTE", "")
	t.Setenv("DUO_REMOTE_URL", "")

	cfg, err := loadConfig([]string{repo})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.remoteURL != "https://relay.example" {
		t.Errorf("remoteURL = %q", cfg.remoteURL)
	}
	if cfg.remoteEnabled {
		t.Error("remote.enabled=false in the project file was ignored")
	}
}

func TestRemoteConfigEnvironmentWins(t *testing.T) {
	repo := t.TempDir()
	writeRemoteConfig(t, repo, `{"remote":{"url":"https://file.example","enabled":false}}`)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_REMOTE_URL", "https://env.example")
	t.Setenv("DUO_REMOTE", "on")

	cfg, err := loadConfig([]string{repo})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.remoteURL != "https://env.example" {
		t.Errorf("remoteURL = %q, want the environment value", cfg.remoteURL)
	}
	if !cfg.remoteEnabled {
		t.Error("DUO_REMOTE=on did not override the file")
	}
}

func TestRemoteConfigRefusesACleartextRemoteRelay(t *testing.T) {
	repo := t.TempDir()
	writeRemoteConfig(t, repo, `{"remote":{"url":"http://relay.example"}}`)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_REMOTE", "")
	t.Setenv("DUO_REMOTE_URL", "")

	if _, err := loadConfig([]string{repo}); err == nil {
		t.Fatal("a cleartext remote relay was accepted")
	}
}

func TestValidateRemoteURL(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"", true},
		{"https://remote.xduo.dev", true},
		{"wss://remote.xduo.dev", true},
		{"http://127.0.0.1:8787", true},
		{"http://localhost:8787", true},
		{"http://relay.example", false},
		{"ftp://relay.example", false},
		{"remote.xduo.dev", false},
	}
	for _, tc := range cases {
		err := validateRemoteURL(tc.url)
		if tc.want && err != nil {
			t.Errorf("validateRemoteURL(%q) = %v, want nil", tc.url, err)
		}
		if !tc.want && err == nil {
			t.Errorf("validateRemoteURL(%q) = nil, want an error", tc.url)
		}
	}
}

func TestRemoteStartRegistersBeforeReturningPairing(t *testing.T) {
	started := make(chan struct{}, 1)
	allow := make(chan struct{})
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/room" {
			select {
			case started <- struct{}{}:
			default:
			}
			<-allow
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer relay.Close()

	manager := &remoteManager{
		svc:     session.New(session.Options{}),
		path:    filepath.Join(t.TempDir(), "remote.json"),
		server:  relay.URL,
		enabled: true,
		now:     time.Now,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		pairing, err := manager.Start(ctx, false)
		if err == nil && pairing.URL == "" {
			err = errors.New("Start returned an empty pairing URL")
		}
		result <- err
	}()

	select {
	case <-started:
	case err := <-result:
		t.Fatalf("Start returned before registration began: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not register the room")
	}
	select {
	case err := <-result:
		t.Fatalf("Start returned before registration completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(allow)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after registration completed")
	}
	manager.stopClient()
}

// A mode command is validated before it reaches the session, and switching to
// the mode that is already running is not an error: the phone would otherwise
// report a failure for a tap that changed nothing.
func TestRemoteSetModeValidatesAndNoOpsTheRunningMode(t *testing.T) {
	manager := &remoteManager{svc: session.New(session.Options{}), now: time.Now}
	sink := remoteSink{m: manager}

	if err := sink.SetMode("turbo"); err == nil || !strings.Contains(err.Error(), "unknown Duo mode") {
		t.Fatalf("SetMode(turbo) = %v, want the unknown-mode error", err)
	}
	// A session that has not started yet runs Goal, which is the mode a zero
	// snapshot normalizes to.
	if err := sink.SetMode("goal"); err != nil {
		t.Errorf("SetMode(goal) on an unstarted session = %v, want nil", err)
	}
	if err := sink.SetMode("step"); err == nil {
		t.Error("SetMode(step) reported success without a running session")
	}
}

// A conversation line is cheap and frequent, so it goes out as an event; a
// workflow change must go out as a whole snapshot, which is the one thing a
// phone can always render.
func TestEventKindSplitsEventsFromSnapshots(t *testing.T) {
	cases := []struct {
		kind events.Kind
		want string
	}{
		{events.KindAssistant, remote.EventActivity},
		{events.KindPeer, remote.EventActivity},
		{events.KindUser, remote.EventActivity},
		{events.KindVerdict, remote.EventActivity},
		{events.KindError, remote.EventActivity},
		{events.KindAttention, ""},
		{events.KindHarness, ""},
		{events.KindModel, ""},
		{events.KindThinking, ""},
	}
	for _, tc := range cases {
		if got := eventKind(tc.kind); got != tc.want {
			t.Errorf("eventKind(%s) = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

// The phone addresses its agents as "austin" and "tony", which is what the
// protocol documents. The snapshot used to be keyed with the protocol's own
// spelling ("Austin"), so the page found no agent at all and drew a red dot for
// a connected one. The wire spelling is asserted here, on the marshalled
// payload, because that is what a phone actually reads.
func TestRemoteSnapshotUsesLowercaseAgentKeys(t *testing.T) {
	manager := &remoteManager{svc: session.New(session.Options{}), now: time.Now}
	state := manager.snapshot()

	if len(state.Agents) != 2 {
		t.Fatalf("snapshot carries %d agents, want 2: %v", len(state.Agents), state.Agents)
	}
	for _, key := range []string{"austin", "tony"} {
		if _, ok := state.Agents[key]; !ok {
			t.Errorf("snapshot has no %q agent: %v", key, state.Agents)
		}
	}
	for _, key := range []string{"Austin", "Tony"} {
		if _, ok := state.Agents[key]; ok {
			t.Errorf("snapshot still uses the protocol spelling %q", key)
		}
	}

	payload, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"austin":`, `"tony":`, `"model":`} {
		if !strings.Contains(string(payload), want) {
			t.Errorf("payload has no %s: %s", want, payload)
		}
	}
	if strings.Contains(string(payload), `"Austin":`) {
		t.Errorf("payload still carries a capitalised agent key: %s", payload)
	}
}

// Every agent field the phone renders comes from one projection, so a model the
// driver reports reaches the page instead of being dropped on the way.
func TestRemoteAgentCarriesTheModelAndTheWireKey(t *testing.T) {
	agent := remoteAgent(session.AgentView{
		Present:   true,
		Driver:    "pi",
		Model:     "claude-sonnet-4-6",
		Process:   agentpkg.ProcessRunning,
		Connected: true,
	})
	if agent.Model != "claude-sonnet-4-6" {
		t.Errorf("model = %q, want the driver's model", agent.Model)
	}
	if agent.Driver != "pi" {
		t.Errorf("driver = %q, want pi", agent.Driver)
	}
	if agent.State != "running" {
		t.Errorf("state = %q, want running", agent.State)
	}
	if !agent.Connected {
		t.Error("a connected agent is not connected in the projection")
	}

	// A session with no driver for this agent must not report the zero value,
	// which would tell the phone it is "starting" when it is not configured.
	absent := remoteAgent(session.AgentView{})
	if absent.State != "" {
		t.Errorf("state of an agent without a driver = %q, want empty", absent.State)
	}
	if absent.Driver != "" || absent.Model != "" {
		t.Errorf("an agent without a driver reports driver=%q model=%q", absent.Driver, absent.Model)
	}

	failed := remoteAgent(session.AgentView{Present: true, Driver: "pi", Process: agentpkg.ProcessFailed})
	if failed.State != "failed" {
		t.Errorf("state of a failed process = %q, want failed", failed.State)
	}

	for _, tc := range []struct {
		in   protocol.AgentID
		want string
	}{
		{protocol.Austin, "austin"},
		{protocol.Tony, "tony"},
		{protocol.Duo, "duo"},
		{"", ""},
	} {
		if got := agentKey(tc.in); got != tc.want {
			t.Errorf("agentKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// newTestManager builds a manager whose state directory is a temp HOME, so a
// pairing written by one call is the pairing the next call reads.
func newTestManager(t *testing.T, server string) *remoteManager {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	path, err := remote.PairingPath()
	if err != nil {
		t.Fatal(err)
	}
	return &remoteManager{
		svc:     session.New(session.Options{}),
		path:    path,
		server:  server,
		enabled: true,
		now:     time.Now,
	}
}

func newTestClient(t *testing.T, m *remoteManager, pairing remote.Pairing) *remote.Client {
	t.Helper()
	client, err := remote.New(remote.Options{
		Pairing:  pairing,
		Sink:     remoteSink{m: m},
		Snapshot: m.snapshot,
		Logf:     t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

// /qrcode on a pairing this process already holds must not re-register or
// reconnect: that costs a relay round trip and drops the socket the phone is
// using. The panel can be answered from the live pairing.
func TestQrcodeReusesAHeldPairingWithoutRegistering(t *testing.T) {
	registers := make(chan struct{}, 8)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/room" {
			registers <- struct{}{}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer relay.Close()

	manager := newTestManager(t, relay.URL)
	pairing, err := remote.NewPairing(relay.URL, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.SavePairing(manager.path, pairing); err != nil {
		t.Fatal(err)
	}
	client := newTestClient(t, manager, pairing)
	manager.mu.Lock()
	manager.pairing = pairing
	manager.client = client
	manager.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got, err := manager.Start(ctx, false)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got.Status.Room != pairing.Room {
		t.Errorf("room = %q, want the held pairing %q", got.Status.Room, pairing.Room)
	}
	select {
	case <-registers:
		t.Fatal("Start registered a room it already held")
	default:
	}
}

// A pairing whose room the relay ended cannot be handed out again: scanning it
// can only produce "Duo is offline", so /qrcode mints a new room instead.
func TestQrcodeRotatesARoomTheRelayEnded(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/room" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer relay.Close()

	manager := newTestManager(t, relay.URL)
	ended, err := remote.NewPairing(relay.URL, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.SavePairing(manager.path, ended); err != nil {
		t.Fatal(err)
	}
	// A closed client is what a terminal relay stop leaves behind: the pairing is
	// still on disk, but nothing is connected for it any more.
	client := newTestClient(t, manager, ended)
	client.Close()
	manager.mu.Lock()
	manager.pairing = ended
	manager.client = client
	manager.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got, err := manager.Start(ctx, false)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer manager.stopClient()
	if got.Status.Room == ended.Room {
		t.Fatalf("Start handed out the dead room %q again", ended.Room)
	}
	saved, ok, err := remote.LoadPairing(manager.path)
	if err != nil || !ok {
		t.Fatalf("LoadPairing = %v, %v", ok, err)
	}
	if saved.Room != got.Status.Room {
		t.Errorf("saved room = %q, want the new room %q", saved.Room, got.Status.Room)
	}
}

// Resume runs before the interface draws its first frame, so it must not wait
// for the relay: the client registers on its own before connecting.
func TestResumeDoesNotWaitForRegistration(t *testing.T) {
	blocked := make(chan struct{})
	release := make(chan struct{})
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/room" {
			close(blocked)
			<-release
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer relay.Close()

	manager := newTestManager(t, relay.URL)
	pairing, err := remote.NewPairing(relay.URL, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.SavePairing(manager.path, pairing); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		manager.Resume(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("Resume waited for the relay to answer")
	}
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("the resumed client never tried to register the room")
	}
	close(release)
	manager.stopClient()
}
