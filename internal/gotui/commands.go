package gotui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atfa/duo/internal/models"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
)

func (a *App) submit() {
	text := strings.TrimSpace(a.composer.ExpandedText())
	if text == "" {
		return
	}
	// A paused session only restarts through an explicit verb. Keeping the typed
	// text in the composer is deliberate: the human can resume and send it.
	if !a.isSlashCommand(text) {
		if status, blocked := a.pausedInputStatus(); blocked {
			a.setStatus(status, true)
			return
		}
	}
	a.composer.Clear()
	a.closeMention()
	a.slashHidden = false
	a.slashCursor = 0
	a.pushHistory(text)
	if a.runSlash(text) {
		return
	}
	if a.svc == nil {
		return
	}
	if err := a.svc.Do(context.Background(), session.SubmitTask{Text: text}); err != nil {
		a.setStatus(err.Error(), true)
		a.addLabeled(protocol.Duo, directionLabel(protocol.Duo, ""), "ERROR: "+err.Error(), true, false)
		return
	}
	a.refreshView()
	// The coordinator reports the task as a conversation entry for each agent it
	// reaches, so the timeline already shows who received it. Clearing the status
	// leaves the idle key hint in place instead of a line the human does not need.
	a.setStatus("", false)
}

// isSlashCommand mirrors runSlash's dispatch rule. An absolute path such as
// /usr/bin/python is a task, not a command, so a paused session must refuse it
// just like any other typed work.
func (a *App) isSlashCommand(text string) bool {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") || strings.HasPrefix(text, "//") {
		return false
	}
	fields := strings.Fields(text)
	return len(fields) > 0 && !strings.Contains(fields[0][1:], "/")
}

func (a *App) runSlash(text string) bool {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") || strings.HasPrefix(text, "//") {
		return false
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return false
	}
	cmd := strings.ToLower(fields[0])
	switch cmd {
	case "/help":
		a.openHelp()
		return true
	case "/overview":
		a.openDetails()
		return true
	case "/plan":
		a.openPlan()
		return true
	case "/search":
		a.openSearch(strings.TrimSpace(text[len(fields[0]):]))
		return true
	case "/sessions":
		a.openSessions()
		return true
	case "/cost":
		a.cost.begin(a.live)
		a.overlay = overlayCost
		return true
	case "/austin":
		a.enterNative(protocol.Austin)
		return true
	case "/tony":
		a.enterNative(protocol.Tony)
		return true
	case "/restart":
		if len(fields) != 2 {
			a.setStatus(a.theme.tr("usage: /restart austin|tony"), true)
			return true
		}
		switch strings.ToLower(fields[1]) {
		case "austin":
			a.restartAgent(protocol.Austin)
		case "tony":
			a.restartAgent(protocol.Tony)
		default:
			a.setStatus(a.theme.tr("usage: /restart austin|tony"), true)
		}
		return true
	case "/paste":
		a.pasteImageFromClipboard()
		return true
	case "/model":
		a.openPicker()
		return true
	case "/qrcode":
		rotate := len(fields) == 2 && strings.EqualFold(fields[1], "new")
		if len(fields) > 2 || (len(fields) == 2 && !rotate) {
			a.setStatus(a.theme.tr("usage: /qrcode [new]"), true)
			return true
		}
		a.openQrcode(rotate)
		return true
	case "/remote":
		if len(fields) == 1 {
			a.showRemoteStatus()
			return true
		}
		if len(fields) != 2 {
			a.setStatus(a.theme.tr("usage: /remote [on|off|status]"), true)
			return true
		}
		switch strings.ToLower(fields[1]) {
		case "on":
			a.openQrcode(false)
		case "off":
			a.remoteOff()
		case "status":
			a.showRemoteStatus()
		default:
			a.setStatus(a.theme.tr("usage: /remote [on|off|status]"), true)
		}
		return true
	case "/observer":
		a.openObserverConfig()
		return true
	case "/reports":
		a.openObserverReports()
		return true
	case "/copy":
		a.copyLatest()
		return true
	case "/preview":
		a.toggleSidebar()
		return true
	case "/timestamps":
		a.toggleTimestamps()
		return true
	case "/clear":
		if a.screen != nil {
			a.screen.Sync()
		}
		a.setStatus(a.theme.tr("screen redrawn"), false)
		return true
	case "/status":
		if a.svc == nil {
			return true
		}
		status := strings.TrimSpace(a.svc.StatusText(context.Background()))
		if status == "" {
			status = a.theme.tr("no status reported")
		}
		a.addLabeled(protocol.Duo, directionLabel(protocol.Duo, ""), status, false, false)
		a.setStatus(a.theme.tr("status reported in timeline"), false)
		return true
	case "/new":
		a.requestNewSession()
		return true
	case "/recall":
		a.requestRecall()
		return true
	case "/quit", "/exit":
		a.requestQuit()
		return true
	case "/language":
		zh := a.live.Language == "zh"
		if len(fields) == 1 {
			lang := a.live.Language
			if lang == "" {
				lang = "en"
			}
			if zh {
				a.setStatus(fmt.Sprintf("当前语言：%s（使用 /language zh 或 /language en）", lang), false)
			} else {
				a.setStatus(fmt.Sprintf("current language: %s (use /language zh or /language en)", lang), false)
			}
			return true
		}
		if len(fields) > 2 {
			if zh {
				a.setStatus("用法：/language [zh|en]", true)
			} else {
				a.setStatus("usage: /language [zh|en]", true)
			}
			return true
		}
		lang := strings.ToLower(fields[1])
		if lang != "zh" && lang != "en" {
			if zh {
				a.setStatus(fmt.Sprintf("无效语言 %q：请使用 zh 或 en", fields[1]), true)
			} else {
				a.setStatus(fmt.Sprintf("invalid language %q: use zh or en", fields[1]), true)
			}
			return true
		}
		if err := a.svc.Do(context.Background(), session.SetLanguage{Language: lang}); err != nil {
			a.setStatus(err.Error(), true)
			return true
		}
		a.refreshView()
		if lang == "zh" {
			a.setStatus(fmt.Sprintf("语言 → %s（已保存到配置）", lang), false)
		} else {
			a.setStatus(fmt.Sprintf("language → %s (saved to config)", lang), false)
		}
		return true
	case "/mode":
		if len(fields) == 1 {
			a.openModePicker()
			return true
		}
		if len(fields) != 2 {
			a.setStatus(a.theme.tr("usage: /mode [fast|goal|step]"), true)
			return true
		}
		target, err := project.ParseMode(fields[1])
		if err != nil {
			a.setStatus(err.Error(), true)
			return true
		}
		a.requestModeSwitch(target, "")
		return true
	case "/escalate":
		reason := strings.TrimSpace(strings.TrimPrefix(text, "/escalate"))
		if strings.HasPrefix(reason, ":") {
			reason = strings.TrimSpace(strings.TrimPrefix(reason, ":"))
		}
		a.requestModeSwitch(project.ModeGoal, reason)
		return true
	case "/pause":
		a.pauseSession()
		return true
	case "/unpause":
		a.unpauseSession()
		return true
	case "/continue":
		a.continueSession()
		return true
	case "/resume":
		a.resumeWork()
		return true
	case "/driver":
		a.openDriverPicker()
		return true
	default:
		if strings.Contains(cmd[1:], "/") {
			return false
		}
		a.setStatus(fmt.Sprintf(a.theme.tr("unknown command %q (type / to see commands)"), cmd), true)
		return true
	}
}

func (a *App) toggleSidebar() {
	a.sidebar = !a.sidebar
	a.setStatus(a.theme.tr(map[bool]string{true: "workflow sidebar shown", false: "workflow sidebar hidden"}[a.sidebar]), false)
}

func (a *App) toggleTimestamps() {
	a.showTimestamp = !a.showTimestamp
	a.setStatus(a.theme.tr(map[bool]string{true: "timestamps shown", false: "timestamps hidden"}[a.showTimestamp]), false)
}

// pausedInputStatus explains why a plain task cannot be sent. Pause only blocks
// new work; the verbs that change the pause state stay available above it.
func (a *App) pausedInputStatus() (string, bool) {
	switch a.live.Pause.State {
	case session.PausePausing:
		return a.theme.tr("pause in progress · /unpause cancels this request"), true
	case session.PausePaused:
		return a.theme.tr("session is paused · /continue to resume"), true
	}
	return "", false
}

// pauseSession asks Duo to hold new work. It never stops a turn: the coordinator
// waits for both agents to settle and the footer shows who is still working.
func (a *App) pauseSession() {
	if a.svc == nil {
		return
	}
	if err := a.svc.Do(context.Background(), session.Pause{}); err != nil {
		a.setStatus(err.Error(), true)
		return
	}
	a.refreshView()
	a.setStatus(a.pauseStatusText(), false)
}

func (a *App) pauseStatusText() string {
	switch a.live.Pause.State {
	case session.PausePaused:
		return a.theme.tr("paused · /continue resume · /driver switch driver")
	case session.PausePausing:
		return a.pauseProgress()
	}
	return a.theme.tr("not paused")
}

// unpauseSession cancels a request that has not completed. A fully paused
// session is resumed only by /continue, which also sends the resume prompt.
func (a *App) unpauseSession() {
	if a.svc == nil {
		return
	}
	if err := a.svc.Do(context.Background(), session.Unpause{}); err != nil {
		a.setStatus(err.Error(), true)
		return
	}
	a.refreshView()
	a.setStatus(a.theme.tr("pause cancelled"), false)
}

// continueSession is the only path back to work after a full pause. The
// coordinator sends one resume prompt that carries the workflow state.
func (a *App) continueSession() {
	if a.svc == nil {
		return
	}
	if err := a.svc.Do(context.Background(), session.Continue{}); err != nil {
		a.setStatus(err.Error(), true)
		return
	}
	a.refreshView()
	a.setStatus(a.theme.tr("work resumed"), false)
}

// resumeWork is the human's explicit opt-in after a restored session. Merely
// resuming sends nothing, so this is the only way to continue without a task.
func (a *App) resumeWork() {
	if a.svc == nil {
		return
	}
	if err := a.svc.Do(context.Background(), session.ResumeWork{}); err != nil {
		a.setStatus(err.Error(), true)
		return
	}
	a.refreshView()
	message := a.theme.tr("Austin and Tony were asked to continue")
	if phase := string(a.live.Phase()); phase != "" {
		message += " (" + phase + ")"
	}
	a.addLabeled(protocol.Duo, directionLabel(protocol.Duo, ""), message, false, false)
}

// openDriverPicker is available before the first task, while fully paused, or
// after DONE. Replacing a driver during a turn would discard its current work.
func (a *App) openDriverPicker() {
	if a.svc == nil {
		return
	}
	if !a.live.DriverChangeAllowed() {
		a.setStatus(a.theme.tr("changing a driver is only available before the first task, while fully paused, or after DONE"), true)
		return
	}
	a.closeMention()
	agent := a.drivers.agent
	current := a.live.Agent(agent).Driver
	a.drivers.begin(agent, current)
	list, err := a.svc.Drivers(context.Background())
	if err != nil {
		a.drivers.fail(err)
	} else {
		names := make([]string, 0, len(list))
		for _, choice := range list {
			names = append(names, choice.Name)
		}
		a.drivers.setNames(names)
	}
	a.drivers.setCurrent(current)
	a.overlay = overlayDrivers
}

// applyDriver changes the selected agent's driver without resuming it. The new
// process starts idle; /continue is what sends the context prompt.
func (a *App) applyDriver(keepOpen bool) {
	name, ok := a.drivers.selected()
	if !ok {
		if !keepOpen {
			a.closeOverlay()
		}
		return
	}
	if a.svc == nil {
		return
	}
	agent := a.drivers.agent
	if err := a.svc.Do(context.Background(), session.SetDriver{Agent: agent, Driver: name}); err != nil {
		a.setStatus(err.Error(), true)
		if !keepOpen {
			a.closeOverlay()
		}
		return
	}
	a.refreshView()
	a.drivers.setCurrent(name)
	a.setStatus(fmt.Sprintf(a.theme.tr("%s driver → %s (new process starts idle)"), agent, name), false)
	if !keepOpen {
		a.closeOverlay()
	}
}

// requestModeSwitch applies a slash, panel or /escalate mode change. A
// transition that would discard the current round asks first; the confirmed
// callback calls applyModeSwitch directly so the question is asked at most once.
func (a *App) requestModeSwitch(target project.Mode, reason string) {
	if target == a.live.EffectiveMode() {
		a.setStatus(fmt.Sprintf(a.theme.tr("already in %s mode"), target.Display()), false)
		return
	}
	if a.svc == nil {
		return
	}
	if loss := a.modeSwitchLoss(target); len(loss) > 0 {
		a.confirmModeSwitch(target, reason, loss)
		return
	}
	a.applyModeSwitch(target, reason)
}

// modeSwitchLoss names only what the transition really discards. A finished
// round keeps its result, and Fast → Goal escalation carries the reason and
// notes into the new Goal plan, so neither asks.
func (a *App) modeSwitchLoss(target project.Mode) []string {
	snap := a.live.Project
	from := snap.EffectiveMode()
	if target == from || snap.Phase == project.PhaseDone {
		return nil
	}
	if from == project.ModeFast && target == project.ModeGoal {
		return nil
	}
	var loss []string
	if len(snap.Steps) > 0 && !project.QueueFinished(snap.Steps) {
		landed := 0
		for _, step := range snap.Steps {
			if step.Done {
				landed++
			}
		}
		loss = append(loss, fmt.Sprintf(a.theme.tr("the Step queue (%d steps, %d landed)"), len(snap.Steps), landed))
	}
	if strings.TrimSpace(snap.Plan) != "" {
		loss = append(loss, fmt.Sprintf(a.theme.tr("the shared plan v%d"), snap.PlanVersion))
	}
	var signed []string
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		if snap.Ready[agent] {
			signed = append(signed, string(agent))
		}
	}
	if len(signed) > 0 {
		loss = append(loss, fmt.Sprintf(a.theme.tr("signatures from %s"), strings.Join(signed, ", ")))
	}
	if from == project.ModeFast && snap.Verification.Head != "" && (target == project.ModeFast || target == project.ModeStep) {
		loss = append(loss, a.theme.tr("Tony's verification result"))
	}
	return loss
}

// confirmModeSwitch keeps the current mode by default: the left button is the
// safe choice, and Esc sends no command at all.
func (a *App) confirmModeSwitch(target project.Mode, reason string, loss []string) {
	title := fmt.Sprintf(a.theme.tr("Switch to %s?"), target.Display())
	text := a.theme.tr("Switching starts a new round and discards:") +
		"\n\n• " + strings.Join(loss, "\n• ") +
		"\n\n" + a.theme.tr("Keep the current mode to preserve this round.")
	a.openConfirmChoices(title, text, "Keep current mode", "Switch mode", func() {
		a.applyModeSwitch(target, reason)
	})
}

// applyModeSwitch changes the mode quietly, then offers the explicit resume
// step. Both /mode and /escalate go through here so a mode switch never wakes
// the agents by itself.
func (a *App) applyModeSwitch(target project.Mode, reason string) {
	if a.svc == nil {
		return
	}
	if err := a.svc.Do(context.Background(), session.SwitchMode{Mode: target.String(), Reason: reason, Quiet: true}); err != nil {
		a.setStatus(err.Error(), true)
		return
	}
	a.refreshView()
	if pause := a.live.Pause.State; pause == session.PausePausing || pause == session.PausePaused {
		// The resume prompt would promise work that the pause gate holds. Offer
		// the pause verbs instead and let /continue deliver the new mode notice.
		a.setStatus(fmt.Sprintf(a.theme.tr("mode → %s; pause is still active — use /continue or /unpause"), target.Display()), false)
		return
	}
	a.setStatus(fmt.Sprintf(a.theme.tr("mode → %s; agents not resumed yet"), target.Display()), false)
	a.openModeResumePrompt(target)
}

func (a *App) openHelp() {
	a.closeMention()
	a.help.offset = 0
	a.overlay = overlayHelp
}

func (a *App) openObserverConfig() {
	if a.svc == nil {
		return
	}
	a.closeMention()
	a.refreshView()
	a.observer.begin(a.live.Observer)
	a.overlay = overlayObserver
}

func (a *App) openObserverReports() {
	a.closeMention()
	a.refreshView()
	if strings.TrimSpace(a.live.Observer.Model) == "" {
		a.openObserverConfig()
		return
	}
	a.observerReports.begin(a.live.Observer)
	a.overlay = overlayObserverReports
	a.requestObserverReport()
}

// requestObserverReport asks the session for a fresh status report. The session
// returns immediately and the panel redraws from the view, so a slow model never
// blocks the interface.
func (a *App) requestObserverReport() {
	if a.svc == nil {
		return
	}
	if !a.live.Observer.Enabled || !a.live.Observer.HasKey {
		return
	}
	a.svc.RequestObserverReport()
}

// requestObserverAnswer sends the question the human typed. The session trims
// and normalizes it, so the panel hands over the raw line; the guard here only
// keeps an empty send from touching the session at all.
func (a *App) requestObserverAnswer(question string) {
	if a.svc == nil {
		return
	}
	if !a.live.Observer.Enabled || !a.live.Observer.HasKey {
		return
	}
	a.svc.RequestObserverAnswer(question)
}

// openPlan shows the read-only plan and Step queue overlay. It reads the live
// snapshot once when opened and refreshes lazily while the overlay is visible.
func (a *App) openPlan() {
	a.closeMention()
	a.plan.begin(a.live)
	a.overlay = overlayPlan
}

func (a *App) openDetails() {
	a.closeMention()
	a.details.begin()
	a.overlay = overlayDetails
	if a.svc == nil {
		a.details.loaded(detailPayload{}, nil)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		status := a.svc.StatusText(ctx)
		changes := a.svc.Changes(ctx)
		a.addAsync(asyncResult{kind: "details", payload: detailPayload{status: status, changes: changes, usage: a.live.Usage}})
	}()
}

func (a *App) copyLatest() {
	text, ok := a.lastAustinToHuman()
	if !ok {
		a.setStatus(a.theme.tr("Austin has not sent a message to the human yet"), false)
		return
	}
	if err := a.copyToClipboard(text); err != nil {
		a.setStatus(fmt.Sprintf(a.theme.tr("copy failed: %s"), err.Error()), true)
		return
	}
	a.showCopiedToast(len([]rune(text)))
}

// openModePicker shows the FAST/GOAL/STEP panel. It is the keyboard, slash and
// header-badge entry point; every path applies through requestModeSwitch.
func (a *App) openModePicker() {
	if a.svc == nil {
		return
	}
	a.closeMention()
	a.modePicker.begin(a.live.EffectiveMode())
	a.overlay = overlayModePicker
}

func (a *App) openPicker() {
	if a.svc == nil {
		return
	}
	a.closeMention()
	a.modelTarget = a.picker.agent
	if a.modelTarget == "" {
		a.modelTarget = protocol.Austin
	}
	current := a.modelForAgent(a.modelTarget)
	thinking := a.currentThink[a.modelTarget]
	if thinking == "" {
		thinking = a.live.Agent(a.modelTarget).Thinking
	}
	if a.picker.begin(a.modelTarget, current, thinking) {
		a.startPickerLoad(a.modelTarget)
	}
	a.picker.selectCurrent()
	a.overlay = overlayPicker
}

func (a *App) modelForAgent(agent protocol.AgentID) string {
	av := a.live.Agent(agent)
	m := a.currentModel[agent]
	if m == "" {
		m = av.Model
	}
	if m != "" {
		if av.Manifest.Bare() && strings.Contains(m, "/") {
			m = m[strings.LastIndex(m, "/")+1:]
		}
		return m
	}
	if av.Manifest != nil {
		return models.Default(av.Manifest)
	}
	return ""
}

func (a *App) startPickerLoad(agent protocol.AgentID) {
	a.picker.beginLoad()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		list, err := a.svc.Models(ctx, agent)
		a.addAsync(asyncResult{kind: "models", agent: agent, payload: list, err: err})
	}()
}

func (a *App) ensurePickerCatalog() {
	a.modelTarget = a.picker.agent
	a.picker.current = a.modelForAgent(a.picker.agent)
	a.picker.thinking = a.currentThink[a.picker.agent]
	if a.picker.thinking == "" {
		a.picker.thinking = a.live.Agent(a.picker.agent).Thinking
	}
	a.picker.selectCurrent()
	if !a.picker.loadedFlag && !a.picker.loading {
		a.startPickerLoad(a.picker.agent)
	}
}

func (a *App) applyPickerModel(keepOpen bool) {
	model, ok := a.picker.selected()
	if !ok {
		if !keepOpen {
			a.closeOverlay()
		}
		return
	}
	agent := a.picker.agent
	if err := a.svc.Do(context.Background(), session.SetModel{Agent: agent, Provider: model.Provider, Model: model.ID}); err != nil {
		a.setStatus(err.Error(), true)
		if !keepOpen {
			a.closeOverlay()
		}
		return
	}
	a.currentModel[agent] = model.Reference()
	a.picker.current = model.Reference()
	a.modelTarget = agent
	a.setStatus(fmt.Sprintf(a.theme.tr("%s model → %s"), agent, model.Reference()), false)
	a.refreshView()
	if !keepOpen {
		a.closeOverlay()
	}
}

func (a *App) cycleThinking() {
	agent := a.modelTarget
	if a.overlay == overlayPicker {
		agent = a.picker.agent
	}
	if err := a.svc.Do(context.Background(), session.CycleThinking{Agent: agent}); err != nil {
		a.setStatus(err.Error(), true)
		return
	}
	// The level shown by the cards and picker comes from the agent's confirmed
	// thinking_state event. Reading View here would show the level being left
	// behind on a live bridge, which reports asynchronously.
	a.refreshView()
}

func (a *App) restartAgent(agent protocol.AgentID) {
	if a.svc == nil {
		return
	}
	if err := a.svc.Do(context.Background(), session.RestartAgent{Agent: agent}); err != nil {
		a.setStatus(err.Error(), true)
		a.addLabeled(protocol.Duo, directionLabel(protocol.Duo, ""), "ERROR: "+err.Error(), true, false)
		return
	}
	a.refreshView()
	a.setStatus(fmt.Sprintf(a.theme.tr("%s restarted"), agent), false)
}
