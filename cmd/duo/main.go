package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/clidoc"
	"github.com/atfa/duo/internal/coordinator"
	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/recovery"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/transport"
	"github.com/atfa/duo/internal/tui"
	"github.com/atfa/duo/internal/version"
	"github.com/atfa/duo/internal/workspace"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "version":
			fmt.Println("Duo " + version.Version)
			return
		case "-h", "--help", "help":
			printUsage()
			return
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Subcommands that never start agents are handled before the interactive configuration.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "apply":
			err := runApply(ctx, os.Args[2:])
			switch {
			case err == nil || ctx.Err() != nil:
			case errors.Is(err, errDeliveryPending):
				os.Exit(1)
			default:
				log.Fatal(err)
			}
			return
		case "sessions":
			if err := runSessions(ctx, os.Args[2:]); err != nil && ctx.Err() == nil {
				log.Fatal(err)
			}
			return
		case "logs":
			if err := runLogs(ctx, os.Args[2:]); err != nil && ctx.Err() == nil {
				log.Fatal(err)
			}
			return
		case "clean":
			if err := runClean(ctx, os.Args[2:]); err != nil && ctx.Err() == nil {
				log.Fatal(err)
			}
			return
		case "mcp-server", "mcp":
			if err := runMCPServer(ctx, os.Args[2:]); err != nil && ctx.Err() == nil {
				log.Fatal(err)
			}
			return
		case "plugins", "plugin":
			if err := runPlugins(os.Args[2:]); err != nil && ctx.Err() == nil {
				log.Fatal(err)
			}
			return
		}
	}

	cfg, err := loadConfig(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	if cfg.help {
		printUsage()
		return
	}
	if err := cfg.validate(); err != nil {
		log.Fatal(err)
	}

	if err := run(ctx, cfg); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, cfg config) error {
	root, err := workspace.FindRoot(ctx, cfg.launchDir)
	if err != nil {
		return err
	}
	scope, err := workspace.ScopePath(root, cfg.launchDir)
	if err != nil {
		return err
	}
	baseDir, err := sessionstore.DefaultBaseDir()
	if err != nil {
		return err
	}
	repoID := sessionstore.RepoID(root)

	if cfg.resume {
		return runResume(ctx, cfg, root, repoID, baseDir)
	}
	return runFresh(ctx, cfg, root, scope, repoID, baseDir)
}

// runtime holds everything a started session needs. Fresh and resumed sessions
// differ only in how it is populated.
type runtime struct {
	cfg       config
	repoID    string
	sessionID string
	createdAt time.Time

	state *project.State
	ws    *workspace.GitManager
	set   workspace.Set

	store   *sessionstore.Store
	journal *sessionstore.EventLog
	logger  *sessionstore.Logger

	agents *agent.Manager
	// driverState is each agent's opaque, plugin-owned resume blob, stored exactly
	// as the plugin returned it. Duo Core never parses it: it hands the same bytes
	// back on the next launch and lets the plugin interpret them.
	//
	// It is carried in the snapshot's legacy string map until the per-driver state
	// field lands; see composeSnapshot.
	driverState map[protocol.AgentID]string

	integration workspace.IntegrationResult
	delivery    sessionstore.Delivery
	tuiHistory  []sessionstore.TUIEntry
	resume      bool
	mode        project.Mode

	// notices collects diagnostics a driver reported while preparing a launch, for
	// drivers that prepare before the interface exists to show them.
	noticeMu sync.Mutex
	notices  []string
}

// collectNotice records a diagnostic a driver reported while preparing a launch.
//
// Drivers are built before the interface exists, so a notice produced during a
// launch is buffered here and handed over as soon as there is somewhere to show it.
// It is the driver's own words: Duo Core hard-codes no warning about any agent.
func (r *runtime) collectNotice(notices []string) {
	r.noticeMu.Lock()
	r.notices = append(r.notices, notices...)
	r.noticeMu.Unlock()
}

func (r *runtime) takeNotices() []string {
	r.noticeMu.Lock()
	defer r.noticeMu.Unlock()
	out := r.notices
	r.notices = nil
	return out
}

// pluginEnv is the environment a driver plugin process gets: exactly the one Duo
// Core gives an agent. An Agent Adapter therefore has everything it needs to reach
// Duo's bridge without Core inventing a second channel, and no driver-specific
// variable is set for a driver that does not own it.
func pluginEnv(r *runtime, id protocol.AgentID, host, port, token string) []string {
	return []string{
		"DUO_ACTIVE=1",
		"DUO_AGENT=" + string(id),
		"DUO_MODE=" + r.mode.String(),
		"DUO_HOST=" + host,
		"DUO_PORT=" + port,
		"DUO_SESSION=" + r.sessionID,
		"DUO_TOKEN=" + token,
		"DUO_REPOSITORY_ROOT=" + r.set.Repository,
		"DUO_SCOPE_PATH=" + r.set.ScopePath,
	}
}

// composeSnapshot builds the durable snapshot. When coord is nil the
// integration result is empty, which is exactly right for a fresh session.
func (r *runtime) composeSnapshot(coord *coordinator.Coordinator) sessionstore.Snapshot {
	integration := r.integration
	deliveryState := r.delivery
	if coord != nil {
		integration = coord.CurrentIntegration()
		deliveryState = coord.CurrentDelivery()
	}
	driverState := make(map[protocol.AgentID]string, len(r.driverState))
	for k, v := range r.driverState {
		driverState[k] = v
	}
	agentDrivers := make(map[protocol.AgentID]string)
	agentModels := make(map[protocol.AgentID]string)
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		if r.cfg.agentDrivers != nil && r.cfg.agentDrivers[id] != "" {
			agentDrivers[id] = r.cfg.agentDrivers[id]
		}
		if r.cfg.agentModels != nil && r.cfg.agentModels[id] != "" {
			agentModels[id] = r.cfg.agentModels[id]
		}
	}
	if r.agents != nil {
		for id, state := range r.agents.DriverStates() {
			driverState[id] = string(state)
		}
		for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
			if d, ok := r.agents.Driver(id); ok {
				if dt := d.DriverType(); dt != "" {
					agentDrivers[id] = dt
				}
				if m := d.Model(); m != "" {
					agentModels[id] = m
				}
			}
		}
	}
	if coord != nil {
		for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
			if m := coord.Model(id); m != "" {
				agentModels[id] = m
			}
		}
	}
	return recovery.Compose(recovery.ComposeInput{
		DuoVersion:   version.Version,
		SessionID:    r.sessionID,
		RepoID:       r.repoID,
		Repository:   r.set.Repository,
		BaseBranch:   r.set.BaseBranch,
		BaseCommit:   r.set.BaseCommit,
		CreatedAt:    r.createdAt,
		Project:      r.state.Snapshot(),
		Worktrees:    r.set,
		PiSessions:   driverState,
		AgentDrivers: agentDrivers,
		AgentModels:  agentModels,
		Integration:  integration,
		Delivery:     deliveryState,
	})
}

// runFresh creates new worktrees, a new session store and a new checkpoint.
func runFresh(ctx context.Context, cfg config, root, scope, repoID, baseDir string) error {
	ws := workspace.NewGitManager(workspace.GitConfig{
		Repository: root,
		ScopePath:  scope,
		Root:       cfg.worktreeRoot,
		Session:    cfg.session,
		BaseRef:    cfg.baseRef,
	})
	set, err := ws.Prepare(ctx)
	if err != nil {
		return fmt.Errorf("prepare Duo worktrees: %w", err)
	}
	_ = workspace.EnsureGitIgnore(root)
	_ = workspace.SaveProjectConfig(root, cfg.agentDriver(protocol.Austin), cfg.agentDrivers, cfg.agentModels)

	store, err := sessionstore.New(baseDir, repoID, set.Session)
	if err != nil {
		return err
	}
	// Plain `duo` always starts a new session; it must never silently overwrite
	// an earlier unfinished one.
	if store.Exists() {
		return fmt.Errorf(
			"Duo session %q already exists for this repository; run `duo --resume %s` to continue it",
			set.Session, set.Session,
		)
	}

	lock, err := store.Lock()
	if err != nil {
		return err
	}
	defer lock.Release()

	r := &runtime{
		cfg:         cfg,
		repoID:      repoID,
		sessionID:   set.Session,
		createdAt:   time.Now().UTC(),
		state:       project.NewStateFor(cfg.mode),
		ws:          ws,
		set:         set,
		store:       store,
		journal:     store.OpenEvents(),
		logger:      store.OpenLog(),
		driverState: map[protocol.AgentID]string{},
		mode:        cfg.mode,
	}
	r.logger.Printf("starting Duo %s session %s mode=%s (source=%s) repository=%s scope=%s", version.Version, r.sessionID, r.mode, r.cfg.modeSource, root, set.ScopePath)
	r.journal.Record("session_start", map[string]any{
		"sessionId":  r.sessionID,
		"mode":       r.mode.String(),
		"phase":      string(r.state.Snapshot().Phase),
		"baseCommit": set.BaseCommit,
		"repository": set.Repository,
		"scope":      set.ScopePath,
		"duoVersion": version.Version,
	})

	// The initial checkpoint must exist before any agent starts, so a crash
	// during startup is still resumable.
	if err := r.store.Save(r.composeSnapshot(nil)); err != nil {
		return err
	}
	return r.serve(ctx)
}

// serve starts the bridge, both Pi agents, the harness and the TUI. It is shared
// by fresh and resumed sessions so crash recovery cannot diverge from normal
// startup.
func (r *runtime) serve(ctx context.Context) error {
	token, err := sessionToken()
	if err != nil {
		return fmt.Errorf("create Duo session token: %w", err)
	}
	r.logger.AddSecret(token)

	server := transport.NewServer(r.cfg.listen, r.sessionID, token)
	tracker := harness.NewTracker()
	bus := events.NewBus()
	coord := coordinator.New(server, r.state, tracker, r.ws, bus)

	// Duo Core's whole knowledge of which coding agents it ships. Each is a Driver
	// Plugin running in-process until its milestone moves it out.
	registerDrivers(func(id protocol.AgentID, msg protocol.Message) {
		coord.RecordActivity(id, msg)
	})
	if r.cfg.testCommand != "" {
		coord.SetTestCommand(r.cfg.testCommand)
	}
	coord.SetIntegration(r.integration)
	coord.SetDelivery(r.delivery)
	coord.EnableDurability(coordinator.Durability{
		Store:  r.store,
		Events: r.journal,
		Log:    r.logger,
		Compose: func(project.Snapshot) sessionstore.Snapshot {
			return r.composeSnapshot(coord)
		},
	})
	if r.resume {
		coord.EnableResumeWake()
	}
	server.SetHandler(coord)

	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ListenAndServe(ctx) }()
	select {
	case <-server.Ready():
	case err := <-serverErr:
		if err != nil {
			return err
		}
		return nil
	case <-ctx.Done():
		return nil
	}

	host, port := bridgeAddress(server.Addr())
	agents := agent.NewManager()
	r.agents = agents
	agents.SetObserver(func(event agent.LifecycleEvent) {
		fields := map[string]any{"agent": string(event.Agent), "state": event.State.String()}
		if event.Err != nil {
			fields["error"] = event.Err.Error()
		}
		r.journal.Record(event.Kind, fields)
		switch event.Kind {
		case "agent_start":
			// A driver whose bridge cannot announce itself is announced here
			// instead, so every agent produces the same "connected" signal at the
			// same moment. The answer is the driver's declared capability, not its
			// name.
			if d, ok := agents.Driver(event.Agent); ok && !d.Capabilities().SelfReports {
				bus.Emit(events.Event{Time: time.Now(), Kind: events.KindSystem, Agent: event.Agent, Text: fmt.Sprintf("%s connected", event.Agent)})
			}
		case "agent_start_failed", "agent_restart":
			if event.Err != nil {
				r.logger.Printf("%s %s failed: %v", event.Agent, event.Kind, event.Err)
			}
		case "agent_exit":
			r.logger.Printf("%s exited (state=%s): %v", event.Agent, event.State, event.Err)
			// A driver that refuses to run says why once, on its own output, and
			// "exit status 1" alone leaves the operator guessing. Write the reason
			// to the session log and repeat it in the interface, so the cause is
			// where the failure is reported.
			if reason := strings.TrimSpace(event.Output); reason != "" {
				r.logger.Printf("%s output before exit:\n%s", event.Agent, indentBlock(reason))
				// Addressed to Duo's own pane, which is the timeline: the failed
				// agent's pane already shows the raw stream, and repeating it
				// there would bury the reason under a second "ERROR:".
				bus.Emit(events.Event{
					Time:  time.Now(),
					Kind:  events.KindError,
					Agent: protocol.Duo,
					Text:  fmt.Sprintf("%s exited (%s):\n%s", event.Agent, exitReason(event), reason),
				})
			}
		}
		bus.Emit(events.Event{Time: time.Now(), Kind: events.KindActivity, Agent: event.Agent})
	})

	for _, agentID := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		wt, ok := r.set.For(agentID)
		dir, hasDir, err := r.set.AgentDir(agentID)
		if err != nil {
			return err
		}
		if !ok || wt.Path == "" || !hasDir {
			return fmt.Errorf("session has no worktree for %s", agentID)
		}
		driverName := r.cfg.agentDriver(agentID)
		modelName := r.cfg.agentModel(agentID)

		// One lookup for every agent: a driver Duo ships in-process, or a plugin
		// executable found on disk. The switch that used to be here is the thing
		// this whole refactor removes — Core asked nothing of a driver and guessed
		// from its name instead.
		plugin, err := driver.Resolve(ctx, driverName, pluginEnv(r, agentID, host, port, token), func(err error) {
			r.logger.Printf("%s: driver plugin: %v", agentID, err)
			bus.Emit(events.Event{Time: time.Now(), Kind: events.KindError, Agent: protocol.Duo, Text: fmt.Sprintf("%s driver plugin: %v", agentID, err)})
		})
		if err != nil {
			return fmt.Errorf("%s: %w", agentID, err)
		}
		if probe, err := plugin.Probe(ctx); err == nil && !probe.Available {
			// Not fatal: the agent CLI may be a wrapper that only exists inside the
			// agent's own worktree, and the failure is reported in context if it
			// turns out to matter. The reason is always shown.
			r.logger.Printf("%s: driver %s unavailable: %s", agentID, driverName, probe.Reason)
		}

		session, err := agent.NewSession(ctx, agent.Config{
			Agent:          agentID,
			Mode:           r.mode.String(),
			Dir:            dir,
			RepositoryRoot: r.set.Repository,
			ScopePath:      r.set.ScopePath,
			Host:           host,
			Port:           port,
			Session:        r.sessionID,
			Token:          token,
			Plugin:         plugin,
			PluginState:    json.RawMessage(r.driverState[agentID]),
			BaseCommand:    r.cfg.agentCommand(agentID),
			Model:          modelName,
			Notices:        r.collectNotice,
		})
		if err != nil {
			return err
		}
		agents.Add(session)
		coord.SetCurrentModel(agentID, modelName)
		r.logger.Printf("%s: driver=%s model=%s worktree=%s cwd=%s sessionID=%s command=%s", agentID, session.DriverType(), modelName, wt.Path, dir, session.SessionID(), session.EffectiveCommand())
	}
	coord.SetAgents(agents)

	_ = workspace.EnsureGitIgnore(r.set.Repository)
	_ = workspace.SaveProjectConfig(r.set.Repository, r.cfg.agentDriver(protocol.Austin), r.cfg.agentDrivers, r.cfg.agentModels)

	// Transcripts live in the main repository, never in an agent worktree, so
	// they survive `duo clean` and stay where the user works.
	transcripts, err := sessionstore.NewLogWriter(r.set.Repository, sessionstore.Transcript{
		SessionID:  r.sessionID,
		Mode:       string(r.state.Snapshot().Mode),
		Repository: r.set.Repository,
		Branch:     r.set.BaseBranch,
		BaseCommit: r.set.BaseCommit,
	})
	if err != nil {
		r.logger.Printf("session log: %v", err)
	}
	if transcripts != nil {
		defer transcripts.Close()
	}

	if err := agents.StartAll(ctx); err != nil {
		return err
	}
	// Checkpoint once the agents are up. An agent's identity is only known after its
	// driver has prepared the launch, so the checkpoint written before startup cannot
	// contain one — and this is the point at which the first real conversation exists.
	// Without it, a session whose agents never reached the bridge would be recorded
	// with no identity at all.
	if err := r.store.Save(r.composeSnapshot(coord)); err != nil {
		r.logger.Printf("checkpoint after startup: %v", err)
	}
	defer agents.StopAll()

	if r.cfg.harnessEnabled {
		monitor := harness.NewMonitor(r.cfg.harness, r.state, tracker, server, bus)
		go monitor.Run(ctx)
	}

	app := tui.New(coord, r.state, tracker, r.ws, server, agents, bus, version.Version, r.tuiHistory, r.journal, transcripts)
	// Drivers are built before the interface exists, so any diagnostic one of them
	// reported is shown now, in its own words.
	app.Notices(r.takeNotices())
	if err := app.Run(ctx); err != nil && ctx.Err() == nil {
		log.Printf("Duo TUI: %v", err)
	}
	agents.StopAll()
	// Release the drivers too: a plugin may own background work, such as a
	// transcript observer, that must not outlive the session.
	defer agents.CloseAll()

	phase := r.state.Snapshot().Phase
	r.journal.Record("session_stop", map[string]any{"phase": string(phase)})
	r.logger.Printf("session %s stopped (phase %s)", r.sessionID, phase)

	what := "started"
	if r.resume {
		what = "resumed"
	}
	fmt.Printf("\nDuo stopped. Session %s was preserved; resume it with `duo --resume %s`.\nWorktrees preserved in %s\n", what, r.sessionID, r.set.Root)
	return nil
}

// exitReason describes a failed process in one clause, for a headline above the
// driver's own output.
func exitReason(event agent.LifecycleEvent) string {
	if event.Err != nil {
		return event.Err.Error()
	}
	return event.State.String()
}

// indentBlock keeps a multi-line driver message readable in the session log
// without letting its later lines look like unrelated entries.
func indentBlock(text string) string {
	return "  " + strings.ReplaceAll(strings.TrimRight(text, "\n"), "\n", "\n  ")
}

func sessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func bridgeAddress(listen string) (string, string) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "127.0.0.1", "8765"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return host, port
}

// printUsage is `duo --help`. The command lines come from internal/clidoc, the
// same table the in-app Help panel renders, so the two cannot disagree.
func printUsage() {
	fmt.Println("Duo " + version.Version)
	fmt.Println()
	fmt.Println("Usage:")
	for _, line := range clidoc.SummaryLines() {
		fmt.Println(line)
	}
	fmt.Println()
	fmt.Println("Starting a session:")
	fmt.Println("  duo                    start a new Fast session (Austin drives, Tony verifies)")
	fmt.Println("  duo --mode goal        start a new Goal session (shared plan + dual sign-off)")
	fmt.Println("  duo --test-cmd <cmd>   run automated test command before accepting verification")
	fmt.Println("  duo --agent pi|agy|opencode")
	fmt.Println("                       select the driver for both agents")
	fmt.Println("  duo --austin-driver <driver>")
	fmt.Println("  duo --tony-driver <driver>")
	fmt.Println("                       select one agent's driver, overriding --agent")
	fmt.Println("  duo resume             resume this repository's unfinished session (same as -r / --resume)")
	fmt.Println("  duo --resume           resume this repository's unfinished session")
	fmt.Println("  duo --resume <id>      resume one specific session (required if several are unfinished)")
	fmt.Println()
	fmt.Println("Aliases:")
	fmt.Println("  --driver = --agent; --austin-agent / --tony-agent = --austin-driver / --tony-driver;")
	fmt.Println("  the bare word resume = --resume; duo plugin = duo plugins; duo mcp = duo mcp-server.")
	fmt.Println()
	fmt.Println("Environment:")
	fmt.Println("  DUO_MODE               default mode for new sessions: fast or goal (an explicit --mode wins)")
	fmt.Println("  DUO_TEST_COMMAND       automated test command run before accepting verification")
	fmt.Println("  DUO_REPO               repository or subdirectory to launch against (a path argument wins)")
	fmt.Println("  DUO_SESSION            session id to use (default: timestamp + random hex)")
	fmt.Println("  DUO_WORKTREE_ROOT      where the Austin/Tony worktrees are created")
	fmt.Println("  DUO_BASE_REF           ref the worktrees are branched from (default: HEAD)")
	fmt.Println("  DUO_PI_COMMAND         command used to launch a Pi agent (default: pi)")
	fmt.Println("  DUO_DRIVER             default driver for both agents: pi, agy or opencode")
	fmt.Println("  DUO_LISTEN             bridge listen address (default: 127.0.0.1:0)")
	fmt.Println("  DUO_HARNESS            enable the idle/stall watchdog (default: true)")
	fmt.Println("  DUO_HARNESS_IDLE_SECONDS / _STALL_SECONDS / _COOLDOWN_SECONDS")
	fmt.Println("                         watchdog thresholds")
	fmt.Println("  DUO_HARNESS_RESUME_GRACE_SECONDS")
	fmt.Println("                         extra grace after --resume (default: 45)")
	fmt.Println("  DUO_HISTORY_FILE       composer task history file (default: ~/.duo/history)")
	fmt.Println("  DUO_AGENT, DUO_SESSION, DUO_TOKEN, DUO_HOST, DUO_PORT")
	fmt.Println("                         bridge connection for `duo mcp-server` (see duo mcp-server --help)")
	fmt.Println()
	fmt.Println("Mode is fixed for a session's lifetime. DUO_MODE sets the default for new sessions;")
	fmt.Println("an explicit --mode wins, and --resume always uses the session's persisted mode.")
	fmt.Println()
	fmt.Println("Every command prints its own usage with --help, for example: duo apply --help")
	fmt.Println("Run with no path from inside a Git repository: cd project && duo")
}

// commandUsage is the signature clidoc records for one command, for the
// "unknown flag" messages of the individual parsers.
func commandUsage(name string) string {
	if command, ok := clidoc.Lookup(name); ok {
		return command.Signature
	}
	return "duo " + name
}

func runPlugins(args []string) error {
	for _, arg := range args {
		switch strings.TrimSpace(arg) {
		case "":
		case "-h", "--help", "help":
			printUsage()
			return nil
		default:
			if strings.HasPrefix(arg, "-") {
				return fmt.Errorf("unknown flag %q (usage: %s)", arg, commandUsage("plugins"))
			}
			return fmt.Errorf("unexpected extra argument %q (usage: %s)", arg, commandUsage("plugins"))
		}
	}
	printPlugins()
	return nil
}

func printPlugins() {
	// Registration is idempotent, so listing plugins populates the same table the
	// session uses. No observer is needed: nothing is launched here.
	registerDrivers(nil)
	entries := driver.Discover()
	if len(entries) == 0 {
		fmt.Println("No driver plugins found.")
	} else {
		fmt.Printf("%d driver plugin(s) available:\n", len(entries))
		for _, entry := range entries {
			origin := "external"
			if entry.Shipped {
				origin = "shipped"
			}
			fmt.Printf("  %-12s %-8s %s\n", entry.Name, "["+origin+"]", entry.Path)
		}
	}
	fmt.Println()
	fmt.Println("Duo selects a driver with --agent <name>, or per agent with")
	fmt.Println("--austin-driver / --tony-driver. Run `duo plugins` again after")
	fmt.Println("installing one to confirm it was found.")
}
