package xduo

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var ErrTunnelNotConfigured = errors.New("tunnel is not configured")

const TunnelSetupURL = "https://desktop.xduo.dev/setup"

type Tunnel struct {
	URL     string
	LogPath string
	cmd     *exec.Cmd
	done    chan struct{}
	err     error
	ready   bool
}

// StartTunnel runs the user's named Cloudflare tunnel in a separate process;
// credentials stay in the local configuration, never in the xduo executable.
func StartTunnel(dir, localURL, entryPath string) (*Tunnel, error) {
	configPath := filepath.Join(dir, "tunnel.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrTunnelNotConfigured, configPath)
		}
		return nil, fmt.Errorf("read %s: %w", configPath, err)
	}
	var cfg struct {
		Hostname        string `json:"hostname"`
		Tunnel          string `json:"tunnel"`
		CredentialsFile string `json:"credentialsFile"`
	}
	if err = json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	host, err := url.Parse("https://" + cfg.Hostname)
	if err != nil || host.Hostname() == "" || host.Host != cfg.Hostname || host.Path != "" || cfg.Tunnel == "" || !filepath.IsAbs(cfg.CredentialsFile) {
		return nil, fmt.Errorf("invalid tunnel configuration in %s", configPath)
	}
	if _, err = os.Stat(cfg.CredentialsFile); err != nil {
		return nil, err
	}
	executable, err := exec.LookPath("cloudflared")
	if err != nil {
		return nil, fmt.Errorf("install cloudflared to use Tunnel: %w", err)
	}
	// JSON-quoted strings are also valid YAML strings; paths stay literal.
	quote := func(s string) string { v, _ := json.Marshal(s); return string(v) }
	origin, err := url.Parse(localURL)
	if err != nil || origin.Scheme != "http" || origin.Hostname() != "127.0.0.1" {
		return nil, fmt.Errorf("invalid local server URL")
	}
	yaml := "tunnel: " + quote(cfg.Tunnel) + "\ncredentials-file: " + quote(cfg.CredentialsFile) + "\nprotocol: http2\ningress:\n  - hostname: " + quote(cfg.Hostname) + "\n    service: " + quote(strings.TrimSuffix(localURL, "/")) + "\n  - service: http_status:404\n"
	yamlPath := filepath.Join(dir, "tunnel.yml")
	if err = os.WriteFile(yamlPath, []byte(yaml), 0600); err != nil {
		return nil, err
	}
	logPath := filepath.Join(dir, "tunnel.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(executable, "--no-autoupdate", "--config", yamlPath, "tunnel", "run", cfg.Tunnel)
	cmd.Stdout = log
	cmd.Stderr = log
	if err = cmd.Start(); err != nil {
		log.Close()
		return nil, err
	}
	t := &Tunnel{URL: "https://" + cfg.Hostname + entryPath, LogPath: logPath, cmd: cmd, done: make(chan struct{})}
	go func() { t.err = cmd.Wait(); log.Close(); close(t.done) }()
	return t, nil
}
func (t *Tunnel) Running() bool {
	select {
	case <-t.done:
		return false
	default:
		return true
	}
}
func (t *Tunnel) Ready() bool {
	if !t.Running() {
		return false
	}
	if !t.ready {
		data, _ := os.ReadFile(t.LogPath)
		t.ready = strings.Contains(string(data), "Registered tunnel connection")
	}
	return t.ready
}
func (t *Tunnel) Failure() error {
	select {
	case <-t.done:
		return fmt.Errorf("tunnel stopped: %v; details: %s", t.err, t.LogPath)
	default:
		return nil
	}
}
func (t *Tunnel) Close() error {
	if !t.Running() {
		return nil
	}
	_ = t.cmd.Process.Signal(os.Interrupt)
	select {
	case <-t.done:
	case <-time.After(2 * time.Second):
		_ = t.cmd.Process.Kill()
		<-t.done
	}
	return nil
}
