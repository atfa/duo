package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/atfa/duo/internal/events"
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
