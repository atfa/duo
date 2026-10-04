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

	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
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
	// agentThinkings is the reasoning effort per agent, or empty for the driver's own default.
	agentThinkings map[protocol.AgentID]string

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

	// help is set by -h/--help/help, which prints the usage and exits without
	// starting or touching a session.
	help bool
}

// helpRequested reports whether arg asks for usage instead of an action. Every
// parser accepts the three spellings, so `duo <repo> --help` and
// `duo clean --help` print usage instead of failing with an unknown flag.
// defaultDriver is the driver an agent gets when nothing names one. It is a name and
// nothing more: Core branches on nothing here, and the driver it selects answers for
// its own binary, models and defaults from its manifest.
const defaultDriver = "pi"

func helpRequested(arg string) bool {
	switch strings.TrimSpace(arg) {
	case "-h", "--help", "help":
		return true
	}
	return false
}

// agentCommand is an operator override, or empty. With no override the driver's own
// manifest names the binary, so there is nothing here to fall back to.
func (c config) agentCommand(agent protocol.AgentID) string {
	return c.agentCommands[agent]
}

// agentDriver names the driver an agent should launch. The rule lives with the
// session, so the CLI cannot drift from the frontend-neutral answer.
func (c config) agentDriver(agent protocol.AgentID) string {
	return session.AgentDriver(c.agentDrivers, agent)
}

// agentModel is what the operator asked for, which may be nothing. The driver's
// manifest supplies the default, so an empty answer here means "the driver decides"
// rather than "Core has no idea".
func (c config) agentModel(agent protocol.AgentID) string {
	return c.agentModels[agent]
}

// agentThinking is the reasoning effort the operator asked for, if any. Which flag
// carries it is the driver's to spell.
func (c config) agentThinking(agent protocol.AgentID) string {
	return c.agentThinkings[agent]
}

// resolveDriver settles the three things only the driver can answer: which binary
// to run, which model to start on, and what the operator's requested effort means.
//
// It runs after the driver resolves rather than while the configuration file is
// parsed, and that ordering is the whole point rather than an inconvenience: a
// driver's manifest is the only thing that knows the spelling of its model
// references, its default, or the binary it prefers, and reading any of them
// earlier is what made Core carry a table of driver names.
func (c config) resolveDriver(agent protocol.AgentID, manifest *driver.Manifest) (command, model, effort string) {
	return session.ResolveDriver(c.agentCommands, c.agentModels, c.agentThinkings, agent, manifest)
}

// sessionOptions translates the CLI's parsed configuration into the
// frontend-neutral form a session starts from. This is the only place the two
// vocabularies meet, so a second frontend fills the same struct from its own
// settings instead of reimplementing any of it.
func (c config) sessionOptions() session.Options {
	return session.Options{
		LaunchDir:           c.launchDir,
		Repository:          c.repository,
		Resume:              c.resume,
		ResumeSession:       c.resumeSession,
		Session:             c.session,
		BaseRef:             c.baseRef,
		WorktreeRoot:        c.worktreeRoot,
		Mode:                c.mode,
		ModeSource:          c.modeSource,
		ModeRaw:             c.modeRaw,
		ModeExplicit:        c.modeExplicit,
		Listen:              c.listen,
		HarnessEnabled:      c.harnessEnabled,
		Harness:             c.harness,
		AgentCommands:       c.agentCommands,
		AgentDrivers:        c.agentDrivers,
		AgentModels:         c.agentModels,
		AgentThinkings:      c.agentThinkings,
		AgentDriverExplicit: c.agentDriverExplicit,
		TestCommand:         c.testCommand,
		RegisterDrivers:     registerDrivers,
	}
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
	help         bool
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
		case helpRequested(arg):
			// `duo <repo> --help` prints usage without launching a session.
			out.help = true
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
				return out, fmt.Errorf("%s requires a value (a driver name; `duo plugins` lists them)", arg)
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
			return out, fmt.Errorf("unknown Duo flag %q (usage: %s)", arg, commandUsage("duo"))
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
	return session.ResumeMode(cfg.modeRaw, cfg.modeExplicit, persisted, sessionID, os.Getenv("DUO_MODE"), warn)
}

func loadConfig(args []string) (config, error) {
	parsed, err := parseArgs(args)
	if err != nil {
		return config{}, err
	}
	if parsed.help {
		return config{help: true}, nil
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

	defaultPiCmd := defaultDriver
	if fileCfg.PiCommand != "" {
		defaultPiCmd = fileCfg.PiCommand
	}
	piCommand := envString("DUO_PI_COMMAND", defaultPiCmd)

	agentCommands := make(map[protocol.AgentID]string)
	agentDrivers := make(map[protocol.AgentID]string)
	agentModels := make(map[protocol.AgentID]string)
	agentThinkings := make(map[protocol.AgentID]string)
	agentDriverExplicit := make(map[protocol.AgentID]bool)
	// Naming a driver anywhere — flag, environment or file — makes it not the default
	// one, and piCommand is a command for the default driver only.
	driverExplicit := parsed.driver != "" || os.Getenv("DUO_DRIVER") != "" || fileCfg.Driver != ""

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

		driverType := defaultDriver
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

		// The command is an override, not a decision. Which binary a driver runs is
		// answered by its own manifest once it resolves, and an operator's command
		// reaches the plugin as the base command for it to interpret. piCommand is
		// still honoured, as the command for the default driver, which is why it
		// applies only when no driver was named explicitly.
		baseCmd := agentCfg.Command
		// piCommand is a command for the default driver, so it applies unless a
		// driver other than the default one was named. Naming the default driver
		// explicitly is not naming a different one, and must not silently drop an
		// operator's custom binary.
		if baseCmd == "" && (!isExplicit || driverType == defaultDriver) {
			baseCmd = piCommand
		}

		// requested is the model the user asked for, and is emptied when it
		// cannot be valid for the driver now selected. A model persisted for one
		// driver is meaningless to another: pi has never heard of an opencode
		// model id and aborts on one. The agent file records which driver the
		// model was chosen for, so a driver changed on the command line or in
		// config invalidates it and the new driver resolves its own default.
		// Without this, --tony-driver pi handed a persisted opencode model to pi
		// and the agent died at startup.
		requested := agentCfg.Model
		if recorded := strings.TrimSpace(agentCfg.Driver); recorded != "" && recorded != driverType {
			requested = ""
		}
		// What survives here is intent only. Spelling the reference for the driver
		// that receives it, substituting its default, and choosing the flag are all
		// answered from the manifest once the driver is resolved — which is also
		// why nothing is appended to the command here. The plugin builds the command
		// line and puts the model on it, so appending a second copy here is how a
		// model reached an agent that had already been told a different one.
		agentModels[id] = requested
		if agentCfg.Thinking != "" {
			agentThinkings[id] = agentCfg.Thinking
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
		agentThinkings:      agentThinkings,
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
