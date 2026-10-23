package remote

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveLoadAndDeletePairing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", PairingFileName)
	p, err := NewPairing(DefaultServer, time.Now(), time.Hour)
	if err != nil {
		t.Fatalf("NewPairing: %v", err)
	}
	if err := SavePairing(path, p); err != nil {
		t.Fatalf("SavePairing: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("pairing file mode = %o, want 600", perm)
	}
	got, ok, err := LoadPairing(path)
	if err != nil || !ok {
		t.Fatalf("LoadPairing: ok=%v err=%v", ok, err)
	}
	if got.Room != p.Room || got.Key != p.Key || got.Server != p.Server {
		t.Errorf("loaded %+v, want %+v", got, p)
	}
	if err := DeletePairing(path); err != nil {
		t.Fatalf("DeletePairing: %v", err)
	}
	if _, ok, err := LoadPairing(path); err != nil || ok {
		t.Errorf("after delete: ok=%v err=%v", ok, err)
	}
	// Deleting a pairing that is already gone is the state the caller wanted.
	if err := DeletePairing(path); err != nil {
		t.Errorf("second DeletePairing: %v", err)
	}
}

func TestLoadPairingRejectsAMalformedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, PairingFileName)
	if err := os.WriteFile(path, []byte(`{"server":"https://x","room":"r","key":"not-a-key"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadPairing(path); err == nil {
		t.Fatal("a malformed pairing loaded without an error")
	}
}

func TestLivePairingIgnoresExpiredAndRevoked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, PairingFileName)
	now := time.Now()
	p, err := NewPairing(DefaultServer, now, time.Hour)
	if err != nil {
		t.Fatalf("NewPairing: %v", err)
	}
	if err := SavePairing(path, p); err != nil {
		t.Fatalf("SavePairing: %v", err)
	}
	if _, ok := LivePairing(path, now); !ok {
		t.Error("a live pairing was not resumed")
	}
	if _, ok := LivePairing(path, now.Add(2*time.Hour)); ok {
		t.Error("an expired pairing was resumed")
	}
	p.Revoked = true
	if err := SavePairing(path, p); err != nil {
		t.Fatalf("SavePairing: %v", err)
	}
	if _, ok := LivePairing(path, now); ok {
		t.Error("a revoked pairing was resumed")
	}
}

func TestMissingPairingIsNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), PairingFileName)
	p, ok, err := LoadPairing(path)
	if err != nil || ok || p.Room != "" {
		t.Errorf("LoadPairing(missing) = %+v, %v, %v; want zero, false, nil", p, ok, err)
	}
}
