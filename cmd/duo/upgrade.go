package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/atfa/duo/internal/version"
)

// The release `duo upgrade` installs from. It is a constant rather than a flag:
// the release this binary belongs to is the only one whose artifacts it can name,
// and pointing it elsewhere would install a binary from a repository nobody asked
// for.
const (
	upgradeRepo    = "atfa/duo"
	upgradeAPIBase = "https://api.github.com"
	upgradeRawBase = "https://raw.githubusercontent.com"

	// upgradeChecksumAsset is the checksum file the release workflow publishes
	// beside the archives. Without it there is nothing to verify against, and an
	// unverified install is exactly what this command exists to prevent.
	upgradeChecksumAsset = "SHA256SUMS"
)

// upgradeTagPattern is what a release tag this command will install looks like.
var upgradeTagPattern = regexp.MustCompile(`^v[0-9][0-9A-Za-z._-]*$`)

type upgradeArgs struct {
	dryRun bool
	help   bool
}

func parseUpgradeArgs(args []string) (upgradeArgs, error) {
	var out upgradeArgs
	for _, raw := range args {
		arg := strings.TrimSpace(raw)
		switch {
		case arg == "":
		case helpRequested(arg):
			out.help = true
			return out, nil
		case arg == "--dry-run":
			out.dryRun = true
		case strings.HasPrefix(arg, "-"):
			return upgradeArgs{}, fmt.Errorf("unknown flag %q (usage: %s)", arg, commandUsage("upgrade"))
		default:
			return upgradeArgs{}, fmt.Errorf("unexpected argument %q (usage: %s)", arg, commandUsage("upgrade"))
		}
	}
	return out, nil
}

// runUpgrade is `duo upgrade`: it replaces the running binary with the latest
// published release, verified against that release's own checksum file. It never
// restarts anything — the process that called it keeps running the old binary, and
// the new one takes effect on the next start. Under `duo step-loop` the existing
// exit-code handoff is what restarts Duo, so this command needs no restart path of
// its own.
func runUpgrade(ctx context.Context, args []string) error {
	parsed, err := parseUpgradeArgs(args)
	if err != nil {
		return err
	}
	if parsed.help {
		printUsage()
		return nil
	}
	target, err := currentExecutable()
	if err != nil {
		return err
	}
	return upgradeFromRelease(ctx, upgradeOptions{
		Client:  &http.Client{Timeout: 2 * time.Minute},
		APIBase: upgradeAPIBase,
		RawBase: upgradeRawBase,
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
		Target:  target,
		DryRun:  parsed.dryRun,
		Out:     os.Stdout,
	})
}

// currentExecutable resolves the file to replace. A symlinked `duo` is replaced at
// the file it points to, so the link a user installed keeps working.
func currentExecutable() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot find the running Duo binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		return resolved, nil
	}
	return self, nil
}

// upgradeOptions is everything one upgrade run needs from its environment, so the
// whole flow — release lookup, download, checksum, schema guard, replace — can be
// exercised against a local release server without touching a real installation.
type upgradeOptions struct {
	Client  *http.Client
	APIBase string
	RawBase string
	GOOS    string
	GOARCH  string
	Target  string
	DryRun  bool
	Out     io.Writer
}

type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type releaseInfo struct {
	Tag    string         `json:"tag_name"`
	Assets []releaseAsset `json:"assets"`
}

func (r releaseInfo) asset(name string) (releaseAsset, bool) {
	for _, asset := range r.Assets {
		if asset.Name == name {
			return asset, true
		}
	}
	return releaseAsset{}, false
}

func upgradeFromRelease(ctx context.Context, opts upgradeOptions) error {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}

	rel, err := latestRelease(ctx, client, opts.APIBase)
	if err != nil {
		return err
	}
	if rel.Tag == "" {
		return errors.New("the latest release carries no tag")
	}
	if !upgradeTagPattern.MatchString(rel.Tag) {
		// The tag becomes part of the URL the schema check reads from, so it has to
		// look like a Duo version rather than anything the network said.
		return fmt.Errorf("the latest release tag %q is not a Duo version; refusing to install it", rel.Tag)
	}
	if rel.Tag == version.Version {
		fmt.Fprintf(out, "duo upgrade: %s is already the latest release\n", version.Version)
		return nil
	}
	archiveName := fmt.Sprintf("duo_%s_%s.tar.gz", opts.GOOS, opts.GOARCH)
	archive, ok := rel.asset(archiveName)
	if !ok {
		return fmt.Errorf("release %s publishes no %s archive for %s/%s", rel.Tag, archiveName, opts.GOOS, opts.GOARCH)
	}
	sums, ok := rel.asset(upgradeChecksumAsset)
	if !ok {
		// The safe direction: an archive nobody published a checksum for is exactly
		// what this command must not install.
		return fmt.Errorf("release %s publishes no %s, so %s cannot be verified; refusing to install it (use scripts/install-release.sh if you accept that risk)", rel.Tag, upgradeChecksumAsset, archiveName)
	}

	fmt.Fprintf(out, "duo upgrade: %s -> %s (%s)\n", version.Version, rel.Tag, archiveName)
	if opts.DryRun {
		fmt.Fprintf(out, "duo upgrade: dry run — would verify %s against %s and replace %s; nothing was downloaded or changed\n", archiveName, upgradeChecksumAsset, opts.Target)
		return nil
	}

	want, err := releaseChecksum(ctx, client, sums.URL, archiveName)
	if err != nil {
		return err
	}
	// The schema guard runs on the release's own source, before anything is
	// replaced: installing first would leave a binary that cannot read this
	// machine's sessions and no way back through this command.
	if err := checkReleaseSchema(ctx, client, opts.RawBase, rel.Tag); err != nil {
		return err
	}
	got, err := installArchive(ctx, client, archive.URL, want, opts.Target)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "duo upgrade: installed %s at %s (sha256 %s)\n", rel.Tag, opts.Target, got[:12])
	fmt.Fprintf(out, "duo upgrade: this process keeps running %s; the new binary takes effect on the next start\n", version.Version)
	return nil
}

func latestRelease(ctx context.Context, client *http.Client, apiBase string) (releaseInfo, error) {
	url := strings.TrimRight(apiBase, "/") + "/repos/" + upgradeRepo + "/releases/latest"
	body, err := getBody(ctx, client, url, 4<<20)
	if err != nil {
		return releaseInfo{}, fmt.Errorf("look up the latest Duo release: %w", err)
	}
	var rel releaseInfo
	if err := json.Unmarshal(body, &rel); err != nil {
		return releaseInfo{}, fmt.Errorf("read the latest release: %w", err)
	}
	return rel, nil
}

// releaseChecksum reads the one hash that belongs to this archive. Names are
// compared by base name so the `./` prefix `sha256sum ./*.tar.gz` writes, and any
// directory a publisher chose, cannot make a published archive look unlisted.
func releaseChecksum(ctx context.Context, client *http.Client, url, archiveName string) (string, error) {
	body, err := getBody(ctx, client, url, 1<<20)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", upgradeChecksumAsset, err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if filepath.Base(fields[1]) != archiveName {
			continue
		}
		hash := strings.ToLower(fields[0])
		if len(hash) != sha256.Size*2 {
			return "", fmt.Errorf("%s lists a malformed sha256 for %s", upgradeChecksumAsset, archiveName)
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return "", fmt.Errorf("%s lists a malformed sha256 for %s", upgradeChecksumAsset, archiveName)
		}
		return hash, nil
	}
	return "", fmt.Errorf("%s in release does not list %s; refusing to install an archive the release does not account for", upgradeChecksumAsset, archiveName)
}

// checkReleaseSchema applies the same guard the step-loop install uses, to the same
// thing: the source the new binary is compiled from. The two files it reads are
// fetched from the release tag, so a forward schema bump is accepted only when that
// tag registers the migration carrying this machine's sessions forward.
func checkReleaseSchema(ctx context.Context, client *http.Client, rawBase, tag string) error {
	dir, err := os.MkdirTemp("", "duo-upgrade-schema-")
	if err != nil {
		return fmt.Errorf("prepare the schema check: %w", err)
	}
	defer os.RemoveAll(dir)
	store := filepath.Join(dir, "internal", "sessionstore")
	if err := os.MkdirAll(store, 0o700); err != nil {
		return fmt.Errorf("prepare the schema check: %w", err)
	}

	snapshot, err := rawSourceFile(ctx, client, rawBase, tag, "snapshot.go")
	if err != nil {
		if errors.Is(err, errSourceAbsent) {
			// Without the schema the new binary writes, this command cannot tell
			// whether it can read this machine's sessions. Refusing keeps the working
			// binary in place, and the script install stays available.
			return fmt.Errorf("release %s publishes no internal/sessionstore/snapshot.go, so its session schema cannot be checked; refusing to install it (use scripts/install-release.sh to install without that check)", tag)
		}
		return fmt.Errorf("read the release source for the schema check: %w", err)
	}
	if err := os.WriteFile(filepath.Join(store, "snapshot.go"), snapshot, 0o600); err != nil {
		return fmt.Errorf("prepare the schema check: %w", err)
	}
	// A tag with no migration registry is not an error here: the guard refuses a
	// forward schema bump on its own, which is the answer that matters.
	if migrate, err := rawSourceFile(ctx, client, rawBase, tag, "migrate.go"); err == nil {
		if err := os.WriteFile(filepath.Join(store, "migrate.go"), migrate, 0o600); err != nil {
			return fmt.Errorf("prepare the schema check: %w", err)
		}
	} else if !errors.Is(err, errSourceAbsent) {
		return fmt.Errorf("read the release source for the schema check: %w", err)
	}
	return checkSchemaCompatible(dir)
}

var errSourceAbsent = errors.New("source file absent")

func rawSourceFile(ctx context.Context, client *http.Client, rawBase, tag, name string) ([]byte, error) {
	url := strings.TrimRight(rawBase, "/") + "/" + upgradeRepo + "/" + tag + "/internal/sessionstore/" + name
	body, err := getBody(ctx, client, url, 1<<20)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return nil, errSourceAbsent
		}
		return nil, err
	}
	return body, nil
}

// installArchive downloads the release archive, proves it is the one the release
// published, and replaces the binary atomically: a fully written and fsynced temp
// file in the target's own directory is renamed over it, so a crash or a failed
// download can never leave a half-written executable in place.
func installArchive(ctx context.Context, client *http.Client, url, want, target string) (string, error) {
	dir := filepath.Dir(target)

	// The archive is verified as a whole before anything is unpacked: hashing only
	// the bytes the extractor happened to read would let the rest of the file be
	// anything at all.
	download, err := os.CreateTemp(dir, ".duo-upgrade-download-*")
	if err != nil {
		return "", fmt.Errorf("prepare the download next to %s: %w", target, err)
	}
	defer func() {
		download.Close()
		os.Remove(download.Name())
	}()
	hasher := sha256.New()
	body, err := openBody(ctx, client, url)
	if err != nil {
		return "", fmt.Errorf("download the release archive: %w", err)
	}
	_, copyErr := io.Copy(io.MultiWriter(download, hasher), body)
	body.Close()
	if copyErr != nil {
		return "", fmt.Errorf("download the release archive: %w", copyErr)
	}
	got := hex.EncodeToString(hasher.Sum(nil))
	if got != want {
		return "", fmt.Errorf("the downloaded archive does not match %s: got sha256 %s, want %s; the installed binary is untouched", upgradeChecksumAsset, got, want)
	}
	if _, err := download.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("read the downloaded archive: %w", err)
	}
	binary, err := extractBinary(download)
	if err != nil {
		return "", err
	}

	tmp, err := os.CreateTemp(dir, ".duo-upgrade-*")
	if err != nil {
		return "", fmt.Errorf("prepare the new binary next to %s: %w", target, err)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()
	if _, err := tmp.Write(binary); err != nil {
		return "", fmt.Errorf("write the new binary: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		return "", fmt.Errorf("make the new binary executable: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("flush the new binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close the new binary: %w", err)
	}
	if err := replaceBinary(tmpName, target); err != nil {
		return "", err
	}
	syncDir(dir)
	return got, nil
}

// extractBinary reads the `duo` executable out of the release archive. Everything
// else in the archive belongs to the script installer, and duplicating that install
// path here is the second chain this command exists to avoid. A Windows build names
// its executable duo.exe, so both spellings are the binary this command wants.
func extractBinary(r io.Reader) ([]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("read the release archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read the release archive: %w", err)
		}
		name := filepath.Clean(header.Name)
		if header.Typeflag != tar.TypeReg || (name != "duo" && name != "duo.exe") {
			continue
		}
		binary, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("read duo from the release archive: %w", err)
		}
		return binary, nil
	}
	return nil, errors.New("the release archive contains no duo executable")
}

// syncDir flushes a directory entry after a rename. A binary that exists only in
// the page cache survives a process crash but not a power loss, and this is the
// file the next start depends on.
func syncDir(dir string) {
	handle, err := os.Open(dir)
	if err != nil {
		return
	}
	defer handle.Close()
	_ = handle.Sync()
}

var errNotFound = errors.New("not found")

func getBody(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	body, err := openBody(ctx, client, url)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(io.LimitReader(body, limit))
}

func openBody(ctx context.Context, client *http.Client, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// GitHub rejects an API request with no User-Agent, and a named client is also
	// what makes a failed upgrade attributable in a proxy log.
	req.Header.Set("User-Agent", "duo-upgrade/"+version.Version)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		return resp.Body, nil
	case resp.StatusCode == http.StatusNotFound:
		resp.Body.Close()
		return nil, errNotFound
	default:
		resp.Body.Close()
		return nil, fmt.Errorf("%s answered %s", req.URL.Redacted(), resp.Status)
	}
}
