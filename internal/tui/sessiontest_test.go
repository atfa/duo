package tui

import (
	"context"
	"io"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/models"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
)

// fakeSession is a Session a test drives: it reports the View the test installs and
// records the commands the interface issues, so a test can assert what the
// interface asked for without a running session.
type fakeSession struct {
	view      session.View
	history   []sessionstore.TUIEntry
	models    []models.Model
	modelsErr error
	changes   []string
	status    string
	notes     []string
	entries   []sessionstore.TUIEntry

	commands []session.Command
	attach   map[protocol.AgentID][]byte
	writes   map[protocol.AgentID][][]byte
	detached []protocol.AgentID
	resizes  [][2]int
	err      error
}

func (f *fakeSession) View() session.View                { return f.view }
func (f *fakeSession) History() []sessionstore.TUIEntry  { return f.history }
func (f *fakeSession) StatusText(context.Context) string { return f.status }
func (f *fakeSession) Changes(context.Context) []string  { return f.changes }
func (f *fakeSession) Notices() []string                 { return f.notes }

func (f *fakeSession) Subscribe(int) (session.Subscription, func()) {
	return session.Subscription{View: f.view}, func() {}
}

func (f *fakeSession) Do(_ context.Context, cmd session.Command) error {
	if f.err != nil {
		return f.err
	}
	f.commands = append(f.commands, cmd)
	return nil
}

func (f *fakeSession) Models(context.Context, protocol.AgentID) ([]models.Model, error) {
	return f.models, f.modelsErr
}

func (f *fakeSession) RecordEntry(e sessionstore.TUIEntry) { f.entries = append(f.entries, e) }

func (f *fakeSession) Attach(a protocol.AgentID, _ io.Writer) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.attach[a], nil
}

func (f *fakeSession) Detach(a protocol.AgentID) { f.detached = append(f.detached, a) }

func (f *fakeSession) Write(a protocol.AgentID, data []byte) error {
	if f.writes == nil {
		f.writes = map[protocol.AgentID][][]byte{}
	}
	f.writes[a] = append(f.writes[a], data)
	return nil
}

func (f *fakeSession) Resize(cols, rows int) error {
	f.resizes = append(f.resizes, [2]int{cols, rows})
	return nil
}

// issued reports whether the interface asked for a command of this type.
func (f *fakeSession) issued(kind string) bool {
	for _, c := range f.commands {
		if c.Kind() == kind {
			return true
		}
	}
	return false
}

// The interface no longer holds any part of the workflow, so its tests install a
// projection instead of reaching into a tracker, an agent manager or a server.
// These helpers are the test-side equivalent of what a session reports.

// previewApp is the fixture the preview tests used before the interface stopped
// holding a tracker: a real tracker whose state is handed to the projection before
// each render, the way a session reports it.
type previewApp struct {
	*App
	tr *harness.Tracker
}

func newPreview(w, h int) *previewApp {
	return &previewApp{App: testApp(w, h), tr: harness.NewTracker()}
}

// setDriver shadows the embedded App's two-argument form: a preview test names the
// driver, not the agent it belongs to.
func (p *previewApp) setDriver(d agent.Driver) { p.App.setDriver(d.Agent(), d) }

func (p *previewApp) sync(id protocol.AgentID) { p.setRuntime(id, p.tr.Snapshot(id)) }

func (p *previewApp) Handle(id protocol.AgentID, a protocol.ActivityType) {
	p.tr.Handle(id, a)
	p.sync(id)
}

func (p *previewApp) Note(id protocol.AgentID, a protocol.ActivityType, name, detail string) {
	p.tr.Note(id, a, name, detail)
	p.sync(id)
}

func (p *previewApp) NoteError(id protocol.AgentID, msg string) {
	p.tr.NoteError(id, msg)
	p.sync(id)
}

func (p *previewApp) UpdateContext(id protocol.AgentID, tokens, window int) {
	p.tr.UpdateContext(id, tokens, window)
	p.sync(id)
}

func (p *previewApp) UpdateRate(id protocol.AgentID, speed float64) {
	p.tr.UpdateRate(id, speed)
	p.sync(id)
}

func (p *previewApp) Snapshot(id protocol.AgentID) harness.AgentRuntime {
	p.sync(id)
	return p.tr.Snapshot(id)
}

// newAppWithHistory builds the interface and restores the conversation a resumed
// session hands over, exactly as New does.
func newAppWithHistory(history []sessionstore.TUIEntry) *App {
	a := New(nil)
	for _, item := range history {
		a.restoreEntry(item)
	}
	return a
}

// setProject installs a workflow state as the interface's projection.
func (a *App) setProject(st *project.State) {
	a.live.Project = st.Snapshot()
	a.live.Mode = a.live.Project.EffectiveMode()
}

// putAgent edits one agent's projection.
func (a *App) putAgent(id protocol.AgentID, mutate func(*session.AgentView)) {
	if a.live.Agents == nil {
		a.live.Agents = make(map[protocol.AgentID]session.AgentView)
	}
	av := a.live.Agents[id]
	mutate(&av)
	a.live.Agents[id] = av
}

// setRuntime installs an agent's runtime activity.
func (a *App) setRuntime(id protocol.AgentID, rt harness.AgentRuntime) {
	a.putAgent(id, func(av *session.AgentView) { av.Present = true; av.Runtime = rt })
}

// setProcess installs an agent's process state.
func (a *App) setProcess(id protocol.AgentID, st agent.ProcessState) {
	a.putAgent(id, func(av *session.AgentView) { av.Present = true; av.Process = st })
}

// setConnected installs an agent's bridge presence.
func (a *App) setConnected(id protocol.AgentID, on bool) {
	a.putAgent(id, func(av *session.AgentView) { av.Connected = on })
}

// setDriver installs everything a driver reports about an agent.
func (a *App) setDriver(id protocol.AgentID, d agent.Driver) {
	a.putAgent(id, func(av *session.AgentView) {
		av.Present = true
		av.Driver = d.DriverType()
		av.Process = d.State()
		av.Model = d.Model()
		av.Manifest = d.Manifest()
		av.Capabilities = d.Capabilities()
	})
}
