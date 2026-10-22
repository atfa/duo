package observer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The saved key is a convenience, never the authority: the environment must be
// able to override it for one run without editing files.
func TestAPIKeyFileIsLastResort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DUO_OBSERVER_API_KEY", "")
	// Clear the variable this test names too: an ambient value of the same name
	// must not make the "no key yet" assertion fail on the operator's machine.
	t.Setenv("OBSERVER_FILE_TEST_KEY", "")

	cfg := Config{APIKeyEnv: "OBSERVER_FILE_TEST_KEY"}
	if got := cfg.APIKey(); got != "" {
		t.Fatalf("APIKey without a file or environment = %q, want empty", got)
	}

	path, err := SaveAPIKey("key-from-file")
	if err != nil {
		t.Fatalf("SaveAPIKey: %v", err)
	}
	if want := filepath.Join(home, ".duo", keyFileName); path != want {
		t.Fatalf("saved key path = %q, want %q", path, want)
	}
	if got := cfg.APIKey(); got != "key-from-file" {
		t.Fatalf("APIKey from file = %q", got)
	}

	t.Setenv("OBSERVER_FILE_TEST_KEY", "key-from-named-variable")
	if got := cfg.APIKey(); got != "key-from-named-variable" {
		t.Fatalf("apiKeyEnv variable must beat the file, got %q", got)
	}

	t.Setenv("DUO_OBSERVER_API_KEY", "key-from-raw-environment")
	if got := cfg.APIKey(); got != "key-from-raw-environment" {
		t.Fatalf("DUO_OBSERVER_API_KEY must win, got %q", got)
	}
}

func TestSaveAPIKeyIsOwnerOnlyAndAtomic(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := SaveAPIKey("first-key")
	if err != nil {
		t.Fatalf("SaveAPIKey: %v", err)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat key directory: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != keyDirMode {
		t.Fatalf("key directory mode = %o, want %o", got, keyDirMode)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat key file: %v", err)
	}
	if got := info.Mode().Perm(); got != keyFileMode {
		t.Fatalf("key file mode = %o, want %o", got, keyFileMode)
	}

	if _, err := SaveAPIKey("second-key"); err != nil {
		t.Fatalf("replace key: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "second-key" {
		t.Fatalf("key after replacement = %q", got)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read key directory: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("temporary key file %q was left behind", entry.Name())
		}
	}
}

func TestSaveAPIKeyRejectsEmptyKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := SaveAPIKey("   "); err == nil {
		t.Fatal("expected an error for an empty key")
	}
	if KeyFileExists() {
		t.Fatal("rejecting an empty key must not create a key file")
	}
}

func TestKeyFileExistsWithoutReading(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if KeyFileExists() {
		t.Fatal("KeyFileExists reported a file before one was saved")
	}
	if _, err := SaveAPIKey("saved"); err != nil {
		t.Fatalf("SaveAPIKey: %v", err)
	}
	if !KeyFileExists() {
		t.Fatal("KeyFileExists did not report the saved key")
	}
}

// The environment overlay is the single rule both the CLI and a live save use,
// so it is pinned here: unset or unparsable values never reset a configuration.
func TestWithEnvAppliesOverridesOnlyWhenSet(t *testing.T) {
	base := Config{Enabled: false, Endpoint: "http://file/v1", Model: "file-model"}
	t.Setenv("DUO_OBSERVER", "")
	t.Setenv("DUO_OBSERVER_ENDPOINT", "")
	t.Setenv("DUO_OBSERVER_MODEL", "")
	if got := base.WithEnv(); got != base {
		t.Fatalf("unset environment changed the config: %+v", got)
	}

	t.Setenv("DUO_OBSERVER", "not-a-bool")
	if got := base.WithEnv(); got.Enabled {
		t.Fatalf("an unparsable DUO_OBSERVER enabled the observer: %+v", got)
	}
	if enabled, _, _ := EnvOverrides(); enabled {
		t.Fatal("an unparsable DUO_OBSERVER was reported as an override")
	}

	t.Setenv("DUO_OBSERVER", "true")
	t.Setenv("DUO_OBSERVER_ENDPOINT", "http://env/v1")
	t.Setenv("DUO_OBSERVER_MODEL", "env-model")
	got := base.WithEnv()
	if !got.Enabled || got.Endpoint != "http://env/v1" || got.Model != "env-model" {
		t.Fatalf("environment did not win: %+v", got)
	}
	enabled, endpoint, model := EnvOverrides()
	if !enabled || !endpoint || !model {
		t.Fatalf("EnvOverrides = %v/%v/%v, want all true", enabled, endpoint, model)
	}
}
