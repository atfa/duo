package gotui

import (
	"context"
	"encoding/json"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/observer"
	"github.com/atfa/duo/internal/session"
	"github.com/gdamore/tcell/v3"
	ui "github.com/metaspartan/gotui/v5"
)

func runObserverSimulation(t *testing.T, fake *fakeSession, onboarding ...bool) (tcell.SimulationScreen, context.CancelFunc) {
	t.Helper()
	app := New(fake)
	if len(onboarding) > 0 {
		app.onboarding = onboarding[0]
	}
	backend, err := ui.NewBackend(&ui.InitConfig{SimulationMode: true, SimulationSize: image.Pt(120, 42)})
	if err != nil {
		t.Fatal(err)
	}
	sim, ok := backend.Screen.(tcell.SimulationScreen)
	if !ok {
		t.Fatalf("screen %T is not a simulation screen", backend.Screen)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.runWithBackend(ctx, backend) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("runWithBackend did not stop")
		}
	})
	waitForText(t, sim, "DUO")
	return sim, cancel
}

func openObserverViaSlash(t *testing.T, sim tcell.SimulationScreen) {
	t.Helper()
	typeText(sim, "/observer")
	waitForText(t, sim, "Configure the advisory observer")
	injectKey(sim, tcell.KeyEnter, 0, tcell.ModNone)
	waitForText(t, sim, "OBSERVER SETTINGS")
}

func openObserverViaShortcut(t *testing.T, sim tcell.SimulationScreen) {
	t.Helper()
	injectKey(sim, tcell.KeyCtrlE, 0, tcell.ModNone)
	waitForText(t, sim, "OBSERVER SETTINGS")
}

func TestObserverConfigOpensFromSlashAndCtrlE(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER", "")
	t.Setenv("DUO_OBSERVER_ENDPOINT", "")
	t.Setenv("DUO_OBSERVER_MODEL", "")
	t.Setenv("DUO_OBSERVER_API_KEY", "")

	for _, open := range []struct {
		name string
		fn   func(*testing.T, tcell.SimulationScreen)
	}{
		{"slash", openObserverViaSlash},
		{"shortcut", openObserverViaShortcut},
	} {
		t.Run(open.name, func(t *testing.T) {
			fake := newFakeSession()
			fake.view.Observer = session.ObserverView{Endpoint: "http://127.0.0.1:4000/v1", Model: "exec"}
			sim, _ := runObserverSimulation(t, fake)
			open.fn(t, sim)
			if len(fake.snapshotCommands()) != 0 {
				t.Fatalf("opening the settings panel issued commands: %#v", fake.snapshotCommands())
			}
		})
	}
}

func TestObserverConfigArrowKeysDoNotCorruptValues(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER", "")
	t.Setenv("DUO_OBSERVER_ENDPOINT", "")
	t.Setenv("DUO_OBSERVER_MODEL", "")
	t.Setenv("DUO_OBSERVER_API_KEY", "")

	const endpoint = "http://127.0.0.1:4000/v1"
	fake := newFakeSession()
	fake.view.Observer = session.ObserverView{Endpoint: endpoint, Model: "exec"}
	sim, _ := runObserverSimulation(t, fake)
	openObserverViaShortcut(t, sim)

	// Move around the existing text; redraws during cursor movement must not
	// write the visible cursor marker back into the configured value.
	injectKey(sim, tcell.KeyLeft, 0, tcell.ModNone)
	injectKey(sim, tcell.KeyLeft, 0, tcell.ModNone)
	injectKey(sim, tcell.KeyRight, 0, tcell.ModNone)
	injectKey(sim, tcell.KeyEnter, 0, tcell.ModNone)
	waitGone(t, sim, "OBSERVER SETTINGS")

	commands := fake.snapshotCommands()
	if len(commands) != 1 {
		t.Fatalf("commands = %#v, want one SetObserver", commands)
	}
	cmd, ok := commands[0].(session.SetObserver)
	if !ok || cmd.Endpoint != endpoint {
		t.Fatalf("saved command = %#v, want endpoint %q", commands[0], endpoint)
	}
}

func TestObserverConfigSaveMasksKeyAndAppliesSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DUO_OBSERVER", "")
	t.Setenv("DUO_OBSERVER_ENDPOINT", "")
	t.Setenv("DUO_OBSERVER_MODEL", "")
	t.Setenv("DUO_OBSERVER_API_KEY", "")

	fake := newFakeSession()
	fake.view.Observer = session.ObserverView{Endpoint: "http://old/v1", Model: "old-model"}
	sim, _ := runObserverSimulation(t, fake)
	openObserverViaShortcut(t, sim)

	// Replace the endpoint, model and key using the actual overlay controls.
	injectKey(sim, tcell.KeyCtrlU, 0, tcell.ModNone)
	typeText(sim, "http://new.test/v1")
	injectKey(sim, tcell.KeyTab, 0, tcell.ModNone)
	injectKey(sim, tcell.KeyCtrlU, 0, tcell.ModNone)
	typeText(sim, "new-model")
	injectKey(sim, tcell.KeyTab, 0, tcell.ModNone)
	typeText(sim, "sk-ui-secret")
	injectKey(sim, tcell.KeyTab, 0, tcell.ModNone)
	injectKey(sim, tcell.KeyRune, ' ', tcell.ModNone)
	if text := screenText(sim); strings.Contains(text, "sk-ui-secret") {
		t.Fatalf("API key was rendered in the screen:\n%s", text)
	}
	injectKey(sim, tcell.KeyEnter, 0, tcell.ModNone)
	waitGone(t, sim, "OBSERVER SETTINGS")

	commands := fake.snapshotCommands()
	if len(commands) != 1 {
		t.Fatalf("commands = %#v, want one SetObserver", commands)
	}
	cmd, ok := commands[0].(session.SetObserver)
	if !ok {
		t.Fatalf("command = %#v, want SetObserver", commands[0])
	}
	if !cmd.Enabled || cmd.Endpoint != "http://new.test/v1" || cmd.Model != "new-model" {
		t.Fatalf("SetObserver = %+v", cmd)
	}
	encoded, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "sk-ui-secret") {
		t.Fatalf("secret crossed the session command seam: %s", encoded)
	}

	keyPath := filepath.Join(home, ".duo", "observer.key")
	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read saved key: %v", err)
	}
	if strings.TrimSpace(string(key)) != "sk-ui-secret" {
		t.Fatalf("key file content = %q", key)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("key file permissions = %o, want 600", got)
	}
	view := fake.View().Observer
	if !view.Enabled || view.Endpoint != cmd.Endpoint || view.Model != cmd.Model || !view.Configured {
		t.Fatalf("observer view not updated after save: %+v", view)
	}
	if strings.Contains(strings.ToLower(screenText(sim)), "sk-ui-secret") {
		t.Fatal("API key appeared in the transcript after saving")
	}
	if _, err := observer.KeyFilePath(); err != nil {
		t.Fatal(err)
	}
}

func TestObserverConfigEscapeDoesNotSave(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DUO_OBSERVER", "")
	t.Setenv("DUO_OBSERVER_ENDPOINT", "")
	t.Setenv("DUO_OBSERVER_MODEL", "")
	t.Setenv("DUO_OBSERVER_API_KEY", "")

	fake := newFakeSession()
	fake.view.Observer = session.ObserverView{Endpoint: "http://original/v1", Model: "original"}
	sim, _ := runObserverSimulation(t, fake)
	openObserverViaSlash(t, sim)
	injectKey(sim, tcell.KeyCtrlU, 0, tcell.ModNone)
	typeText(sim, "http://changed/v1")
	injectKey(sim, tcell.KeyTab, 0, tcell.ModNone)
	typeText(sim, "sk-cancelled-secret")
	injectKey(sim, tcell.KeyEsc, 0, tcell.ModNone)
	waitGone(t, sim, "OBSERVER SETTINGS")
	if got := fake.snapshotCommands(); len(got) != 0 {
		t.Fatalf("Esc changed the session: %#v", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".duo", "observer.key")); !os.IsNotExist(err) {
		t.Fatalf("Esc wrote the API key, stat error = %v", err)
	}
	if fake.View().Observer.Endpoint != "http://original/v1" {
		t.Fatalf("Esc changed observer config: %+v", fake.View().Observer)
	}
}

func TestObserverConfigBlankKeyKeepsExistingKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DUO_OBSERVER", "")
	t.Setenv("DUO_OBSERVER_ENDPOINT", "")
	t.Setenv("DUO_OBSERVER_MODEL", "")
	t.Setenv("DUO_OBSERVER_API_KEY", "")
	keyPath, err := observer.SaveAPIKey("existing-key")
	if err != nil {
		t.Fatal(err)
	}

	fake := newFakeSession()
	fake.view.Observer = session.ObserverView{
		Enabled: true, Endpoint: "http://existing/v1", Model: "existing-model", HasKey: true, KeyFile: true, Configured: true,
	}
	sim, _ := runObserverSimulation(t, fake)
	openObserverViaSlash(t, sim)
	injectKey(sim, tcell.KeyEnter, 0, tcell.ModNone)
	waitGone(t, sim, "OBSERVER SETTINGS")

	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(key)) != "existing-key" {
		t.Fatalf("blank API key replaced the existing key with %q", key)
	}
	commands := fake.snapshotCommands()
	if len(commands) != 1 {
		t.Fatalf("commands = %#v, want one SetObserver", commands)
	}
	if _, ok := commands[0].(session.SetObserver); !ok {
		t.Fatalf("command = %#v, want SetObserver", commands[0])
	}
}

func TestObserverConfigShowsEnvironmentOverridesAndKeyState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := newFakeSession()
	fake.view.Observer = session.ObserverView{
		Enabled: true, Endpoint: "http://env/v1", Model: "env-model", HasKey: true,
		KeyFile: true, Configured: true, EnvOverride: true, EnvOverrideEndpoint: true, EnvOverrideModel: true,
	}
	sim, _ := runObserverSimulation(t, fake)
	openObserverViaShortcut(t, sim)
	text := screenText(sim)
	for _, want := range []string{"controlled by environment", "key available", "key file present", "configured", "DUO_OBSERVER*"} {
		if !strings.Contains(text, want) {
			t.Errorf("observer environment/key state missing %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "http://env/v1") {
		t.Fatalf("effective environment endpoint not shown:\n%s", text)
	}
}

func TestObserverOnboardingSavesAndDoesNotRepeat(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER", "")
	t.Setenv("DUO_OBSERVER_ENDPOINT", "")
	t.Setenv("DUO_OBSERVER_MODEL", "")
	t.Setenv("DUO_OBSERVER_API_KEY", "")
	fake := newFakeSession()
	sim, _ := runObserverSimulation(t, fake, true)
	waitForText(t, sim, "SET UP OBSERVER")
	text := screenText(sim)
	for _, want := range []string{"http://127.0.0.1:4000/v1", "exec", "Esc Later"} {
		if !strings.Contains(text, want) {
			t.Errorf("onboarding missing %q:\n%s", want, text)
		}
	}

	// Enable the prefilled settings and save them through the same settings path.
	injectKey(sim, tcell.KeyTab, 0, tcell.ModNone)
	injectKey(sim, tcell.KeyTab, 0, tcell.ModNone)
	injectKey(sim, tcell.KeyTab, 0, tcell.ModNone)
	injectKey(sim, tcell.KeyRune, ' ', tcell.ModNone)
	injectKey(sim, tcell.KeyEnter, 0, tcell.ModNone)
	waitGone(t, sim, "SET UP OBSERVER")
	commands := fake.snapshotCommands()
	if len(commands) != 1 {
		t.Fatalf("commands after onboarding save = %#v", commands)
	}
	saved, ok := commands[0].(session.SetObserver)
	if !ok || !saved.Enabled || saved.Endpoint != "http://127.0.0.1:4000/v1" || saved.Model != "exec" {
		t.Fatalf("onboarding save = %#v", commands[0])
	}
	if strings.Contains(screenText(sim), "SET UP OBSERVER") {
		t.Fatal("startup onboarding repeated after save")
	}
}

func TestObserverOnboardingLaterPersistsDismissalAndManualOpenStillWorks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DUO_OBSERVER", "")
	t.Setenv("DUO_OBSERVER_ENDPOINT", "")
	t.Setenv("DUO_OBSERVER_MODEL", "")
	t.Setenv("DUO_OBSERVER_API_KEY", "")
	fake := newFakeSession()
	sim, _ := runObserverSimulation(t, fake, true)
	waitForText(t, sim, "SET UP OBSERVER")
	injectKey(sim, tcell.KeyEsc, 0, tcell.ModNone)
	waitGone(t, sim, "SET UP OBSERVER")
	commands := fake.snapshotCommands()
	if len(commands) != 1 {
		t.Fatalf("commands after Later = %#v", commands)
	}
	later, ok := commands[0].(session.SetObserver)
	if !ok || later.Enabled || later.Endpoint != "" || later.Model != "" {
		t.Fatalf("Later command = %#v, want only enabled:false", commands[0])
	}
	if fake.View().Observer.Configured != true {
		t.Fatal("Later did not mark observer configured")
	}
	if strings.Contains(screenText(sim), "SET UP OBSERVER") {
		t.Fatal("onboarding appeared again after Later")
	}

	typeText(sim, "/observer")
	waitForText(t, sim, "Configure the advisory observer")
	injectKey(sim, tcell.KeyEnter, 0, tcell.ModNone)
	waitForText(t, sim, "OBSERVER SETTINGS")
}

func TestObserverOnboardingSkipsConfiguredEnvironmentSetup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := newFakeSession()
	fake.view.Observer = session.ObserverView{
		Endpoint: "http://env/v1", Model: "exec", Configured: true, EnvOverride: true,
	}
	sim, _ := runObserverSimulation(t, fake, true)
	if strings.Contains(screenText(sim), "SET UP OBSERVER") {
		t.Fatal("configured environment observer triggered onboarding")
	}
	injectKey(sim, tcell.KeyCtrlE, 0, tcell.ModNone)
	waitForText(t, sim, "OBSERVER SETTINGS")
}

func TestObserverOnboardingUsesChineseStrings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := newFakeSession()
	fake.view.Language = "zh"
	sim, _ := runObserverSimulation(t, fake, true)
	for _, want := range []string{"设置观察器", "现在配置可选的建议型观察器", "稍后"} {
		waitForText(t, sim, want)
	}
}
