package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/atfa/duo/internal/models"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/workspace"
)

// modelsResult carries an asynchronous model catalog read back to the event
// loop. Driver CLI commands can take seconds to start, so the picker must never block on them.
type modelsResult struct {
	agent   protocol.AgentID
	command string
	models  []models.Model
	err     error
}

func (a *App) commandForTarget() string {
	if a.agents != nil {
		return a.agents.CommandFor(a.modelTarget)
	}
	return ""
}

// needsRestartForModel reports whether a driver picks up a new model only at
// launch. Pi has a live bridge and switches in place; the CLI-only drivers take
// the model as a startup flag, so changing it means restarting the agent.
func needsRestartForModel(driverType string) bool {
	return driverType != "pi"
}

func (a *App) driverType(agent protocol.AgentID) string {
	if a.agents != nil {
		return a.agents.DriverTypeFor(agent)
	}
	return "pi"
}

func (a *App) modelForAgent(agent protocol.AgentID) string {
	if m := a.currentModel[agent]; m != "" {
		if a.driverType(agent) == "agy" && strings.Contains(m, "/") {
			m = m[strings.LastIndex(m, "/")+1:]
		}
		return m
	}
	if a.agents != nil {
		if d, ok := a.agents.Driver(agent); ok {
			if m := d.Model(); m != "" {
				if d.DriverType() == "agy" && strings.Contains(m, "/") {
					m = m[strings.LastIndex(m, "/")+1:]
				}
				return m
			}
			return models.Default(d.Manifest())
		}
	}
	// With no driver handle there is no manifest to ask, and Core has no business
	// keeping a copy of one driver's default to hand to another. An empty model
	// means the driver resolves its own, which is what it would have done anyway.
	return ""
}

func (a *App) initModelMaps() {
	if a.modelsByAgent == nil {
		a.modelsByAgent = make(map[protocol.AgentID][]models.Model)
	}
	if a.modelLoadedByAgent == nil {
		a.modelLoadedByAgent = make(map[protocol.AgentID]bool)
	}
	if a.modelLoadingByAgent == nil {
		a.modelLoadingByAgent = make(map[protocol.AgentID]bool)
	}
	if a.modelErrByAgent == nil {
		a.modelErrByAgent = make(map[protocol.AgentID]string)
	}
}

func (a *App) syncModelsForTarget() {
	a.initModelMaps()
	target := a.modelTarget
	if loaded, ok := a.modelLoadedByAgent[target]; ok && loaded {
		a.models = a.modelsByAgent[target]
		a.modelLoaded = true
		a.modelLoading = false
		a.modelErr = a.modelErrByAgent[target]
		a.clampModelCursor()
		return
	}
	if a.modelLoadingByAgent[target] {
		a.models = nil
		a.modelLoaded = false
		a.modelLoading = true
		a.modelErr = ""
		a.clampModelCursor()
		return
	}
	a.models = nil
	a.modelLoaded = false
	a.modelLoading = false
	a.modelErr = ""
	a.clampModelCursor()
	a.loadModels()
}

// openModelPicker shows the picker and lazily reads the catalog on first use.
func (a *App) openModelPicker() {
	a.view = viewModel
	a.modelFilter = a.modelFilter[:0]
	a.modelCursor = 0
	a.requestFullClear()
	a.syncModelsForTarget()
	if current := a.modelForAgent(a.modelTarget); current != "" {
		for i, m := range a.filteredModels() {
			if m.Reference() == current {
				a.modelCursor = i
				break
			}
		}
	}
}

func (a *App) closeModelPicker() {
	a.view = viewMain
	a.modelFilter = a.modelFilter[:0]
	a.requestFullClear()
}

func (a *App) loadModels() {
	a.initModelMaps()
	target := a.modelTarget
	if a.modelLoadingByAgent[target] || a.modelLoadedByAgent[target] {
		return
	}
	command := a.commandForTarget()
	a.modelLoading = true
	a.modelLoadingByAgent[target] = true
	go func() {
		// The driver answers for itself. Which command it will run is its business,
		// so command is carried along only to label the result.
		var src models.Source
		if a.agents != nil {
			if d, ok := a.agents.Driver(target); ok {
				src = d
			}
		}
		list, err := models.Load(context.Background(), src)
		a.modelCh <- modelsResult{agent: target, command: command, models: list, err: err}
	}()
}

func (a *App) applyModelList(res modelsResult) {
	a.initModelMaps()
	a.modelLoadingByAgent[res.agent] = false
	if res.err != nil {
		a.modelErrByAgent[res.agent] = res.err.Error()
	} else {
		a.modelsByAgent[res.agent] = res.models
		a.modelLoadedByAgent[res.agent] = true
		a.modelErrByAgent[res.agent] = ""

		other := otherAgent(res.agent)
		otherCmd := ""
		if a.agents != nil {
			otherCmd = a.agents.CommandFor(other)
		}
		if otherCmd == res.command && !a.modelLoadedByAgent[other] {
			a.modelsByAgent[other] = res.models
			a.modelLoadedByAgent[other] = true
		}
	}

	if a.modelTarget == res.agent {
		a.modelLoading = false
		if res.err != nil {
			a.modelErr = res.err.Error()
			a.setStatus(a.modelErr, true)
		} else {
			a.models = res.models
			a.modelLoaded = true
			a.modelErr = ""
			a.clampModelCursor()
		}
	}
	a.markDirty()
}

// filteredModels applies the picker's case-insensitive substring filter over
// "provider/id", which is how a human thinks about a model.
func (a *App) filteredModels() []models.Model {
	query := strings.ToLower(strings.TrimSpace(string(a.modelFilter)))
	if query == "" {
		return a.models
	}
	var out []models.Model
	for _, model := range a.models {
		if strings.Contains(strings.ToLower(model.Reference()), query) {
			out = append(out, model)
		}
	}
	return out
}

func (a *App) selectedModel() (models.Model, bool) {
	list := a.filteredModels()
	if a.modelCursor < 0 || a.modelCursor >= len(list) {
		return models.Model{}, false
	}
	return list[a.modelCursor], true
}

func (a *App) clampModelCursor() {
	if a.modelCursor >= len(a.filteredModels()) {
		a.modelCursor = len(a.filteredModels()) - 1
	}
	if a.modelCursor < 0 {
		a.modelCursor = 0
	}
}

func (a *App) moveModelCursor(delta int) {
	a.modelCursor += delta
	a.clampModelCursor()
}

// modelVisibleRows is the number of catalog rows between the fixed header rows
// and the footer. The frame is: top border, target, filter, rows, separator,
// footer.
func modelVisibleRows(h int) int { return maxInt(h-5, 1) }

// applySelectedModel asks the target agent's Pi bridge to switch models. Pi
// persists the choice in its session, so a restart or resume keeps it. With
// keepOpen set (Space) the picker stays up so the thinking level can be adjusted
// in the same visit; Enter closes it.
func (a *App) applySelectedModel(ctx context.Context, keepOpen bool) {
	model, ok := a.selectedModel()
	if !ok {
		if !keepOpen {
			a.closeModelPicker()
		}
		return
	}
	if err := a.coord.SetModel(ctx, a.modelTarget, model.Provider, model.ID); err != nil {
		a.setStatus(err.Error(), true)
		return
	}
	if a.agents != nil {
		if d, ok := a.agents.Driver(a.modelTarget); ok {
			// The launch flag needs the provider-qualified reference: opencode
			// only accepts provider/model and aborts on a bare id. agy strips any
			// provider prefix itself, and pi takes the model over the bridge.
			d.SetModel(model.Reference())
			if needsRestartForModel(d.DriverType()) {
				// The model only reaches a non-pi agent as a startup flag, and
				// RestartRunning stops before it starts, so a failure here leaves
				// the agent dead. Report it instead of claiming the new model.
				if err := d.RestartRunning(ctx); err != nil {
					a.setStatus(fmt.Sprintf("%s model → %s failed: %v", a.modelTarget, model.Reference(), err), true)
					if !keepOpen {
						a.closeModelPicker()
					}
					return
				}
			}
		}
	}
	if a.currentModel == nil {
		a.currentModel = make(map[protocol.AgentID]string)
	}
	a.currentModel[a.modelTarget] = model.Reference()
	if a.ws != nil && a.ws.Set().Repository != "" {
		repoRoot := a.ws.Set().Repository
		agentDrivers := map[protocol.AgentID]string{
			protocol.Austin: a.driverType(protocol.Austin),
			protocol.Tony:   a.driverType(protocol.Tony),
		}
		agentModels := map[protocol.AgentID]string{
			protocol.Austin: a.modelForAgent(protocol.Austin),
			protocol.Tony:   a.modelForAgent(protocol.Tony),
		}
		_ = workspace.SaveProjectConfig(repoRoot, a.driverType(protocol.Austin), agentDrivers, agentModels)
	}
	a.setStatus(fmt.Sprintf("%s model → %s", a.modelTarget, model.Reference()), false)
	if !keepOpen {
		a.closeModelPicker()
	}
}

func (a *App) handleModelKey(key string) inputAction {
	switch key {
	case "ctrl-m", "alt-m", "esc":
		a.closeModelPicker()
	case "tab":
		a.modelTarget = otherAgent(a.modelTarget)
		if len(a.modelsByAgent) > 0 || a.modelLoadedByAgent[a.modelTarget] || a.modelLoadingByAgent[a.modelTarget] {
			a.syncModelsForTarget()
		} else {
			a.clampModelCursor()
		}
	case "shift-tab":
		return inputAction{kind: actionCycleThinking}
	case "up":
		a.moveModelCursor(-1)
	case "down":
		a.moveModelCursor(1)
	case "page-up":
		a.moveModelCursor(-modelVisibleRows(a.height))
	case "page-down":
		a.moveModelCursor(modelVisibleRows(a.height))
	case "home":
		a.modelCursor = 0
	case "end":
		a.modelCursor = maxInt(len(a.filteredModels())-1, 0)
	case "backspace", "delete":
		if len(a.modelFilter) > 0 {
			a.modelFilter = a.modelFilter[:len(a.modelFilter)-1]
			a.clampModelCursor()
		}
	case "enter":
		return inputAction{kind: actionApplyModel}
	case " ":
		// Space applies the model but keeps the picker open, so a model and a
		// thinking level can be set in one visit. Model references never contain
		// a space, so giving up space as filter input costs nothing.
		return inputAction{kind: actionApplyModelKeepOpen}
	default:
		if len(key) == 1 && key[0] >= 32 && key[0] < 0x80 {
			a.modelFilter = append(a.modelFilter, key[0])
			a.clampModelCursor()
		}
	}
	return inputAction{}
}

func otherAgent(agent protocol.AgentID) protocol.AgentID {
	if agent == protocol.Tony {
		return protocol.Austin
	}
	return protocol.Tony
}

// overlayRow writes one bordered content row of an overlay frame, in the same
// mode tint as the main frame behind it.
func (a *App) overlayRow(b *strings.Builder, line string, width int, color string) {
	b.WriteString(paint(a.frameColor(), "│") + paint(color, fit(line, width)) + paint(a.frameColor(), "│\r\n"))
}

func (a *App) writeModel(b *strings.Builder, w, h int) {
	contentWidth := w - 2
	visible := modelVisibleRows(h)
	title := fmt.Sprintf(" Duo Models · %s ", a.modelTarget)
	b.WriteString(paint(a.frameColor(), "┌") + paint(ansiTitle, fit(title, contentWidth, "─")) + paint(a.frameColor(), "┐\r\n"))

	current := a.modelForAgent(a.modelTarget)
	if current == "" {
		current = "unknown"
	}
	thinking := a.currentThinking[a.modelTarget]
	if thinking == "" {
		thinking = "unknown"
	}
	a.overlayRow(b, fmt.Sprintf(" Target: %s · model %s · thinking %s", a.modelTarget, current, thinking), contentWidth, ansiTitle)
	a.overlayRow(b, " Filter: "+string(a.modelFilter)+"█", contentWidth, ansiTitle)

	list := a.filteredModels()
	a.clampModelCursor()
	drv := a.driverType(a.modelTarget)
	status := ""
	switch {
	case a.modelLoading:
		status = fmt.Sprintf(" Loading models from %s…", drv)
	case a.modelErr != "":
		status = " " + a.modelErr
	case a.status != "":
		status = " " + a.status
	case len(list) == 0:
		status = " No models match the filter."
	default:
		status = fmt.Sprintf(" %d of %d models", len(list), len(a.models))
	}
	if a.modelErr != "" || a.statusError {
		a.overlayRow(b, status, contentWidth, ansiError)
	} else {
		a.overlayRow(b, status, contentWidth, ansiHint)
	}

	// The count/status line consumes one row; the rest scroll a window that
	// always keeps the cursor visible.
	rowCapacity := maxInt(visible-1, 0)
	start := 0
	if a.modelCursor >= rowCapacity {
		start = a.modelCursor - rowCapacity + 1
	}
	if max := maxInt(len(list)-rowCapacity, 0); start > max {
		start = max
	}
	for i := 0; i < rowCapacity; i++ {
		at := start + i
		if at >= len(list) {
			a.overlayRow(b, "", contentWidth, ansiHint)
			continue
		}
		color := ansiHint
		if at == a.modelCursor {
			color = ansiTitle
		}
		a.overlayRow(b, " "+a.modelRow(list[at], at == a.modelCursor), contentWidth, color)
	}

	b.WriteString(paint(a.frameColor(), "├") + paint(a.frameColor(), strings.Repeat("─", contentWidth)) + paint(a.frameColor(), "┤\r\n"))
	foot := " ↑↓ · Space apply·stay · Enter apply·close · Tab agent · Shift+Tab think · Esc "
	b.WriteString(paint(a.frameColor(), "└") + paint(ansiHint, fit(foot, contentWidth, "─")) + paint(a.frameColor(), "┘"))
}

// modelRow draws one catalog line. The cursor and the model the target agent is
// actually on are independent, so they get independent markers.
func (a *App) modelRow(model models.Model, cursor bool) string {
	current := a.modelForAgent(a.modelTarget)
	prefix := "  "
	switch {
	case cursor && current == model.Reference():
		prefix = "▶●"
	case cursor:
		prefix = "▶ "
	case current == model.Reference():
		prefix = " ●"
	}
	line := prefix + model.Reference()
	if model.Thinking {
		line += "  ·  thinking"
	}
	return line
}
