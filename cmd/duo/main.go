package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/coordinator"
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/recovery"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/transport"
	"github.com/atfa/duo/internal/tui"
	"github.com/atfa/duo/internal/workspace"
)

const version = "v0.4.4"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "version":
			fmt.Println("Duo " + version)
			return
		case "-h", "--help", "help":
			fmt.Println("Duo " + version)
			fmt.Println("Usage: duo [git-repository] [--resume [session-id]]")
			fmt.Println("       duo apply [session-id]")
			fmt.Println()
			fmt.Println("  duo                    start a new durable session")
			fmt.Println("  duo --resume           resume this repository's unfinished session")
			fmt.Println("  duo --resume <id>      resume one specific session (required if several are unfinished)")
			fmt.Println("  duo apply              deliver a pending final result to this repository")
			fmt.Println("  duo apply <id>         apply one specific session's final result")
			fmt.Println()
			fmt.Println("Run with no path from inside a Git repository: cd project && duo")
			return
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// `duo apply` is a small, self-contained transaction that never starts
	// agents, so it is handled before the interactive configuration.
	if len(os.Args) > 1 && os.Args[1] == "apply" {
		err := runApply(ctx, os.Args[2:])
		switch {
		case err == nil || ctx.Err() != nil:
		case errors.Is(err, errDeliveryPending):
			os.Exit(1)
		default:
			log.Fatal(err)
		}
		return
	}

	cfg, err := loadConfig(os.Args[1:])
	if err != nil {
		log.Fatal(err)
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

	piSessions  map[protocol.AgentID]string
	integration workspace.IntegrationResult
	delivery    sessionstore.Delivery
	resume      bool
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
	return recovery.Compose(recovery.ComposeInput{
		DuoVersion:  version,
		SessionID:   r.sessionID,
		RepoID:      r.repoID,
		Repository:  r.set.Repository,
		BaseBranch:  r.set.BaseBranch,
		BaseCommit:  r.set.BaseCommit,
		CreatedAt:   r.createdAt,
		Project:     r.state.Snapshot(),
		Worktrees:   r.set,
		PiSessions:  r.piSessions,
		Integration: integration,
		Delivery:    deliveryState,
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

	piSessions, err := piSessionIDs(nil)
	if err != nil {
		return err
	}

	r := &runtime{
		cfg:        cfg,
		repoID:     repoID,
		sessionID:  set.Session,
		createdAt:  time.Now().UTC(),
		state:      project.NewState(),
		ws:         ws,
		set:        set,
		store:      store,
		journal:    store.OpenEvents(),
		logger:     store.OpenLog(),
		piSessions: piSessions,
	}
	r.logger.Printf("starting Duo %s session %s repository=%s scope=%s", version, r.sessionID, root, set.ScopePath)
	r.journal.Record("session_start", map[string]any{
		"sessionId":  r.sessionID,
		"phase":      string(project.PhasePlan),
		"baseCommit": set.BaseCommit,
		"repository": set.Repository,
		"scope":      set.ScopePath,
		"duoVersion": version,
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
	agents.SetObserver(func(event agent.LifecycleEvent) {
		fields := map[string]any{"agent": string(event.Agent), "state": event.State.String()}
		if event.Err != nil {
			fields["error"] = event.Err.Error()
		}
		r.journal.Record(event.Kind, fields)
		switch event.Kind {
		case "agent_start_failed", "agent_restart":
			if event.Err != nil {
				r.logger.Printf("%s %s failed: %v", event.Agent, event.Kind, event.Err)
			}
		case "agent_exit":
			r.logger.Printf("%s exited (state=%s): %v", event.Agent, event.State, event.Err)
		}
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
		session := agent.NewSession(agent.Config{
			Agent:          agentID,
			Dir:            dir,
			RepositoryRoot: r.set.Repository,
			ScopePath:      r.set.ScopePath,
			Host:           host,
			Port:           port,
			Session:        r.sessionID,
			Token:          token,
			Command:        r.cfg.piCommand,
			PiSessionID:    r.piSessions[agentID],
		})
		agents.Add(session)
		r.logger.Printf("%s: worktree=%s cwd=%s piSession=%s command=%s", agentID, wt.Path, dir, session.PiSessionID(), session.EffectiveCommand())
	}

	if err := agents.StartAll(ctx); err != nil {
		return err
	}
	defer agents.StopAll()

	if r.cfg.harnessEnabled {
		monitor := harness.NewMonitor(r.cfg.harness, r.state, tracker, server, bus)
		go monitor.Run(ctx)
	}

	app := tui.New(coord, r.state, tracker, r.ws, server, agents, bus)
	if err := app.Run(ctx); err != nil && ctx.Err() == nil {
		log.Printf("Duo TUI: %v", err)
	}
	agents.StopAll()

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
