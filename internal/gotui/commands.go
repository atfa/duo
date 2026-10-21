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
	text := strings.TrimSpace(a.composer.Text())
	if text == "" {
		return
	}
	a.composer.Clear()
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
	status := "sent to Austin"
	if a.live.EffectiveMode() != project.ModeFast {
		status = "sent to Austin and Tony"
	}
	a.setStatus(status, false)
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
	case "/model":
		a.openPicker()
		return true
	case "/copy":
		a.copyLatest()
		return true
	case "/preview":
		a.sidebar = !a.sidebar
		a.setStatus(map[bool]string{true: "workflow sidebar shown", false: "workflow sidebar hidden"}[a.sidebar], false)
		return true
	case "/timestamps":
		a.showTimestamp = !a.showTimestamp
		a.setStatus(map[bool]string{true: "timestamps shown", false: "timestamps hidden"}[a.showTimestamp], false)
		return true
	case "/clear":
		if a.screen != nil {
			a.screen.Sync()
		}
		a.setStatus("screen redrawn", false)
		return true
	case "/status":
		if a.svc == nil {
			return true
		}
		status := strings.TrimSpace(a.svc.StatusText(context.Background()))
		if status == "" {
			status = "no status reported"
		}
		a.addLabeled(protocol.Duo, directionLabel(protocol.Duo, ""), status, false, false)
		a.setStatus("status reported in timeline", false)
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
			mode := a.live.EffectiveMode().Display()
			a.setStatus(fmt.Sprintf("current mode: %s (switch with /mode fast|goal|step)", mode), false)
			return true
		}
		if len(fields) != 2 {
			a.setStatus("usage: /mode [fast|goal|step]", true)
			return true
		}
		target, err := project.ParseMode(fields[1])
		if err != nil {
			a.setStatus(err.Error(), true)
			return true
		}
		a.switchModeFromComposer(target, "")
		return true
	case "/escalate":
		reason := strings.TrimSpace(strings.TrimPrefix(text, "/escalate"))
		if strings.HasPrefix(reason, ":") {
			reason = strings.TrimSpace(strings.TrimPrefix(reason, ":"))
		}
		a.switchModeFromComposer(project.ModeGoal, reason)
		return true
	default:
		if strings.Contains(cmd[1:], "/") {
			return false
		}
		a.setStatus(fmt.Sprintf("unknown command %q (type / to see commands)", cmd), true)
		return true
	}
}

// switchModeFromComposer applies a slash-command mode change quietly, then
// offers the explicit resume step. Both /mode and /escalate go through here so
// a mode switch never wakes the agents by itself.
func (a *App) switchModeFromComposer(target project.Mode, reason string) {
	if target == a.live.EffectiveMode() {
		a.setStatus("already in "+target.Display()+" mode", false)
		return
	}
	if a.svc == nil {
		return
	}
	if err := a.svc.Do(context.Background(), session.SwitchMode{Mode: target.String(), Reason: reason, Quiet: true}); err != nil {
		a.setStatus(err.Error(), true)
		return
	}
	a.refreshView()
	if a.live.Language == "zh" {
		a.setStatus("模式 → "+target.Display()+"；尚未恢复 Agent 工作", false)
	} else {
		a.setStatus("mode → "+target.Display()+"; agents not resumed yet", false)
	}
	a.openModeResumePrompt(target)
}

func (a *App) openHelp() {
	a.help.offset = 0
	a.overlay = overlayHelp
}

func (a *App) openDetails() {
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
		a.addAsync(asyncResult{kind: "details", payload: detailPayload{status: status, changes: changes}})
	}()
}

func (a *App) copyLatest() {
	text, ok := a.lastAustinToHuman()
	if !ok {
		a.setStatus("Austin has not sent a message to the human yet", false)
		return
	}
	if err := a.copyToClipboard(text); err != nil {
		a.setStatus("copy failed: "+err.Error(), true)
		return
	}
	a.showCopiedToast(len([]rune(text)))
}

func (a *App) openPicker() {
	if a.svc == nil {
		return
	}
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
	a.setStatus(fmt.Sprintf("%s model → %s", agent, model.Reference()), false)
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
	a.setStatus(string(agent)+" restarted", false)
}
