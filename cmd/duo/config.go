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
	launchDir    string
	worktreeRoot string
	session      string
	baseRef      string
	piCommand    string

	resume        bool
	resumeSession string
}

// cliArgs is the parsed command line.
type cliArgs struct {
	repository string
	resume     bool
	sessionID  string
}

// parseArgs understands `duo [repository] [--resume [session-id]]`. Plain `duo`
// always starts a new session; only --resume attaches to persisted state.
func parseArgs(args []string) (cliArgs, error) {
	var out cliArgs
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "":
		case arg == "--resume" || arg == "-r":
			out.resume = true
			if i+1 < len(args) && !strings.HasPrefix(strings.TrimSpace(args[i+1]), "-") {
				out.sessionID = strings.TrimSpace(args[i+1])
				i++
			}
		case strings.HasPrefix(arg, "--resume="):
			out.resume = true
			out.sessionID = strings.TrimPrefix(arg, "--resume=")
		case strings.HasPrefix(arg, "-"):
			return out, fmt.Errorf("unknown Duo flag %q (usage: duo [git-repository] [--resume [session-id]])", arg)
		default:
			if out.repository != "" {
				return out, fmt.Errorf("unexpected extra argument %q", arg)
			}
			out.repository = arg
		}
	}
	if out.resume && out.sessionID == "" {
		// Duo chooses the newest unfinished session.
	}
	return out, nil
}

func loadConfig(args []string) (config, error) {
	parsed, err := parseArgs(args)
	if err != nil {
		return config{}, err
	}

	cwd, _ := os.Getwd()
	launch := strings.TrimSpace(os.Getenv("DUO_REPO"))
	if parsed.repository != "" {
		launch = parsed.repository
	}
	if launch == "" {
		launch = cwd
	}
	if abs, err := filepath.Abs(launch); err == nil {
		launch = abs
	}

	session := strings.TrimSpace(os.Getenv("DUO_SESSION"))
	if session == "" {
		session = defaultSession()
	}

	harnessConfig := harness.Config{
		IdleThreshold:  time.Duration(envInt("DUO_HARNESS_IDLE_SECONDS", 15)) * time.Second,
		StallThreshold: time.Duration(envInt("DUO_HARNESS_STALL_SECONDS", 300)) * time.Second,
		Cooldown:       time.Duration(envInt("DUO_HARNESS_COOLDOWN_SECONDS", 30)) * time.Second,
		TickInterval:   2 * time.Second,
	}
	if parsed.resume {
		// A resumed session needs a grace period: Pi must reconnect and reload
		// context, and that must not be mistaken for an idle or stalled run.
		harnessConfig.RecoveryGrace = time.Duration(envInt("DUO_HARNESS_RESUME_GRACE_SECONDS", 45)) * time.Second
	}

	return config{
		listen:         envString("DUO_LISTEN", "127.0.0.1:0"),
		harnessEnabled: envBool("DUO_HARNESS", true),
		harness:        harnessConfig,
		repository:     launch,
		launchDir:      launch,
		worktreeRoot:   strings.TrimSpace(os.Getenv("DUO_WORKTREE_ROOT")),
		session:        session,
		baseRef:        envString("DUO_BASE_REF", "HEAD"),
		piCommand:      envString("DUO_PI_COMMAND", "pi"),
		resume:         parsed.resume,
		resumeSession:  parsed.sessionID,
	}, nil
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
