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
func (m *remoteManager) Resume(ctx context.Context) {
	if !m.enabled {
		return
	}
	pairing, ok := remote.LivePairing(m.path, m.now())
	if !ok {
		return
	}
	if err := m.start(ctx, pairing); err != nil {
		log.Printf("Duo remote: %v", err)
	}
}

// Start is `/qrcode`: it reuses a live pairing, or mints and saves a new one.
func (m *remoteManager) Start(ctx context.Context, rotate bool) (gotui.RemotePairing, error) {
	if !m.enabled {
		return gotui.RemotePairing{}, errors.New("remote access is disabled (remote.enabled=false or DUO_REMOTE=off)")
	}
	if m.server == "" {
		return gotui.RemotePairing{}, errors.New("no relay URL configured (remote.url or DUO_REMOTE_URL)")
	}
	pairing, ok := remote.LivePairing(m.path, m.now())
	if !ok || rotate {
		fresh, err := remote.NewPairing(m.server, m.now(), remote.DefaultRoomTTL)
		if err != nil {
			return gotui.RemotePairing{}, err
		}
		if err := remote.SavePairing(m.path, fresh); err != nil {
			return gotui.RemotePairing{}, err
		}
		pairing = fresh
	}
	if err := m.start(ctx, pairing); err != nil {
		return gotui.RemotePairing{}, err
	}
	return gotui.RemotePairing{
		URL:     pairing.URL(),
		Display: pairing.Server + "/p/" + pairing.Room,
		Status:  m.Status(),
	}, nil
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

func (m *remoteManager) start(ctx context.Context, pairing remote.Pairing) error {
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
	registerCtx, registerCancel := context.WithTimeout(ctx, 15*time.Second)
	err = client.RegisterRoom(registerCtx)
	registerCancel()
	if err != nil {
		client.Close()
		return fmt.Errorf("register remote room before showing pairing: %w", err)
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
// coalesces to four frames a second, so a busy turn does not become a stream of
// near-identical states.
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
					Agent: string(ev.Agent),
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
		Plan:    firstLine(view.Project.Plan),
		Pause:   remote.PauseState{State: string(view.Pause.State)},
		Agents:  map[string]remote.Agent{},
	}
	if step, ok := currentStep(view.Project); ok {
		state.Step = &step
	}
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		av := view.Agent(id)
		state.Agents[string(id)] = remote.Agent{
			Connected: av.Connected,
			Running:   av.Runtime.Busy,
			Model:     av.Model,
			Thinking:  av.Thinking,
			Activity:  clip(agentActivity(av), 160),
		}
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
	switch entry.Pane {
	case string(protocol.Austin), string(protocol.Tony):
		return entry.Pane
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
