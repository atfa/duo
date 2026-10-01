package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/models"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/workspace"
)

type config struct {
	listen         string
	harnessEnabled bool
	harness        harness.Config

	repository    string
	launchDir     string
	worktreeRoot  string
	session       string
	baseRef       string
	piCommand     string
	testCommand   string
	agentCommands map[protocol.AgentID]string
	agentDrivers  map[protocol.AgentID]string
	agentModels   map[protocol.AgentID]string

	driverExplicit      bool
	agentDriverExplicit map[protocol.AgentID]bool

	resume        bool
	resumeSession string

	// mode is the resolved workflow of a NEW session. For --resume the persisted
	// mode wins, so only the explicit CLI intent is recorded here and validated
	// once the session is loaded.
	mode         project.Mode
	modeSource   string
	modeRaw      string
	modeExplicit bool
}

func (c config) agentCommand(agent protocol.AgentID) string {
	if cmd, ok := c.agentCommands[agent]; ok && cmd != "" {
		return cmd
	}
	return c.piCommand
}

func (c config) agentDriver(agent protocol.AgentID) string {
	if drv, ok := c.agentDrivers[agent]; ok && drv != "" {
		return drv
	}
	return "pi"
}

func (c config) agentModel(agent protocol.AgentID) string {
	if m, ok := c.agentModels[agent]; ok && m != "" {
		return m
	}
	return models.DefaultModelForDriver(c.agentDriver(agent))
}

// configFile describes ~/.duo/config.json or .duo/config.json.
type configFile struct {
	Mode        string               `json:"mode,omitempty"`
	PiCommand   string               `json:"piCommand,omitempty"`
	Driver      string               `json:"driver,omitempty"`
	TestCommand string               `json:"testCommand,omitempty"`
	Agents      map[string]agentFile `json:"agents,omitempty"`
	Harness     harnessFile          `json:"harness,omitempty"`
}

type agentFile struct {
	Command  string `json:"command,omitempty"`
	Driver   string `json:"driver,omitempty"`
	Model    string `json:"model,omitempty"`
	Thinking string `json:"thinking,omitempty"`
}

type harnessFile struct {
	Enabled         *bool `json:"enabled,omitempty"`
	IdleSeconds     int   `json:"idleSeconds,omitempty"`
	StallSeconds    int   `json:"stallSeconds,omitempty"`
	CooldownSeconds int   `json:"cooldownSeconds,omitempty"`
}

// cliArgs is the parsed command line.
type cliArgs struct {
	repository   string
	resume       bool
	sessionID    string
	mode         string
	modeExplicit bool
	testCommand  string
	driver       string
	austinDriver string
	tonyDriver   string
}

// parseArgs understands `duo [repository] [--mode fast|goal] [--resume [id]]`.
// Plain `duo` always starts a new session; only --resume attaches to persisted
// state.
func parseArgs(args []string) (cliArgs, error) {
	var out cliArgs
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "":
		case arg == "--resume" || arg == "-r" || arg == "resume":
			out.resume = true
			if i+1 < len(args) && !strings.HasPrefix(strings.TrimSpace(args[i+1]), "-") {
				cand := strings.TrimSpace(args[i+1])
				if strings.ContainsAny(cand, `/\`) || cand == "." || cand == ".." {
					if out.repository == "" {
						out.repository = cand
						i++
					}
				} else {
					out.sessionID = cand
					i++
				}
			}
		case strings.HasPrefix(arg, "--resume="):
			out.resume = true
			out.sessionID = strings.TrimPrefix(arg, "--resume=")
		case arg == "--mode" || arg == "-m":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--mode requires a value (fast or goal)")
			}
			out.mode = strings.TrimSpace(args[i+1])
			out.modeExplicit = true
			i++
		case strings.HasPrefix(arg, "--mode=") || strings.HasPrefix(arg, "-m="):
			out.mode = strings.TrimSpace(strings.SplitN(arg, "=", 2)[1])
			out.modeExplicit = true
		case arg == "--test-cmd":
			if i+1 >= len(args) {
				return out, fmt.Errorf("--test-cmd requires a value (e.g. \"go test ./...\")")
			}
			out.testCommand = strings.TrimSpace(args[i+1])
			i++
		case strings.HasPrefix(arg, "--test-cmd="):
			out.testCommand = strings.TrimSpace(strings.TrimPrefix(arg, "--test-cmd="))
		case arg == "--agent" || arg == "--driver":
			if i+1 >= len(args) {
				return out, fmt.Errorf("%s requires a value (pi or agy)", arg)
			}
			out.driver = strings.TrimSpace(args[i+1])
			i++
		case strings.HasPrefix(arg, "--agent=") || strings.HasPrefix(arg, "--driver="):
			out.driver = strings.TrimSpace(strings.SplitN(arg, "=", 2)[1])
		case arg == "--austin-driver" || arg == "--austin-agent":
			if i+1 >= len(args) {
				return out, fmt.Errorf("%s requires a value", arg)
			}
			out.austinDriver = strings.TrimSpace(args[i+1])
			i++
		case strings.HasPrefix(arg, "--austin-driver=") || strings.HasPrefix(arg, "--austin-agent="):
			out.austinDriver = strings.TrimSpace(strings.SplitN(arg, "=", 2)[1])
		case arg == "--tony-driver" || arg == "--tony-agent":
			if i+1 >= len(args) {
				return out, fmt.Errorf("%s requires a value", arg)
			}
			out.tonyDriver = strings.TrimSpace(args[i+1])
			i++
		case strings.HasPrefix(arg, "--tony-driver=") || strings.HasPrefix(arg, "--tony-agent="):
			out.tonyDriver = strings.TrimSpace(strings.SplitN(arg, "=", 2)[1])
		case strings.HasPrefix(arg, "-"):
			return out, fmt.Errorf("unknown Duo flag %q (usage: duo [git-repository] [--mode fast|goal] [--test-cmd <command>] [--agent pi|agy] [--resume [session-id]])", arg)
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

// resolveNewMode applies the documented precedence for a NEW session:
// explicit --mode > DUO_MODE > config file > built-in default (fast).
func resolveNewMode(parsed cliArgs, fileMode ...string) (project.Mode, string, error) {
	if parsed.modeExplicit {
		mode, err := project.ParseMode(parsed.mode)
		if err != nil {
			return "", "", err
		}
		return mode, "cli", nil
	}
	if raw := strings.TrimSpace(os.Getenv("DUO_MODE")); raw != "" {
		mode, err := project.ParseMode(raw)
		if err != nil {
			return "", "", fmt.Errorf("invalid DUO_MODE: %w", err)
		}
		return mode, "env", nil
	}
	if len(fileMode) > 0 && fileMode[0] != "" {
		mode, err := project.ParseMode(fileMode[0])
		if err != nil {
			return "", "", fmt.Errorf("invalid config file mode %q: %w", fileMode[0], err)
		}
		return mode, "config", nil
	}
	return project.DefaultMode, "default", nil
}

// resumeMode decides the mode of a resumed session. Mode is fixed per session,
// so the persisted mode always wins: an explicit --mode that disagrees is an
// operator error, while an ambient DUO_MODE is only a warning so an exported
// environment can never break resume.
func resumeMode(cfg config, persisted project.Mode, sessionID string, warn func(string)) (project.Mode, error) {
	if cfg.modeExplicit {
		mode, err := project.ParseMode(cfg.modeRaw)
		if err != nil {
			return "", err
		}
		if mode != persisted {
			return "", fmt.Errorf("session %s is a %s session; mode is fixed per session, so --mode %s cannot be applied", sessionID, persisted, mode)
		}
		return persisted, nil
	}
	if raw := strings.TrimSpace(os.Getenv("DUO_MODE")); raw != "" {
		mode, err := project.ParseMode(raw)
		switch {
		case err != nil:
			warn(fmt.Sprintf("ignoring invalid DUO_MODE %q while resuming: %v", raw, err))
		case mode != persisted:
			warn(fmt.Sprintf("ignoring DUO_MODE=%s while resuming: session %s is a %s session", mode, sessionID, persisted))
		}
	}
	return persisted, nil
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

	fileCfg, err := loadMergedConfigFile(launch)
	if err != nil {
		return config{}, err
	}

	session := strings.TrimSpace(os.Getenv("DUO_SESSION"))
	if session == "" {
		session = defaultSession()
	}

	idleFallback := 15
	if fileCfg.Harness.IdleSeconds > 0 {
		idleFallback = fileCfg.Harness.IdleSeconds
	}
	stallFallback := 300
	if fileCfg.Harness.StallSeconds > 0 {
		stallFallback = fileCfg.Harness.StallSeconds
	}
	cooldownFallback := 30
	if fileCfg.Harness.CooldownSeconds > 0 {
		cooldownFallback = fileCfg.Harness.CooldownSeconds
	}

	harnessConfig := harness.Config{
		IdleThreshold:  time.Duration(envInt("DUO_HARNESS_IDLE_SECONDS", idleFallback)) * time.Second,
		StallThreshold: time.Duration(envInt("DUO_HARNESS_STALL_SECONDS", stallFallback)) * time.Second,
		Cooldown:       time.Duration(envInt("DUO_HARNESS_COOLDOWN_SECONDS", cooldownFallback)) * time.Second,
		TickInterval:   2 * time.Second,
	}
	if parsed.resume {
		// A resumed session needs a grace period: Pi must reconnect and reload
		// context, and that must not be mistaken for an idle or stalled run.
		harnessConfig.RecoveryGrace = time.Duration(envInt("DUO_HARNESS_RESUME_GRACE_SECONDS", 45)) * time.Second
	}

	harnessEnabled := true
	if fileCfg.Harness.Enabled != nil {
		harnessEnabled = *fileCfg.Harness.Enabled
	}
	harnessEnabled = envBool("DUO_HARNESS", harnessEnabled)

	defaultPiCmd := "pi"
	if fileCfg.PiCommand != "" {
		defaultPiCmd = fileCfg.PiCommand
	}
	piCommand := envString("DUO_PI_COMMAND", defaultPiCmd)

	agentCommands := make(map[protocol.AgentID]string)
	agentDrivers := make(map[protocol.AgentID]string)
	agentModels := make(map[protocol.AgentID]string)
	agentDriverExplicit := make(map[protocol.AgentID]bool)
	driverExplicit := parsed.driver != "" || os.Getenv("DUO_DRIVER") != ""

	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		key := strings.ToLower(string(id))
		agentCfg := fileCfg.Agents[key]

		isExplicit := driverExplicit
		if id == protocol.Austin && parsed.austinDriver != "" {
			isExplicit = true
		}
		if id == protocol.Tony && parsed.tonyDriver != "" {
			isExplicit = true
		}
		agentDriverExplicit[id] = isExplicit

		driverType := "pi"
		if fileCfg.Driver != "" {
			driverType = fileCfg.Driver
		}
		if agentCfg.Driver != "" {
			driverType = agentCfg.Driver
		}
		if envDrv := os.Getenv("DUO_DRIVER"); envDrv != "" {
			driverType = envDrv
		}
		if parsed.driver != "" {
			driverType = parsed.driver
		}
		if id == protocol.Austin && parsed.austinDriver != "" {
			driverType = parsed.austinDriver
		}
		if id == protocol.Tony && parsed.tonyDriver != "" {
			driverType = parsed.tonyDriver
		}

		baseCmd := "agy"
		if driverType != "agy" {
			baseCmd = piCommand
		}
		if agentCfg.Command != "" {
			baseCmd = agentCfg.Command
		}
		if strings.HasPrefix(baseCmd, "agy") || strings.Contains(baseCmd, "/agy") {
			driverType = "agy"
		}

		model := agentCfg.Model
		if driverType == "agy" && strings.Contains(model, "/") {
			model = model[strings.LastIndex(model, "/")+1:]
		}
		if model == "" {
			model = models.DefaultModelForDriver(driverType)
		}
		agentModels[id] = model

		if agentCfg.Model != "" && !hasFlag(baseCmd, "--model") {
			modelArg := agentCfg.Model
			if driverType == "agy" && strings.Contains(modelArg, "/") {
				modelArg = modelArg[strings.LastIndex(modelArg, "/")+1:]
			}
			baseCmd = baseCmd + " --model " + modelArg
		}
		if agentCfg.Thinking != "" && !hasFlag(baseCmd, "--thinking") {
			baseCmd = baseCmd + " --thinking " + agentCfg.Thinking
		}
		agentCommands[id] = baseCmd
		agentDrivers[id] = driverType
	}

	var mode project.Mode
	var modeSource string
	if !parsed.resume {
		// A resumed session takes its mode from persisted state, so only a new
		// session resolves CLI > DUO_MODE > config > default here.
		mode, modeSource, err = resolveNewMode(parsed, fileCfg.Mode)
		if err != nil {
			return config{}, err
		}
	}

	testCommand := parsed.testCommand
	if testCommand == "" {
		testCommand = envString("DUO_TEST_COMMAND", fileCfg.TestCommand)
	}

	return config{
		listen:              envString("DUO_LISTEN", "127.0.0.1:0"),
		harnessEnabled:      harnessEnabled,
		harness:             harnessConfig,
		repository:          launch,
		launchDir:           launch,
		worktreeRoot:        strings.TrimSpace(os.Getenv("DUO_WORKTREE_ROOT")),
		session:             session,
		baseRef:             envString("DUO_BASE_REF", "HEAD"),
		piCommand:           piCommand,
		testCommand:         testCommand,
		agentCommands:       agentCommands,
		agentDrivers:        agentDrivers,
		agentModels:         agentModels,
		driverExplicit:      driverExplicit,
		agentDriverExplicit: agentDriverExplicit,
		resume:              parsed.resume,
		resumeSession:       parsed.sessionID,
		mode:                mode,
		modeSource:          modeSource,
		modeRaw:             parsed.mode,
		modeExplicit:        parsed.modeExplicit,
	}, nil
}

func loadConfigFile(path string) (configFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return configFile{}, nil
		}
		return configFile{}, err
	}
	var out configFile
	if err := json.Unmarshal(data, &out); err != nil {
		return configFile{}, fmt.Errorf("parse config file %s: %w", path, err)
	}
	return out, nil
}

func loadMergedConfigFile(launchDir string) (configFile, error) {
	var merged configFile

	// 1. Global config (~/.duo/config.json)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		globalPath := filepath.Join(home, ".duo", "config.json")
		if globalCfg, err := loadConfigFile(globalPath); err == nil {
			mergeConfig(&merged, globalCfg)
		} else if !os.IsNotExist(err) {
			return configFile{}, err
		}
	}

	// 2. Project config (.duo/config.json or .duo.json)
	candidates := []string{
		filepath.Join(launchDir, ".duo", "config.json"),
		filepath.Join(launchDir, ".duo.json"),
	}
	if root, err := workspace.FindRoot(context.Background(), launchDir); err == nil && root != "" && root != launchDir {
		candidates = append(candidates,
			filepath.Join(root, ".duo", "config.json"),
			filepath.Join(root, ".duo.json"),
		)
	}
	for _, p := range candidates {
		projectCfg, err := loadConfigFile(p)
		if err == nil && (projectCfg.Mode != "" || projectCfg.PiCommand != "" || projectCfg.Driver != "" || projectCfg.TestCommand != "" || len(projectCfg.Agents) > 0 || projectCfg.Harness != (harnessFile{})) {
			mergeConfig(&merged, projectCfg)
			break
		} else if err != nil && !os.IsNotExist(err) {
			return configFile{}, err
		}
	}

	return merged, nil
}

func mergeConfig(dst *configFile, src configFile) {
	if src.Mode != "" {
		dst.Mode = src.Mode
	}
	if src.Driver != "" {
		dst.Driver = src.Driver
	}
	if src.PiCommand != "" {
		dst.PiCommand = src.PiCommand
	}
	if src.TestCommand != "" {
		dst.TestCommand = src.TestCommand
	}
	if len(src.Agents) > 0 {
		if dst.Agents == nil {
			dst.Agents = make(map[string]agentFile)
		}
		for k, v := range src.Agents {
			existing := dst.Agents[k]
			if v.Command != "" {
				existing.Command = v.Command
			}
			if v.Driver != "" {
				existing.Driver = v.Driver
			}
			if v.Model != "" {
				existing.Model = v.Model
			}
			if v.Thinking != "" {
				existing.Thinking = v.Thinking
			}
			dst.Agents[k] = existing
		}
	}
	if src.Harness.Enabled != nil {
		dst.Harness.Enabled = src.Harness.Enabled
	}
	if src.Harness.IdleSeconds > 0 {
		dst.Harness.IdleSeconds = src.Harness.IdleSeconds
	}
	if src.Harness.StallSeconds > 0 {
		dst.Harness.StallSeconds = src.Harness.StallSeconds
	}
	if src.Harness.CooldownSeconds > 0 {
		dst.Harness.CooldownSeconds = src.Harness.CooldownSeconds
	}
}

func hasFlag(command, flag string) bool {
	for _, token := range strings.Fields(command) {
		if token == flag || strings.HasPrefix(token, flag+"=") {
			return true
		}
	}
	return false
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
