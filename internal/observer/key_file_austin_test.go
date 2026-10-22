package observer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPIKeyPrecedenceIncludesSavedFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DUO_OBSERVER_API_KEY", "")
	t.Setenv("OBSERVER_FILE_TEST_KEY", "")
	cfg := Config{APIKeyEnv: "OBSERVER_FILE_TEST_KEY"}
	if got := cfg.APIKey(); got != "" {
		t.Fatalf("empty key = %q", got)
	}
	path, err := SaveAPIKey("key-file")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.APIKey(); got != "key-file" {
		t.Fatalf("file key = %q", got)
	}
	t.Setenv("OBSERVER_FILE_TEST_KEY", "named-env")
	if got := cfg.APIKey(); got != "named-env" {
		t.Fatalf("named env key = %q", got)
	}
	t.Setenv("DUO_OBSERVER_API_KEY", "raw-env")
	if got := cfg.APIKey(); got != "raw-env" {
		t.Fatalf("raw env key = %q", got)
	}
	if path != filepath.Join(home, ".duo", keyFileName) {
		t.Fatalf("path = %q", path)
	}
}

func TestSaveAPIKeyPermissionsReplacementAndEmptyKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := SaveAPIKey("first")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := dir.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory mode %o", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode %o", got)
	}
	if _, err := SaveAPIKey("second"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != "second" {
		t.Fatalf("content = %q", got)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != keyFileName {
		t.Fatalf("unexpected temporary files: %v", entries)
	}
	if _, err := SaveAPIKey(" "); err == nil {
		t.Fatal("empty key accepted")
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != "second" {
		t.Fatalf("empty key changed saved key to %q", got)
	}
}
