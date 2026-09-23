package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/atfa/duo/internal/harness"
)

type config struct {
	listen         string
	harnessEnabled bool
	harness        harness.Config

	repository   string
	worktreeRoot string
	session      string
	baseRef      string
	piCommand    string
}

func loadConfig(args []string) config {
	cwd, _ := os.Getwd()
	repo := strings.TrimSpace(os.Getenv("DUO_REPO"))
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		repo = args[0]
	}
	if repo == "" {
		repo = cwd
	}
	if abs, err := filepath.Abs(repo); err == nil {
		repo = abs
	}

	session := strings.TrimSpace(os.Getenv("DUO_SESSION"))
	if session == "" {
		session = defaultSession()
	}

	return config{
		listen:         envString("DUO_LISTEN", "127.0.0.1:0"),
		harnessEnabled: envBool("DUO_HARNESS", true),
		harness: harness.Config{
			IdleThreshold:  time.Duration(envInt("DUO_HARNESS_IDLE_SECONDS", 15)) * time.Second,
			StallThreshold: time.Duration(envInt("DUO_HARNESS_STALL_SECONDS", 300)) * time.Second,
			Cooldown:       time.Duration(envInt("DUO_HARNESS_COOLDOWN_SECONDS", 30)) * time.Second,
			TickInterval:   2 * time.Second,
		},
		repository:   repo,
		worktreeRoot: strings.TrimSpace(os.Getenv("DUO_WORKTREE_ROOT")),
		session:      session,
		baseRef:      envString("DUO_BASE_REF", "HEAD"),
		piCommand:    envString("DUO_PI_COMMAND", "pi"),
	}
}

func defaultSession() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("generate Duo session: %w", err))
	}
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

func (c config) validate() error {
	if strings.TrimSpace(c.repository) == "" {
		return fmt.Errorf("repository is empty")
	}
	if strings.TrimSpace(c.piCommand) == "" {
		return fmt.Errorf("DUO_PI_COMMAND is empty")
	}
	return nil
}

func envString(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func envInt(name string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func envBool(name string, fallback bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	if v == "" {
		return fallback
	}
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
