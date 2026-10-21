package gotui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/terminal"
	"github.com/gdamore/tcell/v3"
	ui "github.com/metaspartan/gotui/v5"
)

type viewMode int

const (
	viewSplit viewMode = iota
	viewTimeline
)

// sidebarPanel identifies one expandable dashboard panel. Only one panel is
// expanded at a time; the other two shrink to their title bar.
type sidebarPanel int

const (
	panelNone sidebarPanel = iota
	panelWorkflow
	panelAustin
	panelTony
)

// App is one live projection of a Duo session. It owns UI state only; every
// workflow fact is read from session.View.
type App struct {
	svc  Session
	live session.View

	theme    theme
	backend  *ui.Backend
	screen   tcell.Screen
	layout   layout
	width    int
	height   int
	now      time.Time
	frame    int
	viewMode viewMode

	sidebarFocus sidebarPanel

	background *background
	modalBg    *background
	header     *headerBar
	workflow   *workflowPanel
	austinCard *agentCard
	tonyCard   *agentCard

	timeline *transcript
	austin   *transcript
	tony     *transcript
	duo      *transcript
	composer *composer
	footer   *footerBar

	entries       []entry
	hadHistory    bool
	nextSeq       int
	showTimestamp bool
	sidebar       bool
	status        string
	statusErr     bool
	warnedNotices map[string]bool

	subEvents    <-chan events.Event
	stop         chan struct{}
	stopOnce     sync.Once
	history      []string
	historyIdx   int
	historyDraft string
	slashCursor  int
	slashHidden  bool

	modelTarget  protocol.AgentID
	currentModel map[protocol.AgentID]string
	currentThink map[protocol.AgentID]string

	overlay overlayKind
	picker  *modelPicker
	help    *helpOverlay
	details *detailOverlay
	confirm *confirmOverlay
	slash   *slashMenu

	asyncCh chan asyncResult

	native          protocol.AgentID
	nativeDetachBuf []byte
	nativeInput     nativeInputAssembler
	nativeTTY       *terminal.TTY
	openTTY         func() (*terminal.TTY, error)
	ctx             context.Context

	pasting     bool
	mousePane   *transcript
	focusPane   *transcript
	pendingLink *linkTarget

	clipboard     func(string) error
	openURL       func(string) error
	openFile      func(string) error
	toast         *toastPanel
	toastDuration time.Duration
}

type asyncResult struct {
	kind    string
	agent   protocol.AgentID
	payload any
	err     error
}

func New(svc Session) *App {
	t := newTheme()
	a := &App{
		svc:           svc,
		theme:         t,
		stop:          make(chan struct{}),
		showTimestamp: true,
		sidebar:       true,
		warnedNotices: map[string]bool{},
		historyIdx:    -1,
		modelTarget:   protocol.Austin,
		currentModel:  map[protocol.AgentID]string{},
		currentThink:  map[protocol.AgentID]string{},
		asyncCh:       make(chan asyncResult, 16),
		history:       loadComposerHistory(),
		openURL:       openURLDefault,
		openFile:      openFileDefault,
		toastDuration: 2 * time.Second,
	}
	a.background = newBackground(t)
	a.modalBg = newBackground(t)
	a.header = newHeader(t)
	a.workflow = newWorkflow(t)
	a.austinCard = newAgentCard(t, protocol.Austin)
	a.tonyCard = newAgentCard(t, protocol.Tony)
	a.timeline = newTranscript(t)
	a.timeline.Title = " CONVERSATION "
	a.austin = newTranscript(t)
	a.austin.Title = " AUSTIN "
	a.tony = newTranscript(t)
	a.tony.Title = " TONY "
	a.duo = newTranscript(t)
	a.duo.Title = " DUO / HUMAN "
	a.duo.SetEmptyText("Human commands, notices and system activity appear here.")
	a.composer = newComposer(t)
	a.footer = newFooter(t)
	a.picker = newModelPicker(t)
	a.help = newHelp(t)
	a.details = newDetails(t)
	a.slash = newSlashMenu(t)
	a.confirm = &confirmOverlay{Block: newModalBlock(t), theme: t, active: 0, choice2: "Cancel", choice1: "Confirm"}
	if svc != nil {
		a.live = svc.View()
		a.applyLanguage()
		before := len(a.entries)
		for _, item := range svc.History() {
			a.restoreEntry(item)
		}
		a.hadHistory = len(a.entries) > before
	}
	return a
}

// Notices shows driver diagnostics that existed before the interface. They are
// deduplicated because a driver can report the same warning on every restart.
func (a *App) Notices(notices []string) {
	a.addNotices(notices)
}

func (a *App) addNotices(notices []string) {
	for _, notice := range notices {
		notice = strings.TrimSpace(notice)
		if notice == "" || a.warnedNotices[notice] {
			continue
		}
		a.warnedNotices[notice] = true
		a.addEntry(protocol.Duo, "", notice, false, false)
	}
}

func (a *App) setStatus(text string, isError bool) {
	a.status, a.statusErr = text, isError
}

func (a *App) showCopiedToast(chars int) {
	if a.toast == nil {
		a.toast = newToast(a.theme)
	}
	text := fmt.Sprintf("%d characters", chars)
	if a.live.Language == "zh" {
		text = fmt.Sprintf("%d 个字符", chars)
	}
	duration := a.toastDuration
	if duration <= 0 {
		duration = 2 * time.Second
	}
	a.toast.show(text, time.Now().Add(duration))
}

func (a *App) refreshView() {
	if a.svc != nil {
		a.live = a.svc.View()
	}
	a.applyLanguage()
}

// applyLanguage makes the session's /language setting visible in the dashboard.
// Widgets share theme.Lang by pointer, so this updates every constructed panel
// without rebuilding the layout. The help overlay caches wrapped lines, so its
// width is invalidated to force a rebuild in the new language.
func (a *App) applyLanguage() {
	lang := "en"
	if a.live.Language == "zh" {
		lang = "zh"
	}
	if a.theme.Lang == nil {
		a.theme.Lang = &lang
	}
	if *a.theme.Lang == lang {
		return
	}
	*a.theme.Lang = lang
	a.help.width = 0
	a.details.width = 0
}

func (a *App) refreshNotices() {
	if a.svc != nil {
		a.addNotices(a.live.Notices)
	}
}

func (a *App) busy() bool {
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		if runtimeActive(a.live.Runtime(id)) {
			return true
		}
	}
	return false
}

func (a *App) Run(ctx context.Context) error {
	if a.svc == nil {
		return fmt.Errorf("terminal UI has no session")
	}
	backend, err := ui.NewBackend(nil)
	if err != nil {
		return fmt.Errorf("open terminal UI: %w", err)
	}
	return a.runWithBackend(ctx, backend)
}

// runWithBackend is the testable half of Run. The backend must already be
// initialized (real or SimulationScreen).
func (a *App) runWithBackend(ctx context.Context, backend *ui.Backend) error {
	if a.svc == nil {
		return fmt.Errorf("terminal UI has no session")
	}
	a.backend = backend
	a.screen = backend.Screen
	a.ctx = ctx
	if a.screen == nil {
		return fmt.Errorf("terminal UI has no screen")
	}
	defer backend.Close()
	a.screen.SetStyle(tcell.StyleDefault.Foreground(a.theme.Fg).Background(a.theme.Bg))
	a.screen.EnableMouse()
	a.screen.EnablePaste()
	a.screen.EnableFocus()
	a.screen.HideCursor()

	sub, cancel := a.svc.Subscribe(256)
	a.subEvents = sub.Events
	defer cancel()
	if sub.View.SessionID != "" || len(sub.View.Worktrees.Repository) > 0 {
		a.live = sub.View
	}
	a.applyLanguage()
	a.addNotices(sub.View.Notices)
	// On resume the header and the restored conversation are the context; a
	// startup banner at the bottom would push the human's own request out of
	// the Duo pane. Pre-run driver notices do not count as conversation.
	if !a.hadHistory {
		a.addLabeled(protocol.Duo, directionLabel(protocol.Duo, ""), a.startupMessage(), false, false)
	}
	a.announceRunningAgents()
	a.resize()
	defer func() {
		if a.native != "" {
			a.leaveNative()
		}
	}()

	tick := time.NewTicker(180 * time.Millisecond)
	defer tick.Stop()
	a.draw()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-a.stop:
			return nil
		case ev, ok := <-sub.Events:
			if !ok {
				return nil
			}
			a.refreshView()
			a.route(ev)
			a.refreshNotices()
			a.draw()
		case ev := <-a.screen.EventQ():
			a.handleEvent(ev)
			if a.isStopped() {
				return nil
			}
			a.draw()
		case res := <-a.asyncCh:
			a.handleAsync(res)
			a.draw()
		case <-tick.C:
			if a.busy() || a.toast != nil {
				a.frame++
				a.refreshView()
				a.draw()
			}
		}
	}
}

func (a *App) isStopped() bool {
	select {
	case <-a.stop:
		return true
	default:
		return false
	}
}

func (a *App) stopApp() {
	a.stopOnce.Do(func() { close(a.stop) })
}

func (a *App) resize() {
	if a.screen == nil {
		return
	}
	w, h := a.screen.Size()
	if w <= 0 || h <= 0 {
		w, h = 80, 24
	}
	a.width, a.height = w, h
	a.layout = a.computeLayout(w, h)
	if a.svc != nil {
		_ = a.svc.Resize(w, h)
	}
	a.timeline.Rebuild(true)
	a.austin.Rebuild(true)
	a.tony.Rebuild(true)
	a.duo.Rebuild(true)
}

func (a *App) draw() {
	if a.screen == nil || a.backend == nil {
		return
	}
	w, h := a.screen.Size()
	if w != a.width || h != a.height {
		a.resize()
	}
	if w <= 0 || h <= 0 {
		return
	}
	a.now = time.Now()
	if a.toast != nil && (a.toast.until.IsZero() || !a.now.Before(a.toast.until)) {
		a.toast = nil
	}
	a.layout = a.computeLayout(w, h)
	a.updateWidgets()
	// Scroll the composer before drawing so the frame and the terminal cursor
	// come from the same visual row. Set the cursor before Render: gotui flushes
	// it during Show(), so updating it afterwards leaves the terminal one frame
	// behind the text.
	x, y := a.composer.prepare()
	if a.overlay == overlayNone {
		a.screen.ShowCursor(x, y)
	} else {
		a.screen.HideCursor()
	}
	items := make([]ui.Drawable, 0, 14)
	items = append(items, a.background, a.header)
	if a.layout.sidebarVisible {
		items = append(items, a.workflow, a.austinCard, a.tonyCard)
	}
	if a.layout.split {
		items = append(items, a.austin, a.tony, a.duo)
	} else {
		items = append(items, a.timeline)
	}
	items = append(items, a.composer, a.footer)
	if a.overlay == overlayNone && a.toast != nil {
		a.toast.place(a.layout)
		items = append(items, a.toast)
	}
	if a.overlay == overlayNone && a.slashOpen() {
		items = append(items, a.slash)
	}
	if a.overlay != overlayNone {
		a.modalBg.SetRect(0, a.layout.main.Min.Y, a.width, a.layout.main.Max.Y)
		items = append(items, a.modalBg)
		switch a.overlay {
		case overlayPicker:
			items = append(items, a.picker)
		case overlayHelp:
			items = append(items, a.help)
		case overlayDetails:
			items = append(items, a.details)
		case overlayConfirm:
			items = append(items, a.confirm)
		}
	}
	a.backend.Render(items...)
}

func (a *App) updateWidgets() {
	l := a.layout
	a.background.SetRect(0, 0, l.width, l.height)
	a.header.SetRect(l.header.Min.X, l.header.Min.Y, l.header.Max.X, l.header.Max.Y)
	a.header.version = a.live.Version
	a.header.repo = a.live.Worktrees.Repository
	if a.header.repo == "" {
		a.header.repo = a.live.Worktrees.Root
	}
	a.header.branch = a.live.Worktrees.BaseBranch
	a.header.session = a.live.SessionID
	a.header.mode = a.live.EffectiveMode().Display()
	a.header.phase = string(a.live.Phase())
	a.header.online, a.header.total = onlineAgents(a.live)
	a.header.view = a.viewMode

	a.workflow.SetRect(l.workflow.Min.X, l.workflow.Min.Y, l.workflow.Max.X, l.workflow.Max.Y)
	a.workflow.view = a.live
	a.workflow.compact = l.height < 32
	a.workflow.expanded = a.sidebarFocus == panelWorkflow
	a.austinCard.SetRect(l.austinCard.Min.X, l.austinCard.Min.Y, l.austinCard.Max.X, l.austinCard.Max.Y)
	a.austinCard.av = a.live.Agent(protocol.Austin)
	a.austinCard.model = a.currentModel[protocol.Austin]
	a.austinCard.thinking = a.currentThink[protocol.Austin]
	a.austinCard.frame = a.frame
	a.austinCard.now = a.now
	a.austinCard.compact = l.austinCard.Dy() < 10
	a.austinCard.expanded = a.sidebarFocus == panelAustin
	a.tonyCard.SetRect(l.tonyCard.Min.X, l.tonyCard.Min.Y, l.tonyCard.Max.X, l.tonyCard.Max.Y)
	a.tonyCard.av = a.live.Agent(protocol.Tony)
	a.tonyCard.model = a.currentModel[protocol.Tony]
	a.tonyCard.thinking = a.currentThink[protocol.Tony]
	a.tonyCard.frame = a.frame
	a.tonyCard.now = a.now
	a.tonyCard.compact = l.tonyCard.Dy() < 10
	a.tonyCard.expanded = a.sidebarFocus == panelTony

	roots := map[string][]string{
		string(protocol.Austin): linkRoots(a.live.Worktrees, string(protocol.Austin)),
		string(protocol.Tony):   linkRoots(a.live.Worktrees, string(protocol.Tony)),
		string(protocol.Duo):    linkRoots(a.live.Worktrees, string(protocol.Duo)),
	}
	a.timeline.SetRect(l.main.Min.X, l.main.Min.Y, l.main.Max.X, l.main.Max.Y)
	a.timeline.Title = " " + a.theme.tr("CONVERSATION") + " "
	a.timeline.SetPathRoots(roots)
	a.timeline.SetEntries(a.entries, a.showTimestamp)
	a.austin.SetRect(l.austin.Min.X, l.austin.Min.Y, l.austin.Max.X, l.austin.Max.Y)
	a.austin.SetPathRoots(roots)
	a.austin.SetEntries(a.filterEntries(string(protocol.Austin)), a.showTimestamp)
	a.tony.SetRect(l.tony.Min.X, l.tony.Min.Y, l.tony.Max.X, l.tony.Max.Y)
	a.tony.SetPathRoots(roots)
	a.tony.SetEntries(a.filterEntries(string(protocol.Tony)), a.showTimestamp)
	a.duo.SetRect(l.duo.Min.X, l.duo.Min.Y, l.duo.Max.X, l.duo.Max.Y)
	a.duo.Title = " " + a.theme.tr("DUO / HUMAN") + " "
	a.duo.SetPathRoots(roots)
	a.duo.SetEntries(a.filterEntries(string(protocol.Duo)), a.showTimestamp)

	a.composer.SetRect(l.composer.Min.X, l.composer.Min.Y, l.composer.Max.X, l.composer.Max.Y)
	a.footer.SetRect(l.footer.Min.X, l.footer.Min.Y, l.footer.Max.X, l.footer.Max.Y)
	a.footer.status, a.footer.isError = a.status, a.statusErr
	if a.overlay == overlayNone {
		a.footer.hint = a.theme.tr("Enter send · Ctrl+Enter newline · Alt+M models · Ctrl+A/T native · F1 help · F2 view · Ctrl+Q quit")
		if l.width < 100 {
			a.footer.hint = a.theme.tr("Enter send · Alt+M models · F1 help · F2 view · Ctrl+Q quit")
		}
		if l.width < 80 {
			a.footer.hint = a.theme.tr("Enter send · F1 help · F2 view · Ctrl+Q quit")
		}
		a.footer.right = ""
	} else {
		a.footer.hint = ""
		a.footer.right = ""
	}
	a.picker.place(l)
	a.help.place(l)
	a.details.place(l)
	a.confirm.place(l)
	a.slash.commands = a.matchingCommands()
	if len(a.slash.commands) == 0 {
		a.slash.cursor = 0
	} else if a.slashCursor >= len(a.slash.commands) {
		a.slashCursor = len(a.slash.commands) - 1
	}
	a.slash.cursor = a.slashCursor
	a.slash.place(l, a.composer)
}

func onlineAgents(v session.View) (int, int) {
	online, total := 0, 0
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		av := v.Agent(id)
		if !av.Present {
			continue
		}
		total++
		if av.Connected || av.Process == agent.ProcessRunning {
			online++
		}
	}
	return online, total
}

func (a *App) startupMessage() string {
	set := a.live.Worktrees
	repo := set.Repository
	if repo == "" {
		repo = set.Root
	}
	sessionID := set.Session
	if sessionID == "" {
		sessionID = a.live.SessionID
	}
	where := fmt.Sprintf("\n%s: %s (%s %s) · %s: %s", a.theme.tr("Repository"), repo, a.theme.tr("branch"), set.BaseBranch, a.theme.tr("Session"), sessionID)
	mode := a.live.EffectiveMode()
	switch mode {
	case project.ModeFast:
		return "Duo " + a.live.Version + " " + a.theme.tr("ready (FAST mode). Type a task and press Enter; Austin drives while Tony independently verifies before Duo delivers the result.") + where
	case project.ModeStep:
		return "Duo " + a.live.Version + " " + a.theme.tr("ready (STEP mode). Type the complex task and press Enter; both agents plan a queue tagged [fast] or [goal], then Duo lands each approved step.") + where
	default:
		return "Duo " + a.live.Version + " " + a.theme.tr("ready (GOAL mode). Type a task and press Enter; Duo sends it to both Austin and Tony to co-design the shared plan.") + where
	}
}

func (a *App) announceRunningAgents() {
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		av := a.live.Agent(id)
		if av.Manifest != nil && av.Manifest.Capabilities.SelfReports {
			continue
		}
		if av.Process != agent.ProcessRunning {
			continue
		}
		a.addLabeled(id, directionLabel(id, ""), fmt.Sprintf("%s %s", id, a.theme.tr("connected")), false, false)
	}
}

func (a *App) filterEntries(pane string) []entry {
	out := make([]entry, 0, len(a.entries)/2)
	for _, e := range a.entries {
		if e.pane == pane {
			out = append(out, e)
		}
	}
	return out
}

func (a *App) activeTranscript() *transcript {
	if a.layout.split && a.focusPane != nil {
		return a.focusPane
	}
	if a.layout.split {
		return a.austin
	}
	return a.timeline
}

func (a *App) toggleView() {
	if a.viewMode == viewSplit {
		a.viewMode = viewTimeline
		return
	}
	a.viewMode = viewSplit
	// The tab pane already shows which view is active. Only explain a
	// fallback, where the split view cannot actually be drawn.
	if !a.layout.split {
		a.setStatus(a.theme.tr("split view needs a wider terminal"), false)
	}
}

func (a *App) openConfirm(title, text string, onYes func()) {
	a.openConfirmChoices(title, text, "Cancel", "Confirm", onYes)
}

func (a *App) openConfirmChoices(title, text, no, yes string, onYes func()) {
	a.confirm = &confirmOverlay{
		Block:   newModalBlock(a.theme),
		theme:   a.theme,
		title:   title,
		text:    text,
		onYes:   onYes,
		active:  0,
		choice2: no,
		choice1: yes,
	}
	a.overlay = overlayConfirm
}

// openModeResumePrompt asks a question that a bare mode switch cannot answer:
// should the agents be woken now, or should Duo wait for the human's next task?
// The safe default is "Not now".
func (a *App) openModeResumePrompt(target project.Mode) {
	title := "Mode switched to " + target.Display()
	text := "No resume message was sent to Austin and Tony. Resume their work in this mode now? Choose Not now if you only want to type the next prompt first."
	if a.live.Language == "zh" {
		title = "已切换到 " + target.Display() + " 模式"
		text = "尚未向 Austin 和 Tony 发送恢复消息。现在让他们按新模式恢复工作吗？如果只想先输入下一条提示词，请选择“暂不恢复”。"
	}
	a.openConfirmChoices(title, text, "Not now", "Resume work", func() {
		if a.svc == nil {
			return
		}
		if err := a.svc.Do(context.Background(), session.ResumeModeWork{}); err != nil {
			a.setStatus(err.Error(), true)
			return
		}
		if a.live.Language == "zh" {
			a.setStatus("已在 "+target.Display()+" 模式恢复 Austin 和 Tony 的工作", false)
		} else {
			a.setStatus("Austin and Tony resumed in "+target.Display()+" mode", false)
		}
	})
}

func (a *App) closeOverlay() {
	a.overlay = overlayNone
	a.screen.HideCursor()
}

func (a *App) requestQuit() {
	if a.live.Phase() == project.PhaseDone {
		a.stopApp()
		return
	}
	a.openConfirm(a.theme.tr("Quit Duo?"), a.theme.tr("The session is not finished. It will be preserved and can be resumed with `duo resume`."), func() {
		a.stopApp()
	})
}

func (a *App) addAsync(result asyncResult) {
	select {
	case a.asyncCh <- result:
	default:
	}
}

func (a *App) handleAsync(res asyncResult) {
	switch res.kind {
	case "models":
		a.picker.loaded(res.agent, res.payload, res.err)
	case "details":
		a.details.loaded(res.payload, res.err)
	}
}

func (a *App) restoreEntry(item sessionstore.TUIEntry) {
	pane := item.Pane
	if pane != string(protocol.Austin) && pane != string(protocol.Tony) && pane != string(protocol.Duo) {
		return
	}
	label := item.Label
	switch label {
	case string(protocol.Austin), string(protocol.Tony), string(protocol.Duo):
		label = directionLabel(protocol.AgentID(label), "")
	}
	a.appendEntry(entry{
		at:      item.Time,
		pane:    pane,
		text:    item.Text,
		err:     item.Error,
		warn:    item.Warning,
		label:   label,
		summary: protocol.IsSummaryReport(item.Text),
	})
}

func (a *App) addEntry(agent protocol.AgentID, peer protocol.AgentID, text string, isError, isWarning bool) {
	label := directionLabel(agent, peer)
	item := entry{at: time.Now(), pane: string(agent), text: text, err: isError, warn: isWarning, label: label}
	a.appendEntry(item)
	if a.svc != nil {
		a.svc.RecordEntry(sessionstore.TUIEntry{Time: item.at, Pane: string(agent), Text: text, Error: isError, Warning: isWarning, Label: label})
	}
}

func (a *App) appendEntry(item entry) {
	a.nextSeq++
	item.seq = a.nextSeq
	if item.at.IsZero() {
		item.at = time.Now()
	}
	if item.label == "" {
		item.label = directionLabel(protocol.AgentID(item.pane), "")
	}
	a.entries = append(a.entries, item)
	if len(a.entries) > 400 {
		a.entries = a.entries[len(a.entries)-400:]
	}
}

func directionLabel(from, to protocol.AgentID) string {
	if from == "" {
		from = protocol.Duo
	}
	if to == "" {
		to = "Human"
	}
	return fmt.Sprintf("%s → %s", from, to)
}
