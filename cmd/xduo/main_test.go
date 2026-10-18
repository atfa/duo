package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestLaunchUsesSiblingBinariesAndPreservesArguments(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin with spaces")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"duo", rendererName} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("binary"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	command, args, env, err := launch("linux", filepath.Join(bin, "xduo"), []string{"--initial-project", "/tmp/my repo"}, func(string) string { return "" }, func(string) (string, error) { t.Fatal("unexpected PATH lookup"); return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	if command != filepath.Join(bin, "duo") || !reflect.DeepEqual(args, []string{"gui", "--welcome", "--initial-project", "/tmp/my repo"}) {
		t.Fatalf("launch = %q %#v", command, args)
	}
	if !strings.Contains(strings.Join(env, "\n"), "DUO_GPUI_BIN="+filepath.Join(bin, rendererName)) {
		t.Fatalf("renderer environment missing: %v", env)
	}
}

func TestLaunchHonorsRendererOverrideAndFindsDuoOnPath(t *testing.T) {
	dir := t.TempDir()
	duo := filepath.Join(dir, "duo")
	renderer := filepath.Join(dir, "renderer override")
	for _, path := range []string{duo, renderer} {
		if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	command, _, env, err := launch("linux", filepath.Join(t.TempDir(), "xduo"), nil, func(key string) string {
		if key == "DUO_GPUI_BIN" {
			return renderer
		}
		return ""
	}, func(name string) (string, error) {
		if name != "duo" {
			t.Fatalf("unexpected lookup %q", name)
		}
		return duo, nil
	})
	if err != nil || command != duo || !strings.Contains(strings.Join(env, "\n"), "DUO_GPUI_BIN="+renderer) {
		t.Fatalf("launch = %q %v %v", command, env, err)
	}
}

func TestLaunchReportsMissingHostOrRenderer(t *testing.T) {
	dir := t.TempDir()
	_, _, _, err := launch("linux", filepath.Join(dir, "xduo"), nil, func(string) string { return "" }, func(string) (string, error) { return "", os.ErrNotExist })
	if err == nil || !strings.Contains(err.Error(), "duo was not found") {
		t.Fatalf("missing host error = %v", err)
	}
	duo := filepath.Join(dir, "duo")
	if err := os.WriteFile(duo, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = launch("linux", filepath.Join(dir, "xduo"), nil, func(string) string { return "" }, func(name string) (string, error) {
		if name == "duo" {
			return "", nil
		}
		return "", os.ErrNotExist
	})
	if err == nil || !strings.Contains(err.Error(), rendererName) {
		t.Fatalf("missing renderer error = %v", err)
	}
}

func TestMacLaunchUsesRecordedBundle(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(t.TempDir(), "xduo Duo.app")
	if err := os.Mkdir(bundle, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "xduo.app-path"), []byte(bundle), 0o644); err != nil {
		t.Fatal(err)
	}
	command, args, _, err := launch("darwin", filepath.Join(dir, "xduo"), []string{"--project", "/repo with spaces"}, func(string) string { return "" }, func(string) (string, error) { t.Fatal("bundle should bypass PATH"); return "", nil })
	if err != nil || command != "/usr/bin/open" || !reflect.DeepEqual(args, []string{"-a", bundle, "--args", "--project", "/repo with spaces"}) {
		t.Fatalf("launch = %q %#v, %v", command, args, err)
	}
}

func TestMacArchiveUsesSiblingBundle(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "xduo.app")
	if err := os.Mkdir(bundle, 0o755); err != nil {
		t.Fatal(err)
	}
	command, args, _, err := launch("darwin", filepath.Join(dir, "xduo"), nil, func(string) string { return "" }, func(string) (string, error) { t.Fatal("bundle should bypass PATH"); return "", nil })
	if err != nil || command != "/usr/bin/open" || !reflect.DeepEqual(args, []string{"-a", bundle, "--args"}) {
		t.Fatalf("launch = %q %#v, %v", command, args, err)
	}
}

func TestExecutableDispatchesThroughPATHAndPreservesExitStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses Unix executables")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin with spaces")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"duo":        "#!/bin/sh\nprintf '%s\\n' \"$@\" >\"$CAPTURE\"\nprintf '%s' \"$DUO_GPUI_BIN\" >>\"$CAPTURE\"\nexit 23\n",
		rendererName: "#!/bin/sh\nexit 0\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	launcher := filepath.Join(bin, "xduo")
	build := exec.Command("go", "build", "-o", launcher, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build launcher: %v\n%s", err, output)
	}
	capture := filepath.Join(dir, "captured args")
	t.Setenv("PATH", bin)
	t.Setenv("DUO_GPUI_BIN", "")
	cmd := exec.Command("xduo", "--initial-project", "/repo with spaces")
	cmd.Env = append(os.Environ(), "CAPTURE="+capture)
	cmd.Dir = dir
	if err := cmd.Run(); err == nil {
		t.Fatal("launcher unexpectedly returned success")
	} else {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 {
			t.Fatalf("launcher error = %v, want exit 23", err)
		}
	}
	got, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	want := "gui\n--welcome\n--initial-project\n/repo with spaces\n" + filepath.Join(bin, rendererName)
	if string(got) != want {
		t.Fatalf("dispatch = %q, want %q", got, want)
	}
}

func TestMacRendererOverrideSkipsAppBundle(t *testing.T) {
	dir := t.TempDir()
	duo := filepath.Join(dir, "duo")
	renderer := filepath.Join(dir, "renderer")
	for _, path := range []string{duo, renderer} {
		if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	command, args, _, err := launch("darwin", filepath.Join(dir, "xduo"), nil, func(key string) string {
		if key == "DUO_GPUI_BIN" {
			return renderer
		}
		return ""
	}, func(string) (string, error) { return "", os.ErrNotExist })
	if err != nil || command != duo || !reflect.DeepEqual(args, []string{"gui", "--welcome"}) {
		t.Fatalf("launch = %q %#v, %v", command, args, err)
	}
}

func TestMacRejectsStaleAppPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "xduo.app-path"), []byte(filepath.Join(dir, "missing.app")), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := launch("darwin", filepath.Join(dir, "xduo"), nil, func(string) string { return "" }, func(string) (string, error) { return "", os.ErrNotExist })
	if err == nil || !strings.Contains(err.Error(), "app bundle is missing") {
		t.Fatalf("stale bundle error = %v", err)
	}
}

func TestMacPackageContainsRendererIconAndForwardsLaunchArguments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("packager uses Unix shell tools")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	bin := filepath.Join(work, "tools")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, contents string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(contents), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(bin, "uname"), "#!/bin/sh\nprintf 'Darwin\\n'\n", 0o755)
	write(filepath.Join(work, "renderer"), "#!/bin/sh\nexit 0\n", 0o755)
	write(filepath.Join(work, "duo"), "#!/bin/sh\nprintf '<%s>' \"$@\" >\"$DUO_CAPTURE_FILE\"\n", 0o755)
	bundle := filepath.Join(work, "xduo bundle.app")
	path := bin + string(os.PathListSeparator) + os.Getenv("PATH")
	packageCmd := exec.Command("bash", filepath.Join(root, "scripts", "package-xduo-macos.sh"), bundle, filepath.Join(work, "renderer"), filepath.Join(work, "duo"), work)
	packageCmd.Env = append(os.Environ(), "PATH="+path, "HOME="+work)
	if output, err := packageCmd.CombinedOutput(); err != nil {
		t.Fatalf("package app: %v\n%s", err, output)
	}
	contents := filepath.Join(bundle, "Contents")
	for _, required := range []string{"MacOS/xduo-renderer", "Resources/xduo.icns", "Info.plist"} {
		if _, err := os.Stat(filepath.Join(contents, required)); err != nil {
			t.Fatalf("packaged file %s: %v", required, err)
		}
	}
	capture := filepath.Join(work, "args")
	launcher := filepath.Join(contents, "MacOS", "xduo")
	cmd := exec.Command(launcher, "--initial-project", "/repo with spaces")
	cmd.Env = append(os.Environ(), "PATH="+path, "HOME="+work, "DUO_CAPTURE_FILE="+capture)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("launch packaged app: %v\n%s", err, output)
	}
	got, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "<gui><--welcome><--initial-project></repo with spaces>" {
		t.Fatalf("packaged app arguments = %q", got)
	}
}

func TestLinuxDesktopInstallWrapperFindsHostAndRendererWithSpaces(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("desktop installer uses Unix shell tools")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	bin := filepath.Join(work, "bin with spaces")
	data := filepath.Join(work, "data with spaces")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"duo", rendererName} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	capture := filepath.Join(work, "launcher environment")
	launcherBody := "#!/bin/sh\nprintf '%s\\n%s\\n' \"$DUO_GPUI_BIN\" \"$PATH\" >\"$CAPTURE\"\nprintf '<%s>' \"$@\" >>\"$CAPTURE\"\n"
	if err := os.WriteFile(filepath.Join(bin, "xduo"), []byte(launcherBody), 0o755); err != nil {
		t.Fatal(err)
	}
	install := exec.Command("bash", filepath.Join(root, "scripts", "install-xduo-desktop.sh"))
	install.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+work, "XDG_DATA_HOME="+data)
	if output, err := install.CombinedOutput(); err != nil {
		t.Fatalf("install desktop entry: %v\n%s", err, output)
	}
	launcher := filepath.Join(data, "applications", "xduo-launcher")
	cmd := exec.Command(launcher, "--welcome")
	cmd.Env = append(os.Environ(), "PATH=/usr/bin:/bin", "HOME="+work, "CAPTURE="+capture)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run desktop wrapper: %v\n%s", err, output)
	}
	got, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(bin, rendererName) + "\n" + bin + ":/usr/bin:/bin\n<--welcome>"
	if string(got) != want {
		t.Fatalf("desktop wrapper environment = %q, want %q", got, want)
	}
}
