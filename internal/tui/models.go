package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/atfa/duo/internal/models"
	"github.com/atfa/duo/internal/protocol"
)

// modelsResult carries an asynchronous `pi --list-models` read back to the event
// loop. Pi takes over a second to start, so the picker must never block on it.
type modelsResult struct {
	models []models.Model
	err    error
}

// openModelPicker shows the picker and lazily reads the catalog on first use.
func (a *App) openModelPicker() {
	a.view = viewModel
	a.modelFilter = a.modelFilter[:0]
	a.modelCursor = 0
	a.requestFullClear()
	a.loadModels()
}

func (a *App) closeModelPicker() {
	a.view = viewMain
	a.modelFilter = a.modelFilter[:0]
	a.requestFullClear()
}

func (a *App) loadModels() {
	if a.modelLoading || a.modelLoaded {
		return
	}
	command := ""
	if a.agents != nil {
		command = a.agents.Command()
	}
	a.modelLoading = true
	go func() {
		list, err := models.List(context.Background(), command)
		a.modelCh <- modelsResult{models: list, err: err}
	}()
}

func (a *App) applyModelList(res modelsResult) {
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
		return
	}
	if err := a.coord.SetModel(ctx, a.modelTarget, model.Provider, model.ID); err != nil {
		a.setStatus(err.Error(), true)
		return
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
		a.clampModelCursor()
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
	case "backspace":
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

// overlayRow writes one bordered content row of an overlay frame.
func overlayRow(b *strings.Builder, line string, width int, color string) {
	b.WriteString(paint(ansiBorder, "│") + paint(color, fit(line, width)) + paint(ansiBorder, "│\r\n"))
}

func (a *App) writeModel(b *strings.Builder, w, h int) {
	contentWidth := w - 2
	visible := modelVisibleRows(h)
	title := fmt.Sprintf(" Duo Models · %s ", a.modelTarget)
	b.WriteString(paint(ansiBorder, "┌") + paint(ansiTitle, fit(title, contentWidth, "─")) + paint(ansiBorder, "┐\r\n"))

	current := a.currentModel[a.modelTarget]
	if current == "" {
		current = "unknown"
	}
	thinking := a.currentThinking[a.modelTarget]
	if thinking == "" {
		thinking = "unknown"
	}
	overlayRow(b, fmt.Sprintf(" Target: %s · model %s · thinking %s", a.modelTarget, current, thinking), contentWidth, ansiTitle)
	overlayRow(b, " Filter: "+string(a.modelFilter)+"█", contentWidth, ansiTitle)

	list := a.filteredModels()
	a.clampModelCursor()
	status := ""
	switch {
	case a.modelLoading:
		status = " Loading models from Pi…"
	case a.modelErr != "":
		status = " " + a.modelErr
	case len(list) == 0:
		status = " No models match the filter."
	default:
		status = fmt.Sprintf(" %d of %d models", len(list), len(a.models))
	}
	if a.modelErr != "" {
		overlayRow(b, status, contentWidth, ansiError)
	} else {
		overlayRow(b, status, contentWidth, ansiHint)
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
			overlayRow(b, "", contentWidth, ansiHint)
			continue
		}
		color := ansiHint
		if at == a.modelCursor {
			color = ansiTitle
		}
		overlayRow(b, " "+a.modelRow(list[at], at == a.modelCursor), contentWidth, color)
	}

	b.WriteString(paint(ansiBorder, "├") + paint(ansiBorder, strings.Repeat("─", contentWidth)) + paint(ansiBorder, "┤\r\n"))
	foot := " ↑↓ · Space apply·stay · Enter apply·close · Tab agent · Shift+Tab think · Esc "
	b.WriteString(paint(ansiBorder, "└") + paint(ansiHint, fit(foot, contentWidth, "─")) + paint(ansiBorder, "┘"))
}

// modelRow draws one catalog line. The cursor and the model the target agent is
// actually on are independent, so they get independent markers.
func (a *App) modelRow(model models.Model, cursor bool) string {
	prefix := "  "
	switch {
	case cursor && a.currentModel[a.modelTarget] == model.Reference():
		prefix = "▶●"
	case cursor:
		prefix = "▶ "
	case a.currentModel[a.modelTarget] == model.Reference():
		prefix = " ●"
	}
	line := prefix + model.Reference()
	if model.Thinking {
		line += "  ·  thinking"
	}
	return line
}
