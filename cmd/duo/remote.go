package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/gotui"
	"github.com/atfa/duo/internal/models"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/remote"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
)

// remoteManager owns the pairing file and the live remote client for one Duo
// process.
//
// It is the only place that maps a session view into the snapshot a phone
// renders and a decrypted command into a session command, so the remote package
// stays free of workflow state and the page can only reach the same bounded
// verbs a human has.
type remoteManager struct {
	svc     *session.Service
	path    string
	server  string
	enabled bool
	now     func() time.Time

	mu      sync.Mutex
	pairing remote.Pairing
	client  *remote.Client
	cancel  context.CancelFunc

	// drivers is the discovered driver list the phone offers. Discovery reads
	// directories and executes nothing, and the answer only changes when a plugin
	// is installed, so it is read once rather than on every published state.
	driversOnce sync.Once
	drivers     []string

	// catalogs is one loaded model list per agent, with the provider/id pairs
	// set_model needs. Loading one starts the driver's own CLI, so it stays out of
	// the snapshot until a phone asks; catalogShown is which of them the snapshot
	// publishes, so the state stays the size of one list.
	catalogMu    sync.Mutex
	catalogs     map[protocol.AgentID]catalogEntry
	catalogLoads map[protocol.AgentID]bool
	catalogShown protocol.AgentID
}

func newRemoteManager(cfg config, svc *session.Service) (*remoteManager, error) {
	path, err := remote.PairingPath()
	if err != nil {
		return nil, err
	}
	return &remoteManager{
		svc:     svc,
		path:    path,
		server:  remote.NormalizeServer(cfg.remoteURL),
		enabled: cfg.remoteEnabled,
		now:     time.Now,
	}, nil
}

// Resume reconnects a pairing that is still live. A machine that never paired
// sends nothing to the relay and costs nothing, which is the default posture.
//
// The registration is not waited for: Run registers before it connects, and
// blocking here would hold the interface's first frame for as long as the relay
// takes to answer. Nothing about resuming a pairing needs that round trip
// before the dashboard is drawn.
func (m *remoteManager) Resume(ctx context.Context) {
	if !m.enabled {
		return
	}
	pairing, ok := remote.LivePairing(m.path, m.now())
	if !ok {
		return
	}
	if err := m.start(ctx, pairing, false); err != nil {
		log.Printf("Duo remote: %v", err)
	}
}

// pairingState is how this process relates to the pairing on disk.
type pairingState int

const (
	// pairingUnheld: nothing is connected for it yet, so /qrcode has to start it.
	pairingUnheld pairingState = iota
	// pairingHeld: this process holds a live connection for it, so /qrcode can
	// show the same code again without touching the relay.
	pairingHeld
	// pairingEnded: the relay ended the room for good (an exhausted daily budget,
	// a revocation, an expiry), so its code can only produce "Duo is offline".
	pairingEnded
)

func (m *remoteManager) stateOf(pairing remote.Pairing) pairingState {
	m.mu.Lock()
	client, current := m.client, m.pairing
	m.mu.Unlock()
	if client == nil || current.Room != pairing.Room {
		return pairingUnheld
	}
	if client.Status().Active {
		return pairingHeld
	}
	return pairingEnded
}

// Start is `/qrcode`: it reuses a live pairing, or mints and saves a new one.
func (m *remoteManager) Start(ctx context.Context, rotate bool) (gotui.RemotePairing, error) {
	if !m.enabled {
		return gotui.RemotePairing{}, errors.New("remote access is disabled (remote.enabled=false or DUO_REMOTE=off)")
	}
	if m.server == "" {
		return gotui.RemotePairing{}, errors.New("no relay URL configured (remote.url or DUO_REMOTE_URL)")
	}
	pairing, live := remote.LivePairing(m.path, m.now())
	if live && !rotate {
		switch m.stateOf(pairing) {
		case pairingHeld:
			// The code the human is asking for already works. Registering again
			// would cost a relay round trip and drop the socket the phone may be
			// using, so the panel is answered from the live pairing instead.
			return m.pairingResult(pairing), nil
		case pairingEnded:
			live = false
		}
	}
	if !live || rotate {
		fresh, err := remote.NewPairing(m.server, m.now(), remote.DefaultRoomTTL)
		if err != nil {
			return gotui.RemotePairing{}, err
		}
		if err := remote.SavePairing(m.path, fresh); err != nil {
			return gotui.RemotePairing{}, err
		}
		pairing = fresh
	}
	if err := m.start(ctx, pairing, true); err != nil {
		return gotui.RemotePairing{}, err
	}
	return m.pairingResult(pairing), nil
}

// pairingResult is what a frontend draws for one pairing.
func (m *remoteManager) pairingResult(pairing remote.Pairing) gotui.RemotePairing {
	return gotui.RemotePairing{
		URL:     pairing.URL(),
		Display: pairing.Server + "/p/" + pairing.Room,
		Status:  m.Status(),
	}
}

// Stop is `/remote off`: it revokes the room on the relay, disconnects, and
// forgets the pairing. Revocation is best-effort — losing the network must not
// leave the human unable to switch remote access off locally.
func (m *remoteManager) Stop(ctx context.Context) (gotui.RemoteStatus, error) {
	m.mu.Lock()
	client := m.client
	m.mu.Unlock()

	var err error
	if client != nil {
		revokeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = client.Revoke(revokeCtx)
		cancel()
		client.Close()
	}
	m.stopClient()
	if delErr := remote.DeletePairing(m.path); delErr != nil && err == nil {
		err = delErr
	}
	m.mu.Lock()
	m.pairing = remote.Pairing{}
	m.mu.Unlock()
	return m.Status(), err
}

// Status reports the pairing without touching the network.
func (m *remoteManager) Status() gotui.RemoteStatus {
	m.mu.Lock()
	pairing := m.pairing
	client := m.client
	m.mu.Unlock()

	status := gotui.RemoteStatus{
		Server:    pairing.Server,
		Room:      pairing.Room,
		ExpiresAt: pairing.ExpiresAt,
	}
	if status.Server == "" {
		status.Server = m.server
	}
	status.Enabled = m.enabled && pairing.Room != "" && pairing.Live(m.now())
	if client != nil {
		current := client.Status()
		status.Connected = current.Connected
		status.Guests = current.Guests
		status.LastError = current.LastError
	}
	return status
}

// start builds the client for a pairing and starts serving it. wait registers
// the room before returning, which /qrcode needs because a code for an
// unregistered room cannot pair; resume does not, because Run registers again
// before every connection and the interface must not wait for the relay.
func (m *remoteManager) start(ctx context.Context, pairing remote.Pairing, wait bool) error {
	m.stopClient()
	client, err := remote.New(remote.Options{
		Pairing:  pairing,
		Sink:     remoteSink{m: m},
		Snapshot: m.snapshot,
		Logf: func(format string, args ...any) {
			log.Printf("Duo remote: "+format, args...)
		},
	})
	if err != nil {
		return err
	}
	if wait {
		registerCtx, registerCancel := context.WithTimeout(ctx, 15*time.Second)
		err = client.RegisterRoom(registerCtx)
		registerCancel()
		if err != nil {
			client.Close()
			return fmt.Errorf("register remote room before showing pairing: %w", err)
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.pairing = pairing
	m.client = client
	m.cancel = cancel
	m.mu.Unlock()
	go client.Run(runCtx)
	go m.watch(runCtx, client)
	return nil
}

func (m *remoteManager) stopClient() {
	m.mu.Lock()
	client := m.client
	cancel := m.cancel
	m.client = nil
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if client != nil {
		client.Close()
	}
}

// watch republishes the snapshot whenever the session changes. The client
// coalesces to one frame a second, so a busy turn does not become a stream of
// near-identical states, and it sends nothing at all while no phone is attached.
func (m *remoteManager) watch(ctx context.Context, client *remote.Client) {
	sub, cancel := m.svc.Subscribe(32)
	defer cancel()
	client.PublishState()
	// An observer report is view state, not an event: it arrives when a model
	// answers, which can be a minute after the phone asked. Polling only while a
	// report is in flight costs nothing when nobody asked for one, and stops on
	// its own, so the phone is never on a timer.
	poll := time.NewTicker(observerReportPoll)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			if m.svc.View().Observer.Analyzing {
				client.PublishState()
			}
			continue
		case ev, ok := <-sub.Events:
			if !ok {
				return
			}
			// A conversation line is cheap and frequent, so it goes out as an
			// event; anything that changes the workflow goes out as a whole
			// snapshot, which is the one thing a phone can always render.
			if kind := eventKind(ev.Kind); kind != "" {
				client.PublishEvent(remote.Event{
					Kind:  kind,
					Agent: agentKey(ev.Agent),
					Text:  clip(ev.Text, 240),
				})
				continue
			}
			client.PublishState()
		}
	}
}

// observerReportPoll is how often the host checks for a finished observer
// report while one is in flight. It is slow on purpose: the observer is
// on-demand, and a report is worth waiting a few seconds for, not streaming.
const observerReportPoll = 5 * time.Second

// eventKind maps a session event to the incremental update a phone applies, or
// returns "" when the change needs a full snapshot.
func eventKind(kind events.Kind) string {
	switch kind {
	case events.KindAssistant, events.KindPeer, events.KindUser, events.KindSystem, events.KindError, events.KindVerdict:
		return remote.EventActivity
	}
	return ""
}

// agentKey is the wire spelling of an agent: the phone addresses its agents as
// "austin" and "tony", which is what docs/remote-protocol.md documents, so both
// the snapshot keys and an event's agent field go through this one function.
func agentKey(id protocol.AgentID) string { return strings.ToLower(string(id)) }

// remoteAgent is one agent's slice of the snapshot.
//
// Process state is reported only for an agent the session actually runs: the
// zero value of the state type is "starting", so an agent with no driver would
// otherwise tell the phone it is starting when it is not configured at all.
func remoteAgent(av session.AgentView) remote.Agent {
	state := ""
	if av.Present {
		state = av.Process.String()
	}
	return remote.Agent{
		Connected: av.Connected,
		Running:   av.Runtime.Busy,
		Driver:    av.Driver,
		Model:     av.Model,
		Thinking:  av.Thinking,
		Activity:  clip(agentActivity(av), 160),
		State:     state,
	}
}

// snapshot maps the session view into the projection a phone renders.
func (m *remoteManager) snapshot() remote.State {
	view := m.svc.View()
	state := remote.State{
		Project: projectName(view),
		Repo:    projectName(view),
		Session: view.SessionID,
		Mode:    strings.ToLower(view.EffectiveMode().Display()),
		Phase:   string(view.Phase()),
		Started: view.Project.Started,
		Drivers: m.driverNames(),
		Catalog: m.catalogSnapshot(),
		Plan:    firstLine(view.Project.Plan),
		Pause:   remote.PauseState{State: string(view.Pause.State)},
		Agents:  map[string]remote.Agent{},
	}
	if step, ok := currentStep(view.Project); ok {
		state.Step = &step
	}
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		state.Agents[agentKey(id)] = remoteAgent(view.Agent(id))
	}
	history := m.svc.History()
	for i := len(history) - 1; i >= 0 && len(state.Activity) < 8; i-- {
		entry := history[i]
		if strings.TrimSpace(entry.Text) == "" {
			continue
		}
		state.Activity = append([]remote.Activity{{
			At:    entry.Time.UnixMilli(),
			Kind:  activityKind(entry),
			Agent: activityAgent(entry),
			Text:  clip(entry.Text, 240),
		}}, state.Activity...)
	}
	state.Observer = remote.ObserverState{Analyzing: view.Observer.Analyzing}
	for _, report := range view.Observer.Reports {
		state.Observer.Reports = append(state.Observer.Reports, remote.ObserverReport{
			At:   report.At.UnixMilli(),
			Text: clip(report.Text, 400),
		})
	}
	if view.Delivery.Status != "" || view.Delivery.AppliedHead != "" {
		state.Delivery = &remote.DeliveryState{
			Applied: view.Delivery.Status == "applied",
			Head:    view.Delivery.AppliedHead,
			Note:    view.Delivery.Reason,
		}
	}
	return state
}

// remoteSink applies a decrypted command through the same session seam a human
// uses. It adds no verb the interface does not already have.
type remoteSink struct{ m *remoteManager }

func (s remoteSink) SubmitTask(text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.m.svc.Do(ctx, session.SubmitTask{Text: text})
}

func (s remoteSink) NewSession() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.m.svc.Do(ctx, session.NewSession{})
}

func (s remoteSink) RequestObserverReport() { s.m.svc.RequestObserverReport() }

// driverNames is every driver this machine can run, in the order the interface
// offers them.
func (m *remoteManager) driverNames() []string {
	m.driversOnce.Do(func() {
		choices, err := m.svc.Drivers(context.Background())
		if err != nil {
			return
		}
		for _, choice := range choices {
			m.drivers = append(m.drivers, choice.Name)
		}
	})
	return m.drivers
}

// wireAgent is the inverse of agentKey: the phone names an agent "austin" or
// "tony", and any other name is a command the host refuses rather than guesses
// at.
func wireAgent(name string) (protocol.AgentID, error) {
	switch id := protocol.CanonicalAgent(name); id {
	case protocol.Austin, protocol.Tony:
		return id, nil
	}
	return "", fmt.Errorf("unknown agent %q", name)
}

// SetDriver replaces one agent's driver. The session owns the gate that says
// when that is safe (before the first task, while fully paused, or after DONE)
// and probes the plugin before using it, so the phone gets the same answer the
// interface would.
func (s remoteSink) SetDriver(agent, driver string) error {
	id, err := wireAgent(agent)
	if err != nil {
		return err
	}
	previous := s.m.svc.View().Agent(id).Driver
	// A driver change resolves, probes and prepares a plugin, which the interface
	// waits for without a deadline. The phone waits 60s for the answer, so the
	// host must not give up sooner and report a failure for work still running.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := s.m.svc.Do(ctx, session.SetDriver{Agent: id, Driver: driver}); err != nil {
		return err
	}
	// The list belongs to the driver that published it, so a change of driver drops
	// it. Naming the driver already running is a no-op, and must not throw away a
	// list that still describes what runs.
	if previous != strings.TrimSpace(driver) {
		s.m.forgetCatalog(id)
	}
	return nil
}

// RequestCatalog shows one agent's model list to the phone, loading it first when
// this agent has none for the driver it runs. The load runs off the command path
// because it starts the driver's own CLI; the phone sees Loading at once and the
// list, or the reason there is none, when it finishes.
func (s remoteSink) RequestCatalog(agent string) error {
	id, err := wireAgent(agent)
	if err != nil {
		return err
	}
	s.m.requestCatalog(id)
	return nil
}

// SetModel switches one agent's model to an entry of the list the phone was
// shown, so the provider and the id are exact rather than guessed from a string
// that may contain a slash.
func (s remoteSink) SetModel(agent, reference string) error {
	id, err := wireAgent(agent)
	if err != nil {
		return err
	}
	model, err := s.m.resolveModel(id, reference)
	if err != nil {
		return err
	}
	// Changing a model restarts an agent whose driver cannot switch live, which
	// takes as long as a driver change; the phone waits 60s for it.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return s.m.svc.Do(ctx, session.SetModel{Agent: id, Provider: model.provider, Model: model.id})
}

// catalogModel is one resolved catalog entry: the reference the phone shows and
// the provider/id pair the session needs.
type catalogModel struct {
	provider string
	id       string
	ref      string
}

const (
	// maxCatalogModels bounds one published list. A phone filters its picker, so a
	// few dozen rows are enough, and MaxPlaintext is 3000 bytes: Total carries the
	// real count so the page can say the list was cut.
	maxCatalogModels = 40
	// catalogTimeout bounds one catalog read. Listing models starts the driver's own
	// CLI (driver.ModelListTimeout is 20s), and a phone waiting on a wedged plugin
	// must be told rather than left loading forever.
	catalogTimeout = 30 * time.Second
)

// catalogEntries resolves what a driver published into the reference the phone
// shows and the pair the session needs. The reference is built from the two
// fields rather than models.Model.Reference, which drops the provider for a
// driver that takes bare ids — and the phone should still see which provider it
// is choosing.
func catalogEntries(list []models.Model) []catalogModel {
	entries := make([]catalogModel, 0, len(list))
	for _, model := range list {
		ref := model.ID
		if model.Provider != "" {
			ref = model.Provider + "/" + model.ID
		}
		entries = append(entries, catalogModel{provider: model.Provider, id: model.ID, ref: ref})
	}
	return entries
}

// catalogState is what the phone sees: at most maxCatalogModels references, the
// true count, and the reason a driver published none.
func catalogState(agent protocol.AgentID, entries []catalogModel, err error) *remote.CatalogState {
	state := &remote.CatalogState{Agent: agentKey(agent), Total: len(entries)}
	if err != nil {
		state.Error = err.Error()
		return state
	}
	for i, entry := range entries {
		if i == maxCatalogModels {
			break
		}
		state.Models = append(state.Models, entry.ref)
	}
	return state
}

// catalogEntry is one agent's loaded model list, tied to the driver that
// published it: a different driver has a different list.
type catalogEntry struct {
	state  *remote.CatalogState
	models []catalogModel
	driver string
}

// requestCatalog shows one agent's list to the phone, loading it first when this
// agent has none for the driver it runs. A list already loaded is published as it
// is: asking again must not start the driver's CLI a second time, and asking
// about another agent must never replace or drop it.
func (m *remoteManager) requestCatalog(agent protocol.AgentID) {
	driver := m.svc.View().Agent(agent).Driver

	m.catalogMu.Lock()
	if m.catalogs == nil {
		m.catalogs = map[protocol.AgentID]catalogEntry{}
	}
	if m.catalogLoads == nil {
		m.catalogLoads = map[protocol.AgentID]bool{}
	}
	// The newest request decides what the phone is looking at, whether or not the
	// list is already here.
	m.catalogShown = agent
	if entry, ok := m.catalogs[agent]; ok && entry.driver == driver {
		m.catalogMu.Unlock()
		m.publish()
		return
	}
	if m.catalogLoads[agent] {
		// This agent's list is already being read; a second CLI would only be slower.
		m.catalogMu.Unlock()
		m.publish()
		return
	}
	m.catalogLoads[agent] = true
	m.catalogs[agent] = catalogEntry{
		state:  &remote.CatalogState{Agent: agentKey(agent), Loading: true},
		driver: driver,
	}
	m.catalogMu.Unlock()
	m.publish()

	go m.loadCatalog(agent, driver)
}

// loadCatalog reads one agent's catalog and republishes the snapshot. It runs off
// the command path because it starts the driver's own CLI.
func (m *remoteManager) loadCatalog(agent protocol.AgentID, driver string) {
	ctx, cancel := context.WithTimeout(context.Background(), catalogTimeout)
	defer cancel()
	list, err := m.svc.Models(ctx, agent)
	var entries []catalogModel
	if err == nil {
		entries = catalogEntries(list)
	}
	state := catalogState(agent, entries, err)

	// A driver change while the read was running makes this answer describe a
	// process that is no longer there, so it is dropped rather than shown.
	current := m.svc.View().Agent(agent).Driver
	m.catalogMu.Lock()
	m.catalogLoads[agent] = false
	if current == driver {
		m.catalogs[agent] = catalogEntry{state: state, models: entries, driver: driver}
	}
	m.catalogMu.Unlock()
	m.publish()
}

// resolveModel finds the entry in this agent's own list, so a list the phone kept
// for one agent still resolves while it looks at the other. A list loaded before
// a driver change no longer describes what runs and is refused rather than used.
func (m *remoteManager) resolveModel(agent protocol.AgentID, reference string) (catalogModel, error) {
	reference = strings.TrimSpace(reference)
	driver := m.svc.View().Agent(agent).Driver
	m.catalogMu.Lock()
	defer m.catalogMu.Unlock()
	entry, ok := m.catalogs[agent]
	if !ok || entry.state == nil {
		return catalogModel{}, fmt.Errorf("no model list for %s yet; open its model list first", agent)
	}
	if entry.state.Loading {
		return catalogModel{}, fmt.Errorf("%s's model list is still loading; try again in a moment", agent)
	}
	if entry.driver != driver {
		return catalogModel{}, fmt.Errorf("%s now runs %s; refresh its model list and pick again", agent, driver)
	}
	for _, model := range entry.models {
		if model.ref == reference {
			return model, nil
		}
	}
	return catalogModel{}, fmt.Errorf("%q is not in %s's model list; refresh it and pick again", reference, agent)
}

// forgetCatalog drops one agent's list after its driver changed. A read already
// in flight for it is left to finish and is discarded when it does, because it
// carries the driver that is gone.
func (m *remoteManager) forgetCatalog(agent protocol.AgentID) {
	m.catalogMu.Lock()
	defer m.catalogMu.Unlock()
	delete(m.catalogs, agent)
	if m.catalogShown == agent {
		m.catalogShown = ""
	}
}

// catalogSnapshot is the list the phone is looking at, or nil when it has asked
// for none. Publishing one list rather than all of them keeps the state the size
// of a single catalog.
func (m *remoteManager) catalogSnapshot() *remote.CatalogState {
	m.catalogMu.Lock()
	defer m.catalogMu.Unlock()
	entry, ok := m.catalogs[m.catalogShown]
	if !ok || entry.state == nil {
		return nil
	}
	copy := *entry.state
	copy.Models = append([]string(nil), entry.state.Models...)
	return &copy
}

// publish republishes the snapshot after a change the session did not report,
// which is how an asynchronous catalog load reaches the phone.
func (m *remoteManager) publish() {
	m.mu.Lock()
	client := m.client
	m.mu.Unlock()
	if client != nil {
		client.PublishState()
	}
}

// SetMode switches the workflow from the phone. It is quiet, like the
// interface's own mode change: the agents are not woken up, and the task the
// human sends next is the one that runs in the new mode. A switch to the mode
// already running is a no-op, because the phone would otherwise report a
// failure for a tap that changed nothing.
func (s remoteSink) SetMode(mode string) error {
	target, err := project.ParseMode(mode)
	if err != nil {
		return err
	}
	if target == s.m.svc.View().EffectiveMode() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.m.svc.Do(ctx, session.SwitchMode{Mode: target.String(), Quiet: true})
}

// projectName is the repository's last path element: the phone shows a project
// name, not a home directory.
func projectName(view session.View) string {
	root := view.Worktrees.Repository
	if root == "" {
		root = view.Worktrees.Root
	}
	if root == "" {
		return ""
	}
	return filepath.Base(root)
}

func currentStep(state project.Snapshot) (remote.StepState, bool) {
	if len(state.Steps) == 0 || state.Step < 1 || state.Step > len(state.Steps) {
		return remote.StepState{}, false
	}
	step := state.Steps[state.Step-1]
	return remote.StepState{
		Index:  state.Step,
		Total:  len(state.Steps),
		Policy: string(step.EffectivePolicy()),
		Title:  clip(step.Title, 120),
	}, true
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	return clip(strings.TrimSpace(text), 200)
}

func agentActivity(av session.AgentView) string {
	if av.Runtime.StreamTail != "" {
		return av.Runtime.StreamTail
	}
	if av.Runtime.Tool != "" {
		if av.Runtime.ToolDetail != "" {
			return av.Runtime.Tool + ": " + av.Runtime.ToolDetail
		}
		return av.Runtime.Tool
	}
	return ""
}

func activityKind(entry sessionstore.TUIEntry) string {
	switch {
	case entry.Error:
		return "error"
	case entry.Warning:
		return "verdict"
	default:
		return "activity"
	}
}

func activityAgent(entry sessionstore.TUIEntry) string {
	switch id := protocol.CanonicalAgent(entry.Pane); id {
	case protocol.Austin, protocol.Tony:
		return agentKey(id)
	}
	return ""
}

// clip shortens a text field to a phone-sized excerpt, counting runes so a
// multi-byte character is never cut in half.
func clip(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}

// validateRemoteURL refuses a relay URL that cannot carry a pairing safely.
// A cleartext URL is only accepted for a local relay, which is what a developer
// running `wrangler dev` uses.
func validateRemoteURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("remote.url: %w", err)
	}
	switch u.Scheme {
	case "https", "wss":
		if u.Host == "" {
			return errors.New("remote.url has no host")
		}
	case "http", "ws":
		host := u.Hostname()
		if host != "localhost" && host != "127.0.0.1" && host != "::1" && !strings.HasSuffix(host, ".localhost") {
			return fmt.Errorf("remote.url %q is cleartext; use https for a remote relay", raw)
		}
	default:
		return fmt.Errorf("remote.url %q must be https", raw)
	}
	return nil
}
