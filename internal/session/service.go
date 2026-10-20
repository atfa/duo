package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/coordinator"
	"github.com/atfa/duo/internal/driver"
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/models"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/transport"
	"github.com/atfa/duo/internal/workspace"
)

var (
	// ErrClosed is returned once a session has been closed.
	ErrClosed = errors.New("duo session is closed")
	// ErrStarted is returned when Start is called on a session that already ran.
	ErrStarted = errors.New("duo session is already started")
)

// errNotRunning is returned by operations that need a live session.
var errNotRunning = errors.New("duo session is not running")

// Result describes how a session ended or why it never needed a frontend loop.
type Result struct {
	SessionID string
	Phase     project.Phase

	// Finished reports that Start ended the session before any frontend loop
	// existed: either the final artifact is already in the user's repository, or
	// delivery is safely pending and only the human can unblock it. A frontend
	// must not start a loop when this is set.
	Finished bool
	// Discarded reports that the session owned nothing and left nothing: it was
	// ended before a task was ever submitted, so its worktrees, record and
	// transcripts are gone and there is nothing to resume. A frontend must not
	// tell the human to resume a session that no longer exists.
	Discarded bool

	// Outcome describes a finished delivery for the frontend to report. Its
	// wording is the frontend's business: Core returns the facts.
	Outcome DeliveryOutcome
}

// Service is one Duo session, headless.
//
// It owns the whole composition root — the repository worktrees, the durable
// session store, the workflow state, the agent bridge, the driver processes, the
// harness and the event bus — and exposes exactly one seam through which a
// frontend drives it:
//
//   - View reports the current state as a value, so drawing a session cannot
//     change it.
//   - Subscribe streams events, starting with a full View so a frontend that
//     attaches late is never blind.
//   - Do performs the verbs a human performs.
//   - Attach, Detach, Write and Resize move an agent's process bytes, which are
//     inherently not serializable.
//
// Everything else about a session is private to this package, so interfaces are
// projections of the same program rather than separate coordinators.
type Service struct {
	opts Options

	mu      sync.RWMutex
	started bool
	closed  bool

	// restart is the pending request to continue on a new Duo binary.
	restart RestartRequest

	ctx    context.Context
	cancel context.CancelFunc

	root, scope, repoID, baseDir  string
	sessionID                     string
	bridgeHost, bridgePort, token string
	createdAt                     time.Time

	state *project.State
	ws    *workspace.GitManager
	set   workspace.Set

	store   *sessionstore.Store
	journal *sessionstore.EventLog
	logger  *sessionstore.Logger
	lock    *sessionstore.Lock

	server      *transport.Server
	coord       *coordinator.Coordinator
	tracker     *harness.Tracker
	bus         *events.Bus
	agents      *agent.Manager
	monitor     *harness.Monitor
	transcripts *sessionstore.LogWriter

	driverState map[protocol.AgentID]sessionstore.DriverState

	integration workspace.IntegrationResult
	delivery    sessionstore.Delivery

	history []sessionstore.TUIEntry
	resume  bool
	mode    project.Mode

	noticeMu sync.Mutex
	notices  []string
}

// New creates a session service. Nothing is started and no file is touched until
// Start is called, so a frontend may construct one before it knows it can run.
func New(opts Options) *Service {
	if opts.RegisterDrivers != nil {
		opts.RegisterDrivers()
	}
	return &Service{opts: opts, bus: events.NewBus()}
}

// Start brings the session up.
//
// It resolves the repository, creates or reconciles the worktrees, takes the
// session lock, starts the agent bridge and both agents, and returns once the
// session is live. When the session turns out to be finished already — delivery
// applied, or pending on a human — it returns a Result with Finished set and
// starts nothing.
//
// Every resource Start acquires is released by Close, including on the error
// paths: the session lock is process-lifetime and exclusive, so leaking it on one
// failure would make the next resume of the same session fail.
func (s *Service) Start(ctx context.Context) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return Result{}, ErrClosed
	}
	if s.started {
		return Result{}, ErrStarted
	}
	s.started = true

	res, err := s.start(ctx)
	if err != nil {
		s.closeLocked(ctx)
		return Result{}, err
	}
	return res, nil
}

// Close stops the agents and releases everything Start acquired. It is idempotent
// and safe to call from any frontend, including after a failed Start.
//
// Close is serialized against Do: a command in flight finishes before the final
// phase is read, so the recorded session_stop cannot race a workflow mutation.
func (s *Service) Close() Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A background context, not s.ctx: by the time a session is torn down its own
	// context is already cancelled, and the Git calls that decide whether the
	// worktrees may be removed have to be able to run.
	return s.closeLocked(context.Background())
}

func (s *Service) start(ctx context.Context) (Result, error) {
	s.ctx, s.cancel = context.WithCancel(ctx)

	root, err := workspace.FindRoot(ctx, s.opts.LaunchDir)
	if err != nil {
		return Result{}, err
	}
	scope, err := workspace.ScopePath(root, s.opts.LaunchDir)
	if err != nil {
		return Result{}, err
	}
	baseDir := s.opts.BaseDir
	if baseDir == "" {
		baseDir, err = sessionstore.DefaultBaseDir()
		if err != nil {
			return Result{}, err
		}
	}
	s.root, s.scope, s.baseDir = root, scope, baseDir
	s.repoID = sessionstore.RepoID(root)

	if s.opts.Resume {
		return s.startResume(ctx)
	}
	return s.startFresh(ctx)
}

// startFresh creates new worktrees, a new session store and a new checkpoint.
func (s *Service) startFresh(ctx context.Context) (Result, error) {
	ws := workspace.NewGitManager(workspace.GitConfig{
		Repository: s.root,
		ScopePath:  s.scope,
		Root:       s.opts.WorktreeRoot,
		Session:    s.opts.Session,
		BaseRef:    s.opts.BaseRef,
	})
	set, err := ws.Prepare(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("prepare Duo worktrees: %w", err)
	}
	_ = workspace.EnsureGitIgnore(s.root)
	_ = workspace.SaveProjectConfig(s.root, s.opts.AgentDriver(protocol.Austin), s.opts.AgentDrivers, s.opts.AgentModels)

	store, err := sessionstore.New(s.baseDir, s.repoID, set.Session)
	if err != nil {
		return Result{}, err
	}
	// Plain `duo` always starts a new session; it must never silently overwrite
	// an earlier unfinished one.
	if store.Exists() {
		return Result{}, fmt.Errorf(
			"Duo session %q already exists for this repository; run `duo resume %s` to continue it",
			set.Session, set.Session,
		)
	}

	lock, err := store.Lock()
	if err != nil {
		return Result{}, err
	}

	s.ws, s.set, s.store, s.lock = ws, set, store, lock
	s.sessionID = set.Session
	s.createdAt = time.Now().UTC()
	s.state = project.NewStateFor(s.opts.Mode)
	s.journal = store.OpenEvents()
	s.logger = store.OpenLog()
	s.driverState = map[protocol.AgentID]sessionstore.DriverState{}
	s.mode = s.opts.Mode

	s.logger.Printf("starting Duo %s session %s mode=%s (source=%s) repository=%s scope=%s", versionString(), s.sessionID, s.mode, s.opts.ModeSource, s.root, set.ScopePath)
	s.journal.Record("session_start", map[string]any{
		"sessionId":  s.sessionID,
		"mode":       s.mode.String(),
		"phase":      string(s.state.Snapshot().Phase),
		"baseCommit": set.BaseCommit,
		"repository": set.Repository,
		"scope":      set.ScopePath,
		"duoVersion": versionString(),
	})

	// The initial checkpoint must exist before any agent starts, so a crash
	// during startup is still resumable.
	if err := s.store.Save(s.composeSnapshot(nil)); err != nil {
		return Result{}, err
	}
	return s.launch(ctx)
}

// launch starts the bridge, both agents and the harness, and returns once the
// session is live. It is shared by fresh and resumed sessions so crash recovery
// cannot diverge from normal startup.
func (s *Service) launch(ctx context.Context) (Result, error) {
	token, err := sessionToken()
	if err != nil {
		return Result{}, fmt.Errorf("create Duo session token: %w", err)
	}
	s.logger.AddSecret(token)

	server := transport.NewServer(s.opts.Listen, s.sessionID, token)
	tracker := harness.NewTracker()
	coord := coordinator.New(server, s.state, tracker, s.ws, s.bus)
	coord.SetLanguage(s.opts.Language)
	s.server, s.tracker, s.coord = server, tracker, coord

	// The driver plugins were registered once, by New: a session must not import a
	// plugin package, or adding an agent would stop being a one-line change in the CLI.
	if s.opts.TestCommand != "" {
		coord.SetTestCommand(s.opts.TestCommand)
	}
	coord.SetIntegration(s.integration)
	coord.SetDelivery(s.delivery)
	coord.EnableDurability(coordinator.Durability{
		Store:  s.store,
		Events: s.journal,
		Log:    s.logger,
		Compose: func(project.Snapshot) sessionstore.Snapshot {
			return s.composeSnapshot(coord)
		},
	})
	if s.resume {
		coord.EnableResumeWake()
	}
	server.SetHandler(coord)

	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ListenAndServe(s.ctx) }()
	select {
	case <-server.Ready():
	case err := <-serverErr:
		if err != nil {
			return Result{}, err
		}
		return Result{SessionID: s.sessionID, Finished: true}, nil
	case <-s.ctx.Done():
		return Result{SessionID: s.sessionID, Finished: true}, nil
	}

	host, port := bridgeAddress(server.Addr())
	s.bridgeHost, s.bridgePort, s.token = host, port, token
	agents := agent.NewManager()
	s.agents = agents
	agents.SetObserver(s.observeLifecycle)

	for _, agentID := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		wt, ok := s.set.For(agentID)
		dir, hasDir, err := s.set.AgentDir(agentID)
		if err != nil {
			return Result{}, err
		}
		if !ok || wt.Path == "" || !hasDir {
			return Result{}, fmt.Errorf("session has no worktree for %s", agentID)
		}
		driverName := s.opts.AgentDriver(agentID)

		// One lookup for every agent: a driver Duo ships in-process, or a plugin
		// executable found on disk. Core asks nothing of a driver and guesses
		// nothing from its name.
		plugin, err := driverResolve(s.ctx, driverName, s.pluginEnv(agentID, host, port, token), func(err error) {
			s.logger.Printf("%s: driver plugin: %v", agentID, err)
			s.bus.Emit(events.Event{Time: time.Now(), Kind: events.KindError, Agent: protocol.Duo, Text: fmt.Sprintf("%s driver plugin: %v", agentID, err)})
		})
		if err != nil {
			return Result{}, fmt.Errorf("%s: %w", agentID, err)
		}
		if probe, err := plugin.Probe(ctx); err == nil && !probe.Available {
			// Not fatal: the agent CLI may be a wrapper that only exists inside the
			// agent's own worktree, and the failure is reported in context if it
			// turns out to matter. The reason is always shown.
			s.logger.Printf("%s: driver %s unavailable: %s", agentID, driverName, probe.Reason)
		}

		// The driver answers for itself now that it is resolved: the binary to run
		// and the model to start on come from its manifest, not from Core's guess at
		// what a driver of this name wants.
		baseCommand, modelName, effort := s.opts.ResolveDriver(agentID, manifestOf(ctx, plugin))

		session, err := agent.NewSession(ctx, agent.Config{
			Agent:          agentID,
			Mode:           s.mode.String(),
			Language:       s.opts.Language,
			Dir:            dir,
			RepositoryRoot: s.set.Repository,
			ScopePath:      s.set.ScopePath,
			Host:           host,
			Port:           port,
			Session:        s.sessionID,
			ResumeWake:     s.resume,
			Token:          token,
			Plugin:         plugin,
			PluginState:    s.driverState[agentID].State,
			BaseCommand:    baseCommand,
			Model:          modelName,
			Thinking:       effort,
			Notices:        s.collectNotice,
		})
		if err != nil {
			return Result{}, err
		}
		agents.Add(session)
		coord.SetCurrentModel(agentID, modelName)
		s.logger.Printf("%s: driver=%s model=%s worktree=%s cwd=%s sessionID=%s command=%s", agentID, session.DriverType(), modelName, wt.Path, dir, session.SessionID(), session.EffectiveCommand())
	}
	coord.SetAgents(agents)

	_ = workspace.EnsureGitIgnore(s.set.Repository)
	_ = workspace.SaveProjectConfig(s.set.Repository, s.opts.AgentDriver(protocol.Austin), s.opts.AgentDrivers, s.opts.AgentModels)

	// Transcripts live in the main repository, never in an agent worktree, so
	// they survive `duo clean` and stay where the user works.
	transcripts, err := sessionstore.NewLogWriter(s.set.Repository, sessionstore.Transcript{
		SessionID:  s.sessionID,
		Mode:       string(s.state.Snapshot().Mode),
		Repository: s.set.Repository,
		Branch:     s.set.BaseBranch,
		BaseCommit: s.set.BaseCommit,
	})
	if err != nil {
		s.logger.Printf("session log: %v", err)
	}
	s.transcripts = transcripts

	if err := agents.StartAll(ctx); err != nil {
		return Result{}, err
	}
	// Checkpoint once the agents are up. An agent's identity is only known after its
	// driver has prepared the launch, so the checkpoint written before startup cannot
	// contain one — and this is the point at which the first real conversation exists.
	// Without it, a session whose agents never reached the bridge would be recorded
	// with no identity at all.
	if err := s.store.Save(s.composeSnapshot(coord)); err != nil {
		s.logger.Printf("checkpoint after startup: %v", err)
	}

	if s.opts.HarnessEnabled {
		harnessConfig := s.opts.Harness
		harnessConfig.Language = s.opts.Language
		// The coordinator, not the socket server: it owns the decision of whether a
		// message goes over a bridge or into the agent's terminal, and a driver with
		// a lazy bridge has no client for the server to be asked about.
		s.monitor = harness.NewMonitor(harnessConfig, s.state, s.tracker, s.coord, s.bus)
		go s.monitor.Run(s.ctx)
	}

	return Result{SessionID: s.sessionID, Phase: s.state.Snapshot().Phase}, nil
}

// observeLifecycle records an agent process transition and fans out the events the
// interface reads. It is the session's, not a frontend's: the events are the same
// whatever is drawing them.
func (s *Service) observeLifecycle(event agent.LifecycleEvent) {
	fields := map[string]any{"agent": string(event.Agent), "state": event.State.String()}
	if event.Err != nil {
		fields["error"] = event.Err.Error()
	}
	s.journal.Record(event.Kind, fields)
	switch event.Kind {
	case "agent_start":
		// A driver whose bridge cannot announce itself is announced here instead,
		// so every agent produces the same "connected" signal at the same moment.
		// The answer is the driver's declared capability, not its name.
		if d, ok := s.agents.Driver(event.Agent); ok && !d.Capabilities().SelfReports {
			s.bus.Emit(events.Event{Time: time.Now(), Kind: events.KindSystem, Agent: event.Agent, Text: fmt.Sprintf("%s connected", event.Agent)})
		}
	case "agent_start_failed", "agent_restart":
		if event.Err != nil {
			s.logger.Printf("%s %s failed: %v", event.Agent, event.Kind, event.Err)
		}
	case "agent_exit":
		s.logger.Printf("%s exited (state=%s): %v", event.Agent, event.State, event.Err)
		if event.Requested {
			// Duo asked this process to stop, so this is a shutdown rather than a
			// failure, and reporting it as one would fill every session log with an
			// error the operator caused.
			break
		}
		// A driver that refuses to run says why once, on its own output, and
		// "exit status 1" alone leaves the operator guessing. Write the reason to
		// the session log and repeat it in the interface, so the cause is where the
		// failure is reported.
		reason := strings.TrimSpace(event.Output)
		if reason == "" {
			// Some agents report a fatal problem only inside a full-screen interface
			// Duo cannot read back, and some give up without saying anything at all.
			// Staying silent then would leave a session that has quietly lost an
			// agent still waiting on that agent, which is exactly the failure this
			// line exists to make visible.
			reason = "the agent process ended on its own without reporting a reason"
		}
		s.logger.Printf("%s output before exit:\n%s", event.Agent, indentBlock(reason))
		// Addressed to Duo's own pane, which is the timeline: the failed agent's
		// pane already shows the raw stream, and repeating it there would bury the
		// reason under a second "ERROR:".
		s.bus.Emit(events.Event{
			Time:  time.Now(),
			Kind:  events.KindError,
			Agent: protocol.Duo,
			Text:  fmt.Sprintf("%s:\n%s", exitHeadline(event), reason),
		})
	}
	s.bus.Emit(events.Event{Time: time.Now(), Kind: events.KindActivity, Agent: event.Agent})
}

// closeLocked releases everything Start acquired. It is idempotent; the caller
// must hold s.mu.
func (s *Service) closeLocked(ctx context.Context) Result {
	res := Result{SessionID: s.sessionID}
	if s.state != nil {
		res.Phase = s.state.Snapshot().Phase
	}
	if s.closed {
		return res
	}
	s.closed = true

	if s.agents != nil {
		s.agents.StopAll()
		if s.store != nil && s.state != nil {
			if err := s.store.Save(s.composeSnapshot(s.coord)); err != nil && s.logger != nil {
				s.logger.Printf("final checkpoint: %v", err)
			}
		}
		s.agents.CloseAll()
	}
	if s.state != nil && s.journal != nil {
		s.journal.Record("session_stop", map[string]any{"phase": string(res.Phase)})
	}
	if s.logger != nil {
		s.logger.Printf("session %s stopped (phase %s)", s.sessionID, res.Phase)
	}
	if s.transcripts != nil {
		s.transcripts.Close()
	}
	if s.lock != nil {
		_ = s.lock.Release()
		s.lock = nil
	}
	// Last, because it undoes everything the lines above acquired: the lock has
	// to be released before its directory can be removed, and the agents have to
	// be stopped before the worktrees they are sitting in can go.
	if s.discardUnstarted(ctx) {
		res.Discarded = true
	}
	if s.cancel != nil {
		s.cancel()
	}
	return res
}

// View reports the session's current state as a value.
func (s *Service) View() View {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.view()
}

func (s *Service) view() View {
	v := View{
		Version:     versionString(),
		SessionID:   s.sessionID,
		Mode:        s.mode,
		Language:    effectiveLanguage(s.opts.Language),
		TestCommand: s.opts.TestCommand,
		Notices:     append([]string(nil), s.notices...),
		Restart:     s.restart,
	}
	if s.state != nil {
		v.Project = s.state.Snapshot()
		v.Mode = v.Project.EffectiveMode()
	}
	if s.ws != nil {
		v.Worktrees = s.ws.Set()
	}
	if s.coord != nil {
		v.Delivery = s.coord.CurrentDelivery()
	} else {
		v.Delivery = s.delivery
	}
	v.Agents = make(map[protocol.AgentID]AgentView, 2)
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		v.Agents[id] = s.agentView(id)
	}
	return v
}

func (s *Service) agentView(id protocol.AgentID) AgentView {
	av := AgentView{}
	if s.tracker != nil {
		// Recent aliases the tracker's live slice; a View must own its copy.
		rt := s.tracker.Snapshot(id)
		rt.Recent = append([]harness.ToolNote(nil), rt.Recent...)
		av.Runtime = rt
	}
	if s.server != nil {
		av.Connected = s.server.IsConnected(id)
	}
	if s.coord != nil {
		av.Thinking = s.coord.Thinking(id)
	}
	if s.agents != nil {
		if d, ok := s.agents.Driver(id); ok {
			av.Present = true
			av.Driver = d.DriverType()
			av.Process = d.State()
			av.Model = d.Model()
			av.Manifest = cloneManifest(d.Manifest())
			av.Capabilities = d.Capabilities()
		}
		av.Command = s.agents.CommandFor(id)
	}
	return av
}

// cloneManifest returns a copy the caller owns.
//
// The driver hands out its own manifest, so storing that pointer in a View would
// let a frontend mutate live driver state through what the View's contract calls a
// read-only projection. Only the slice field can alias, so it is the one that needs
// copying.
func cloneManifest(m *driver.Manifest) *driver.Manifest {
	if m == nil {
		return nil
	}
	cloned := *m
	cloned.Notices = append([]string(nil), m.Notices...)
	return &cloned
}

// Subscription is a frontend's live view of a session: the state at the moment of
// subscribing, then every event after it.
//
// Handing the View and the stream over together is what makes a late-attaching
// frontend safe. There is no event sequence number, so a gap cannot be detected;
// starting from a full View means a frontend that joins late is merely missing
// events, never blind to the session's state.
type Subscription struct {
	View   View
	Events <-chan events.Event
}

// Subscribe returns the current View together with a stream of later events.
// Cancel stops delivery and closes the stream.
func (s *Service) Subscribe(buffer int) (Subscription, func()) {
	// Subscribe first so no event can slip between reading the View and the stream.
	ch, cancel := s.bus.Subscribe(buffer)
	return Subscription{View: s.View(), Events: ch}, cancel
}

// Do performs one human verb. It is serialized with Close so the session can
// never be torn down underneath a command.
func (s *Service) Do(ctx context.Context, cmd Command) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrClosed
	}
	if s.coord == nil {
		return errNotRunning
	}

	switch c := cmd.(type) {
	case SubmitTask:
		err := s.coord.SubmitUserTask(ctx, c.Text)
		s.syncMode()
		return err
	case SwitchMode:
		target, err := project.ParseMode(c.Mode)
		if err != nil {
			return err
		}
		if err := s.coord.SwitchMode(ctx, target, c.Reason); err != nil {
			return err
		}
		s.syncMode()
		return nil
	case EscalateToGoal:
		err := s.coord.SwitchMode(ctx, project.ModeGoal, c.Reason)
		s.syncMode()
		return err
	case SetModel:
		return s.setModel(ctx, c)
	case CycleThinking:
		return s.cycleThinking(ctx, c.Agent)
	case RestartAgent:
		if s.agents == nil {
			return errNotRunning
		}
		if err := s.agents.Restart(ctx, c.Agent); err != nil {
			return err
		}
		s.tracker.Reset(c.Agent)
		return nil
	case SetDriver:
		return s.setDriver(ctx, c)
	case SetLanguage:
		return s.setLanguage(c)
	case RestartDuo:
		return s.requestRestart(c)
	default:
		return fmt.Errorf("unsupported command %T", cmd)
	}
}

// syncMode keeps the service field used for logging and driver launch in step
// with the domain. Mode can change from a human command or an agent tool, so the
// state, not a cached copy, is authoritative.
func (s *Service) syncMode() {
	if s.state != nil {
		s.mode = s.state.Mode()
	}
}

func (s *Service) setDriver(ctx context.Context, c SetDriver) error {
	if c.Agent != protocol.Austin && c.Agent != protocol.Tony {
		return fmt.Errorf("unknown agent %q", c.Agent)
	}
	name := strings.TrimSpace(c.Driver)
	if name == "" {
		return errors.New("driver name is empty")
	}
	if name == s.opts.AgentDriver(c.Agent) {
		return nil
	}
	old, ok := s.agents.Session(c.Agent)
	if !ok {
		return fmt.Errorf("%s agent session is not running", c.Agent)
	}
	env := s.pluginEnv(c.Agent, s.bridgeHost, s.bridgePort, s.token)
	plugin, err := driverResolve(s.ctx, name, env, func(err error) {
		s.logger.Printf("%s: driver plugin: %v", c.Agent, err)
		s.bus.Emit(events.Event{Time: time.Now(), Kind: events.KindError, Agent: protocol.Duo, Text: fmt.Sprintf("%s driver plugin: %v", c.Agent, err)})
	})
	if err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	probe, err := plugin.Probe(probeCtx)
	if err != nil || probe == nil {
		plugin.Close()
		if err == nil {
			return errors.New("driver probe returned no result")
		}
		return fmt.Errorf("probe %s: %w", name, err)
	}
	if !probe.Available {
		plugin.Close()
		return fmt.Errorf("%s is unavailable: %s", name, probe.Reason)
	}

	commands, modelsByAgent, thinkingByAgent := driverSwitchOptions(s.opts, c.Agent)
	manifest := manifestOf(probeCtx, plugin)
	baseCommand, model, effort := ResolveDriver(commands, modelsByAgent, thinkingByAgent, c.Agent, manifest)
	previous, ok := old.(*agent.Session)
	if !ok {
		plugin.Close()
		return fmt.Errorf("%s session does not support driver replacement", c.Agent)
	}
	candidate, err := previous.WithDriver(probeCtx, plugin, baseCommand, model, effort)
	if err != nil {
		plugin.Close()
		return err
	}
	prepareCtx, prepareCancel := context.WithTimeout(ctx, 10*time.Second)
	if err := candidate.Preflight(prepareCtx); err != nil {
		prepareCancel()
		candidate.Close()
		return fmt.Errorf("prepare %s: %w", name, err)
	}
	prepareCancel()
	s.coord.EnableResumeWakeForAgent(c.Agent)
	if err := s.agents.Replace(ctx, candidate); err != nil {
		return err
	}
	if s.opts.AgentDrivers == nil {
		s.opts.AgentDrivers = make(map[protocol.AgentID]string)
	}
	if s.opts.AgentModels == nil {
		s.opts.AgentModels = make(map[protocol.AgentID]string)
	}
	if s.opts.AgentCommands == nil {
		s.opts.AgentCommands = make(map[protocol.AgentID]string)
	}
	if s.opts.AgentThinkings == nil {
		s.opts.AgentThinkings = make(map[protocol.AgentID]string)
	}
	s.opts.AgentDrivers[c.Agent] = name
	s.opts.AgentCommands[c.Agent] = baseCommand
	s.opts.AgentModels[c.Agent] = ""
	s.opts.AgentThinkings[c.Agent] = ""
	s.coord.SetCurrentModel(c.Agent, model)
	s.tracker.Reset(c.Agent)
	s.persistProjectConfig()
	if err := s.store.Save(s.composeSnapshot(s.coord)); err != nil {
		s.logger.Printf("checkpoint after driver switch: %v", err)
	}
	return nil
}

func driverSwitchOptions(opts Options, id protocol.AgentID) (commands, modelsByAgent, thinkingByAgent map[protocol.AgentID]string) {
	commands = cloneAgentStrings(opts.AgentCommands)
	modelsByAgent = cloneAgentStrings(opts.AgentModels)
	thinkingByAgent = cloneAgentStrings(opts.AgentThinkings)
	commands[id] = ""
	modelsByAgent[id] = ""
	thinkingByAgent[id] = ""
	return commands, modelsByAgent, thinkingByAgent
}

func cloneAgentStrings(src map[protocol.AgentID]string) map[protocol.AgentID]string {
	out := make(map[protocol.AgentID]string, len(src)+1)
	for k, v := range src {
		out[k] = v
	}
	return out
}

// requestRestart records that the session asked to continue on a new Duo binary.
// The agent asking is a child of the Duo it is upgrading, so it cannot relaunch
// itself: Duo stops, and a launcher outside Duo resumes the session on the new
// binary. The request is in-memory on purpose — a restart that never happened is
// recoverable with `duo resume`, a persisted one would restart a session forever.
func (s *Service) requestRestart(c RestartDuo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.restart = RestartRequest{Reason: strings.TrimSpace(c.Reason), Install: c.Install}
	return nil
}

// setLanguage switches the language Duo writes in and makes the switch real
// now: the coordinator's next prompts use it, agents pick it up when they next
// restart, and the project configuration records it so a resume and the next
// session start in the language the human chose. Unlike the model, a language
// that failed to persist is reported: the command promised to save it.
func (s *Service) setLanguage(c SetLanguage) error {
	language := strings.ToLower(strings.TrimSpace(c.Language))
	if language != "zh" && language != "en" {
		return fmt.Errorf("invalid language %q: must be zh or en", c.Language)
	}
	s.opts.Language = language
	s.coord.SetLanguage(language)
	repo := ""
	if s.ws != nil {
		repo = s.ws.Set().Repository
	}
	return workspace.SaveProjectLanguage(repo, language)
}

// setModel switches an agent's model and makes the switch real: the driver is told
// the reference it must launch with and, when it cannot switch live, is restarted
// so the flag takes effect. The project configuration is rewritten last so a
// session that is resumed uses the model it is actually running.
func (s *Service) setModel(ctx context.Context, c SetModel) error {
	if err := s.coord.SetModel(ctx, c.Agent, c.Provider, c.Model); err != nil {
		return err
	}
	ref := models.Model{Provider: c.Provider, ID: c.Model, Bare: s.bareModels(c.Agent)}.Reference()
	if s.agents != nil {
		if d, ok := s.agents.Driver(c.Agent); ok {
			// The launch flag needs the provider-qualified reference: opencode only
			// accepts provider/model and aborts on a bare id. A driver that strips the
			// prefix itself is told the bare one.
			d.SetModel(ref)
			if !d.Capabilities().LiveModelSwitch {
				// Without a live switch the model only reaches the agent as a startup
				// flag, and RestartRunning stops before it starts, so a failure here
				// leaves the agent dead. Report it instead of claiming the new model.
				if err := d.RestartRunning(ctx); err != nil {
					return fmt.Errorf("%s model → %s failed: %w", c.Agent, ref, err)
				}
			}
		}
	}
	s.persistProjectConfig()
	return nil
}

// bareModels reports whether a driver takes bare model ids.
func (s *Service) bareModels(agent protocol.AgentID) bool {
	if m := s.agents.ManifestFor(agent); m != nil {
		return m.Bare()
	}
	return false
}

// cycleThinking advances an agent's effort and, when its driver cannot switch
// effort live, restarts the agent so the level takes effect. The restart used to
// be the interface's job; it belongs to the session, because the failure it can
// produce is a fact about an agent process.
func (s *Service) cycleThinking(ctx context.Context, agent protocol.AgentID) error {
	if err := s.coord.CycleThinking(ctx, agent); err != nil {
		return err
	}
	thinking := s.coord.Thinking(agent)
	if thinking == "" {
		thinking = "unknown"
	}
	if s.agents != nil {
		if d, ok := s.agents.Driver(agent); ok {
			d.SetEffort(thinking)
			if !d.Capabilities().LiveThinkingSwitch {
				if err := d.RestartRunning(ctx); err != nil {
					return fmt.Errorf("%s thinking: %s failed: %w", agent, thinking, err)
				}
			}
		}
	}
	return nil
}

// persistProjectConfig records the drivers and models in use, so the next session
// remembers them. It is best-effort: the file is a convenience, never a source of
// truth.
func (s *Service) persistProjectConfig() {
	repo := ""
	if s.ws != nil {
		repo = s.ws.Set().Repository
	}
	if repo == "" {
		return
	}
	drivers := map[protocol.AgentID]string{
		protocol.Austin: s.opts.AgentDriver(protocol.Austin),
		protocol.Tony:   s.opts.AgentDriver(protocol.Tony),
	}
	mods := map[protocol.AgentID]string{
		protocol.Austin: s.opts.AgentModels[protocol.Austin],
		protocol.Tony:   s.opts.AgentModels[protocol.Tony],
	}
	_ = workspace.SaveProjectConfig(repo, s.opts.AgentDriver(protocol.Austin), drivers, mods)
}

// Models reads an agent's model catalog. It is a read, not a command: a picker
// asks, and nothing about the session changes.
func (s *Service) Models(ctx context.Context, agent protocol.AgentID) ([]models.Model, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.agents == nil {
		return nil, errNotRunning
	}
	d, ok := s.agents.Driver(agent)
	if !ok {
		return nil, fmt.Errorf("%s has no driver", agent)
	}
	return models.Load(ctx, d)
}

// StatusText renders the workflow status the way the coordinator would answer a
// status request over the bridge.
func (s *Service) StatusText(ctx context.Context) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.coord == nil {
		return ""
	}
	return s.coord.StatusText(ctx)
}

// Changes lists what the session has changed relative to its base, for a detail
// view. It is a read: the session is not touched.
func (s *Service) Changes(ctx context.Context) []string {
	s.mu.RLock()
	set := s.set
	s.mu.RUnlock()
	if set.Austin.Path == "" {
		return nil
	}
	base := set.BaseCommit
	if base == "" {
		base = set.BaseBranch
	}
	return gitChanges(ctx, set.Austin.Path, base)
}

// History returns the interface entries recorded before this session resumed, so
// a frontend can show the conversation that led here.
func (s *Service) History() []sessionstore.TUIEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]sessionstore.TUIEntry(nil), s.history...)
}

// Notices returns the diagnostics a driver reported while preparing a launch.
// Drivers are built before a frontend exists, so they are buffered and handed over
// here.
func (s *Service) Notices() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.notices...)
}

// RecordEntry writes one interface entry to the journal and the transcript.
//
// It is a seam method rather than something a frontend does for itself because the
// journal and the Markdown transcript are session records: a second frontend must
// produce the same durable history as the first.
func (s *Service) RecordEntry(entry sessionstore.TUIEntry) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.journal != nil {
		s.journal.RecordTUIEntry(entry)
	}
	if s.transcripts != nil {
		s.transcripts.Append(entry)
	}
}

// Attach hands an agent's process output to w and marks the agent as
// human-attached. The returned bytes are the screen the agent last printed, which
// the caller replays so the attached view begins where the agent is.
//
// This is deliberately not a command: a live process byte stream cannot be
// serialized, so it is a separate surface with no wire representation.
func (s *Service) Attach(agent protocol.AgentID, w io.Writer) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.agents == nil {
		return nil, errNotRunning
	}
	d, ok := s.agents.Session(agent)
	if !ok || !d.Running() {
		return nil, fmt.Errorf("%s agent session is not running", agent)
	}
	if s.tracker != nil {
		s.tracker.SetHumanAttached(agent, true)
	}
	return d.Attach(w), nil
}

// Detach undoes Attach.
func (s *Service) Detach(agent protocol.AgentID) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.agents != nil {
		if d, ok := s.agents.Session(agent); ok {
			d.Detach()
		}
	}
	if s.tracker != nil {
		s.tracker.SetHumanAttached(agent, false)
	}
}

// Write sends input bytes to an attached agent's process.
func (s *Service) Write(agent protocol.AgentID, data []byte) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.agents == nil {
		return errNotRunning
	}
	d, ok := s.agents.Session(agent)
	if !ok {
		return fmt.Errorf("%s agent session is not running", agent)
	}
	return d.Write(data)
}

// Resize tells the agent processes how large the frontend now is, so the agent
// CLI wraps to the same width the user sees.
func (s *Service) Resize(cols, rows int) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.agents == nil {
		return nil
	}
	return s.agents.ResizeAll(cols, rows)
}

// collectNotice records a diagnostic a driver reported while preparing a launch.
//
// Drivers are built before the frontend exists, so a notice produced during a
// launch is buffered here and handed over as soon as there is somewhere to show
// it. It is the driver's own words: Duo Core hard-codes no warning about any agent.
func (s *Service) collectNotice(notices []string) {
	s.noticeMu.Lock()
	s.notices = append(s.notices, notices...)
	s.noticeMu.Unlock()
}

// pluginEnv is the environment a driver plugin process gets: exactly the one Duo
// Core gives an agent. An Agent Adapter therefore has everything it needs to reach
// Duo's bridge without Core inventing a second channel, and no driver-specific
// variable is set for a driver that does not own it.
func (s *Service) pluginEnv(id protocol.AgentID, host, port, token string) []string {
	// The operator's environment comes first and Duo's own variables are appended
	// after it, so ours win: the process reads the later of two identical names.
	//
	// The world the agent gets is the world the plugin runs in. Withholding it
	// looks harmless and is not — HOME is where every one of these CLIs keeps its
	// configuration and credentials, and PATH is how a plugin finds the CLI it
	// wraps. Without them a plugin silently answered from a hardcoded default and
	// reported a CLI missing that was sitting right there on PATH.
	mode := s.mode
	if s.state != nil {
		mode = s.state.Mode()
	}
	return append(os.Environ(), []string{
		"DUO_ACTIVE=1",
		"DUO_AGENT=" + string(id),
		"DUO_MODE=" + mode.String(),
		"DUO_LANGUAGE=" + effectiveLanguage(s.opts.Language),
		"DUO_HOST=" + host,
		"DUO_PORT=" + port,
		"DUO_SESSION=" + s.sessionID,
		"DUO_TOKEN=" + token,
		"DUO_REPOSITORY_ROOT=" + s.set.Repository,
		"DUO_SCOPE_PATH=" + s.set.ScopePath,
	}...)
}

func effectiveLanguage(language string) string {
	if strings.TrimSpace(language) == "zh" {
		return "zh"
	}
	return "en"
}

// discardUnstarted removes everything a session that never started left behind:
// the worktrees and their branches, the session record, and the transcripts it
// wrote into the user's repository.
//
// The rule it enforces is symmetry. Launching Duo and leaving without giving it
// a task should leave the repository and the disk exactly as they were; today it
// leaves two worktrees, two branches, a session that `duo resume` would happily
// offer, and three transcript files the human never asked for and cannot later
// explain.
//
// Two independent conditions have to hold, and both are deliberate. The session
// must not have started, which is the workflow's own record that no task was
// submitted and no agent ever began a turn. Git must also report nothing worth
// keeping, because that record is only as good as the drivers' reporting: a
// driver that never announces a turn would leave a session looking untouched
// while the human worked in it through an attached terminal. Asking Git is what
// makes this safe for a driver nobody has audited.
//
// note writes to the session log when there is one. Nothing here is reported as
// an error. A session being torn down is not the
// moment to fail, so every failure is logged and the session is left resumable
// instead — `duo clean` remains the way to remove it by hand.
func (s *Service) discardUnstarted(ctx context.Context) bool {
	if s.state == nil || s.set.Session == "" {
		return false
	}
	if s.state.Snapshot().Started {
		return false
	}
	carries, err := s.set.CarriesWork(ctx)
	if err != nil {
		s.note("keeping session %s: could not tell whether its worktrees are empty: %v", s.set.Session, err)
		return false
	}
	if carries {
		s.note("keeping session %s: a worktree has changes or commits, so it is not an untouched run", s.set.Session)
		return false
	}

	_ = s.transcripts.Remove()
	if err := s.set.Discard(ctx); err != nil {
		s.note("removing worktrees for session %s: %v", s.set.Session, err)
		return false
	}
	if s.store != nil {
		if err := s.store.Remove(); err != nil {
			s.note("removing the record for session %s: %v", s.set.Session, err)
			return false
		}
	}
	s.note("session %s was ended before a task was submitted and left nothing behind", s.set.Session)
	return true
}

// note appends one line to the session log. The teardown path uses it instead of
// returning an error: a session being closed is not the moment to fail, and a
// line in the log is enough for the operator who wonders why their worktree is
// still there.
func (s *Service) note(format string, args ...any) {
	if s.logger == nil {
		return
	}
	s.logger.Printf(format, args...)
}
