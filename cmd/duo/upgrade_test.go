package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/atfa/duo/internal/sessionstore"
)

// fakeRelease is a release server that speaks the two endpoints `duo upgrade`
// reads: the latest-release API and the assets it names. Everything the command
// checks — the checksum file, the archive, the schema source — is served from here,
// so each refusal path can be exercised without a network or an installation.
type fakeRelease struct {
	tag      string
	archive  []byte
	sums     string
	hasSums  bool
	snapshot string
	migrate  string

	mu          sync.Mutex
	archiveGets int
}

func (f *fakeRelease) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	base := "http://" + r.Host
	switch {
	case r.URL.Path == "/api/repos/atfa/duo/releases/latest":
		assets := []releaseAsset{{
			Name: fmt.Sprintf("duo_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH),
			URL:  base + "/dl/archive",
		}}
		if f.hasSums {
			assets = append(assets, releaseAsset{Name: upgradeChecksumAsset, URL: base + "/dl/sums"})
		}
		if err := json.NewEncoder(w).Encode(releaseInfo{Tag: f.tag, Assets: assets}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	case r.URL.Path == "/dl/archive":
		f.mu.Lock()
		f.archiveGets++
		f.mu.Unlock()
		_, _ = w.Write(f.archive)
	case r.URL.Path == "/dl/sums":
		_, _ = io.WriteString(w, f.sums)
	case strings.HasSuffix(r.URL.Path, "/internal/sessionstore/snapshot.go"):
		if f.snapshot == "" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, f.snapshot)
	case strings.HasSuffix(r.URL.Path, "/internal/sessionstore/migrate.go"):
		if f.migrate == "" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, f.migrate)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeRelease) archiveDownloads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.archiveGets
}

func releaseArchive(t *testing.T, binary string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	// The real workflow packs with `tar -C dist/package -czf … .`, so every entry
	// carries a "./" prefix. The extractor must not depend on the bare name.
	body := []byte(binary)
	if err := tw.WriteHeader(&tar.Header{Name: "./duo", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func archiveHash(archive []byte) string {
	sum := sha256.Sum256(archive)
	return hex.EncodeToString(sum[:])
}

func checksumFile(hash string) string {
	return fmt.Sprintf("%s  ./duo_%s_%s.tar.gz\n", hash, runtime.GOOS, runtime.GOARCH)
}

// schemaSource is the shape the release's own snapshot.go has: the constant
// checkSchemaCompatible reads out of the source the new binary is built from.
func schemaSource(version int) string {
	return fmt.Sprintf("package sessionstore\n\n// SchemaVersion is the persisted snapshot schema.\nconst SchemaVersion = %d\n", version)
}

func targetBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "duo")
	if err := os.WriteFile(path, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func upgradeServer(t *testing.T, release *fakeRelease) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(release)
	t.Cleanup(server.Close)
	return server
}

func upgradeOptionsFor(server *httptest.Server, target string, out io.Writer) upgradeOptions {
	return upgradeOptions{
		Client:  server.Client(),
		APIBase: server.URL + "/api",
		RawBase: server.URL + "/raw",
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
		Target:  target,
		Out:     out,
	}
}

// A download that does not hash to the published checksum must leave the installed
// binary exactly as it was: this command's whole value is that a bad archive cannot
// reach the path the next start executes.
func TestUpgradeChecksumMismatchKeepsBinary(t *testing.T) {
	release := &fakeRelease{
		tag:      "v9.9.9",
		archive:  releaseArchive(t, "new binary"),
		sums:     checksumFile(strings.Repeat("0", 64)),
		hasSums:  true,
		snapshot: schemaSource(sessionstore.SchemaVersion),
	}
	server := upgradeServer(t, release)
	target := targetBinary(t)

	err := upgradeFromRelease(context.Background(), upgradeOptionsFor(server, target, io.Discard))
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("upgradeFromRelease = %v, want a checksum mismatch", err)
	}
	if got := readFile(t, target); got != "old binary" {
		t.Errorf("the target binary is %q, want it untouched", got)
	}
	if entries, err := os.ReadDir(filepath.Dir(target)); err != nil {
		t.Fatal(err)
	} else if len(entries) != 1 {
		t.Errorf("the target directory holds %d entries, want only the binary (a temp file leaked)", len(entries))
	}
}

// A dry run reports what it would do and touches nothing: not the binary, and not
// even the archive, so asking what an upgrade would install costs no download.
func TestUpgradeDryRunReportsOnly(t *testing.T) {
	release := &fakeRelease{
		tag:      "v9.9.9",
		archive:  releaseArchive(t, "new binary"),
		sums:     checksumFile(strings.Repeat("a", 64)),
		hasSums:  true,
		snapshot: schemaSource(sessionstore.SchemaVersion),
	}
	server := upgradeServer(t, release)
	target := targetBinary(t)
	var out strings.Builder
	opts := upgradeOptionsFor(server, target, &out)
	opts.DryRun = true

	if err := upgradeFromRelease(context.Background(), opts); err != nil {
		t.Fatalf("upgradeFromRelease: %v", err)
	}
	if got := readFile(t, target); got != "old binary" {
		t.Errorf("a dry run changed the target binary to %q", got)
	}
	if got := release.archiveDownloads(); got != 0 {
		t.Errorf("a dry run downloaded the archive %d time(s), want none", got)
	}
	for _, want := range []string{"v9.9.9", "dry run", target} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the dry run does not report %q\n%s", want, out.String())
		}
	}
}

// A forward schema bump is only safe when the release registers the migration that
// carries this machine's sessions forward. Without it the new binary would refuse
// the session, so the upgrade stops before anything is installed.
func TestUpgradeRefusesNewerSchema(t *testing.T) {
	archive := releaseArchive(t, "new binary")
	release := &fakeRelease{
		tag:      "v9.9.9",
		archive:  archive,
		sums:     checksumFile(archiveHash(archive)),
		hasSums:  true,
		snapshot: schemaSource(sessionstore.SchemaVersion + 1),
	}
	server := upgradeServer(t, release)
	target := targetBinary(t)

	err := upgradeFromRelease(context.Background(), upgradeOptionsFor(server, target, io.Discard))
	if err == nil || !strings.Contains(err.Error(), "refusing to continue") {
		t.Fatalf("upgradeFromRelease = %v, want the schema refusal", err)
	}
	if got := readFile(t, target); got != "old binary" {
		t.Errorf("the target binary is %q, want it untouched", got)
	}
	if got := release.archiveDownloads(); got != 0 {
		t.Errorf("the archive was downloaded %d time(s) before the schema guard ran", got)
	}
}

// A release with no checksum file cannot be verified at all, and skipping the check
// would make the whole command a decorative wrapper around curl.
func TestUpgradeRefusesAReleaseWithoutChecksums(t *testing.T) {
	release := &fakeRelease{
		tag:      "v9.9.9",
		archive:  releaseArchive(t, "new binary"),
		snapshot: schemaSource(sessionstore.SchemaVersion),
	}
	server := upgradeServer(t, release)
	target := targetBinary(t)

	err := upgradeFromRelease(context.Background(), upgradeOptionsFor(server, target, io.Discard))
	if err == nil || !strings.Contains(err.Error(), upgradeChecksumAsset) {
		t.Fatalf("upgradeFromRelease = %v, want a refusal naming %s", err, upgradeChecksumAsset)
	}
	if got := readFile(t, target); got != "old binary" {
		t.Errorf("the target binary is %q, want it untouched", got)
	}
	if got := release.archiveDownloads(); got != 0 {
		t.Errorf("the archive was downloaded %d time(s) without a checksum to verify it", got)
	}
}

// The verified path: the archive matches the published checksum and the schema is
// the one this binary already reads, so the executable is replaced with a complete,
// executable file and no temporary file is left beside it.
func TestUpgradeInstallsAVerifiedArchive(t *testing.T) {
	archive := releaseArchive(t, "new binary")
	release := &fakeRelease{
		tag:      "v9.9.9",
		archive:  archive,
		sums:     checksumFile(archiveHash(archive)),
		hasSums:  true,
		snapshot: schemaSource(sessionstore.SchemaVersion),
	}
	server := upgradeServer(t, release)
	target := targetBinary(t)
	var out strings.Builder

	if err := upgradeFromRelease(context.Background(), upgradeOptionsFor(server, target, &out)); err != nil {
		t.Fatalf("upgradeFromRelease: %v", err)
	}
	if got := readFile(t, target); got != "new binary" {
		t.Errorf("the target binary is %q, want the extracted one", got)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("the installed binary is %v, want it executable", info.Mode().Perm())
	}
	if entries, err := os.ReadDir(filepath.Dir(target)); err != nil {
		t.Fatal(err)
	} else if len(entries) != 1 {
		t.Errorf("the target directory holds %d entries, want only the binary (a temp file leaked)", len(entries))
	}
	if !strings.Contains(out.String(), "installed v9.9.9") {
		t.Errorf("the upgrade does not report what it installed\n%s", out.String())
	}
}
