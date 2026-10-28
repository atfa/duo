package gotui

import (
	"fmt"
	"image"
	"strings"
	"time"

	"github.com/atfa/duo/internal/agent"
	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui/v5"
)

func fillBlock(buf *ui.Buffer, r image.Rectangle, style ui.Style) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			buf.SetCell(ui.NewCell(' ', style), image.Pt(x, y))
		}
	}
}

// expandableTitle shows the click affordance shared by the three sidebar
// panels: a right-pointing triangle while collapsed, down while expanded.
func expandableTitle(label string, expanded bool) string {
	if expanded {
		return " " + label + " ▾ "
	}
	return " " + label + " ▸ "
}

// viewTabLabels are drawn on the header's divider row, not on a row of their
// own; computeLayout and the header use the same strings so their hit boxes and
// glyphs cannot drift apart.
func viewTabLabels(t theme) (string, string) {
	return " " + t.tr("Split") + " ", " " + t.tr("Timeline") + " "
}

type background struct {
	ui.Block
	theme theme
}

func newBackground(t theme) *background {
	b := &background{Block: *ui.NewBlock(), theme: t}
	b.Border = false
	b.BackgroundColor = t.Bg
	return b
}

func (b *background) Draw(buf *ui.Buffer) {
	fillBlock(buf, b.GetRect(), b.theme.Base)
}

type headerBar struct {
	ui.Block
	theme                 theme
	version, repo, branch string
	session, mode, phase  string
	pause                 session.PauseState
	online, total         int
	view                  viewMode

	// modeBadge is the rectangle drawn for the mode/phase badge this frame. The
	// mouse handler reads it, so a click always matches what the human sees even
	// after the badge grows (PAUSED) or the terminal resizes.
	modeBadge    image.Rectangle
	showReturn   bool
	returnButton image.Rectangle
}

func newHeader(t theme) *headerBar {
	h := &headerBar{Block: *ui.NewBlock(), theme: t}
	h.Border = false
	h.BackgroundColor = t.Panel
	return h
}

func (h *headerBar) Draw(buf *ui.Buffer) {
	fillBlock(buf, h.GetRect(), ui.Style{Fg: h.theme.Fg, Bg: h.theme.Panel})
	h.modeBadge = image.Rectangle{}
	y := h.Min.Y
	x := h.Min.X + 1
	right := h.Max.X
	h.returnButton = image.Rectangle{}
	if h.showReturn && h.Dx() >= 32 {
		right -= 19
		h.returnButton = image.Rect(right, h.Min.Y, h.Max.X-1, h.Min.Y+1)
	}
	width := right - h.Min.X
	buf.SetString("◆", ui.Style{Fg: h.theme.Accent, Bg: h.theme.Panel, Modifier: ui.ModifierBold}, image.Pt(x, y))
	x += 2
	buf.SetString("DUO", ui.Style{Fg: h.theme.Fg, Bg: h.theme.Panel, Modifier: ui.ModifierBold}, image.Pt(x, y))
	x += 3
	if h.version != "" {
		buf.SetString(" "+h.version+" ", ui.Style{Fg: h.theme.Faint, Bg: h.theme.Panel}, image.Pt(x, y))
		x += len(h.version) + 2
	}
	badge := strings.ToUpper(h.mode) + "  ·  " + h.phase
	if h.mode == "" {
		badge = h.phase
	}
	badgeStyle := ui.Style{Fg: h.theme.Accent, Bg: h.theme.Panel, Modifier: ui.ModifierBold}
	if h.phase == "DONE" {
		badgeStyle = ui.Style{Fg: h.theme.OK, Bg: h.theme.Panel, Modifier: ui.ModifierBold}
	}
	switch h.pause {
	case session.PausePausing:
		badge += "  ·  PAUSING"
		badgeStyle = ui.Style{Fg: h.theme.Warn, Bg: h.theme.Panel, Modifier: ui.ModifierBold}
	case session.PausePaused:
		badge += "  ·  PAUSED"
		badgeStyle = ui.Style{Fg: h.theme.OK, Bg: h.theme.Panel, Modifier: ui.ModifierBold}
	}
	// Reserve the badge's actual width, not a fixed 18 columns: PAUSED grows it
	// and the old reservation let the badge overwrite the end of the branch.
	badgeWidth := rw.StringWidth(badge)
	reserve := 18
	if width >= 40 {
		reserve = badgeWidth + 2
	}
	where := h.repo
	if h.branch != "" {
		where += "  ·  " + h.branch
	}
	if x < right-10 {
		buf.SetString(fitText(where, max(1, right-x-reserve)), h.theme.Subtle, image.Pt(x, y))
	}
	if width >= 40 {
		badgeX := max(h.Min.X+1, right-badgeWidth-2)
		buf.SetString(badge, badgeStyle, image.Pt(badgeX, y))
		h.modeBadge = image.Rect(badgeX, y, badgeX+badgeWidth, y+1)
	}
	if !h.returnButton.Empty() {
		buf.SetString("[ Back to xduo ]", ui.Style{Fg: h.theme.Bg, Bg: h.theme.Accent}, h.returnButton.Min)
	}
	y++
	if y < h.Max.Y {
		split, timeline := viewTabLabels(h.theme)
		tabWidth := rw.StringWidth(split) + rw.StringWidth(timeline)
		tabX := max(h.Min.X+1, h.Max.X-tabWidth-1)
		line := h.theme.tr("session") + " " + h.session
		if h.online > 0 {
			line += fmt.Sprintf("   ·   %d/%d %s", h.online, h.total, h.theme.tr("agents online"))
		}
		if available := tabX - (h.Min.X + 1) - 1; available > 8 {
			buf.SetString(fitText(line, available), h.theme.Subtle, image.Pt(h.Min.X+1, y))
		}
		x = tabX
		drawTab := func(label string, active bool) {
			st := ui.Style{Fg: h.theme.Muted, Bg: h.theme.Panel}
			if active {
				st = ui.Style{Fg: h.theme.Bg, Bg: h.theme.Accent, Modifier: ui.ModifierBold}
			}
			buf.SetString(label, st, image.Pt(x, y))
			x += rw.StringWidth(label)
		}
		drawTab(split, h.view == viewSplit)
		drawTab(timeline, h.view == viewTimeline)
	}
}

type workflowPanel struct {
	ui.Block
	theme    theme
	view     session.View
	compact  bool
	expanded bool
}

func newWorkflow(t theme) *workflowPanel {
	w := &workflowPanel{Block: *ui.NewBlock(), theme: t}
	w.BorderRounded = true
	w.BorderStyle = ui.Style{Fg: t.Border, Bg: t.Panel}
	w.BackgroundColor = t.Panel
	w.Title = expandableTitle(w.theme.tr("WORKFLOW"), false)
	w.TitleStyle = ui.Style{Fg: t.Accent, Bg: t.Panel, Modifier: ui.ModifierBold}
	w.PaddingLeft, w.PaddingRight = 1, 1
	return w
}

func (w *workflowPanel) Draw(buf *ui.Buffer) {
	w.Title = expandableTitle(w.theme.tr("WORKFLOW"), w.expanded)
	w.Block.Draw(buf)
	if w.Inner.Dx() <= 2 || w.Inner.Dy() <= 0 {
		return
	}
	x, y := w.Inner.Min.X, w.Inner.Min.Y
	line := func(text string, style ui.Style) bool {
		for _, wrapped := range wrapText(text, style, w.Inner.Dx()) {
			if y >= w.Inner.Max.Y {
				return false
			}
			buf.SetString(wrapped.text(), style, image.Pt(x, y))
			y++
		}
		return true
	}
	mode := w.view.EffectiveMode()
	phase := string(w.view.Phase())
	if mode == project.ModeStep {
		phase = stepQueuePhase(w.view.Project)
	}
	lineRuns := func(runs []textRun) bool {
		for _, packed := range packChainRuns(runs, w.Inner.Dx()) {
			if y >= w.Inner.Max.Y {
				return false
			}
			cx := x
			for _, run := range packed.runs {
				buf.SetString(run.text, run.style, image.Pt(cx, y))
				cx += rw.StringWidth(run.text)
			}
			y++
		}
		return true
	}
	if !lineRuns(stageChainRuns(modeChain(mode.String()), phase, mode, w.theme)) {
		return
	}
	if mode == project.ModeFast {
		line(w.theme.tr("verify")+"  "+w.theme.tr(w.view.Project.Verification.Label()), w.verifyStyle())
		line(w.theme.tr("delivery")+"  "+deliveryLabel(w.view.Delivery, w.theme), w.deliveryStyle())
	} else {
		line(fmt.Sprintf("%s v%d  ·  %d %s", w.theme.tr("plan"), w.view.Project.PlanVersion, len(strings.TrimSpace(w.view.Project.Plan)), w.theme.tr("bytes")), w.theme.Subtle)
		if mode == project.ModeStep && len(w.view.Project.Steps) > 0 {
			cur := w.view.Project.Step
			if cur < 1 {
				cur = 1
			}
			done := 0
			for _, s := range w.view.Project.Steps {
				if s.Done || s.DeliveryHead != "" {
					done++
				}
			}
			line(fmt.Sprintf("%s  %d/%d %s", w.theme.tr("steps"), done, len(w.view.Project.Steps), w.theme.tr("landed")), w.theme.Subtle)
			if cur <= len(w.view.Project.Steps) {
				step := w.view.Project.Steps[cur-1]
				line(fmt.Sprintf("%s  %d. [%s] %s", w.theme.tr("current"), cur, strings.ToUpper(step.EffectivePolicy().String()), step.Title), w.theme.BodyMuted)
				if phase == "STEPS" {
					// The queue lifecycle alone cannot say where the step is: a FAST
					// step and a GOAL step share the queue but not their gates.
					gateChain, gateStage := stepGateChain(step, w.view.Phase())
					prefix := fmt.Sprintf("%s  [%s]  ", w.theme.tr("step gate"), strings.ToUpper(step.EffectivePolicy().String()))
					runs := append([]textRun{{text: prefix, style: w.theme.Subtle}}, stageChainRuns(gateChain, gateStage, step.EffectivePolicy(), w.theme)...)
					if !lineRuns(runs) {
						return
					}
				}
			}
		} else {
			line(fmt.Sprintf("%s  A:%s  T:%s", w.theme.tr("ready"), readyMark(w.view.Project.Ready[protocol.Austin]), readyMark(w.view.Project.Ready[protocol.Tony])), w.theme.Subtle)
		}
		line(w.theme.tr("delivery")+"  "+deliveryLabel(w.view.Delivery, w.theme), w.deliveryStyle())
	}
	if w.Inner.Dy() >= 7 {
		root := w.view.Worktrees.Root
		if root == "" {
			root = w.view.Worktrees.Repository
		}
		line(w.theme.tr("worktrees")+"  "+shortPath(root, max(8, w.Inner.Dx()-14)), w.theme.Subtle)
	}
	if !w.compact {
		if note := strings.TrimSpace(w.view.Project.Verification.Note); note != "" && mode == project.ModeFast {
			line(w.theme.tr("note")+"  "+strings.ReplaceAll(note, "\n", " "), w.theme.Warning)
		}
		if w.view.Delivery.Reason != "" && w.view.Delivery.Status == sessionstore.DeliveryBlocked {
			line(w.theme.tr("delivery note")+"  "+w.view.Delivery.Reason, w.theme.Warning)
		}
	}
}

func (w *workflowPanel) verifyStyle() ui.Style {
	switch w.view.Project.Verification.Status {
	case project.VerificationPassed:
		return w.theme.Success
	case project.VerificationIssueFound:
		return w.theme.Warning
	default:
		return w.theme.Subtle
	}
}

func (w *workflowPanel) deliveryStyle() ui.Style {
	switch w.view.Delivery.Status {
	case sessionstore.DeliveryApplied:
		return w.theme.Success
	case sessionstore.DeliveryBlocked:
		return w.theme.Error
	default:
		return w.theme.Subtle
	}
}

// stepQueuePhase collapses Step's internal Goal phase set onto the queue's
// top-level lifecycle. The per-step gate remains visible in the step status.
func stepQueuePhase(s project.Snapshot) string {
	switch s.Phase {
	case project.PhasePlan:
		return "PLAN"
	case project.PhaseDone:
		return "DONE"
	default:
		return "STEPS"
	}
}

// stageChainRuns renders one workflow tier as styled tokens: the stage the
// session is in now gets a pointer and a solid accent chip, stages behind it are
// ticked, and stages ahead stay quiet. One uniformly tinted line made every
// stage look alike, which is what the panel has to answer at a glance.
func stageChainRuns(chain []string, stage string, policy project.Mode, t theme) []textRun {
	runs := make([]textRun, 0, len(chain))
	for i, p := range chain {
		mark, style := "○", t.Subtle
		switch {
		case p == stage:
			mark, style = "▶", ui.Style{Fg: t.Bg, Bg: t.Accent, Modifier: ui.ModifierBold}
		case phaseDone(policy, stage, p):
			// Keep the panel background: theme.Success carries the page colour.
			mark, style = "✓", ui.Style{Fg: t.OK, Bg: t.Panel}
		}
		// The gap travels with the token after the first, so packing can move a
		// whole stage to the next row without leaving spaces behind.
		text := mark + " " + p
		if i > 0 {
			text = "  " + text
		}
		runs = append(runs, textRun{text: text, style: style})
	}
	return runs
}

// packChainRuns lays workflow tokens out as whole words. wrapRuns breaks at any
// space, which at the sidebar's width could strand a ○ marker at the end of one
// row and its stage name on the next; a stage token must move as one unit.
func packChainRuns(runs []textRun, width int) []renderLine {
	var lines []renderLine
	lineWidth := 0
	for _, run := range runs {
		text := run.text
		if lineWidth == 0 {
			text = strings.TrimLeft(text, " ")
		}
		if trimmed := strings.TrimRight(text, " "); rw.StringWidth(trimmed) > width {
			text = fitText(trimmed, width)
		}
		w := rw.StringWidth(strings.TrimRight(text, " "))
		if lineWidth > 0 && lineWidth+w > width {
			lines = append(lines, renderLine{})
			lineWidth = 0
			text = strings.TrimLeft(text, " ")
			w = rw.StringWidth(text)
		}
		if len(lines) == 0 {
			lines = append(lines, renderLine{})
		}
		last := &lines[len(lines)-1]
		last.runs = append(last.runs, textRun{text: text, style: run.style})
		lineWidth += w
	}
	if len(lines) == 0 {
		lines = append(lines, renderLine{})
	}
	return lines
}

func phaseDone(mode project.Mode, current, candidate string) bool {
	for _, p := range modeChain(mode.String()) {
		if p == current {
			return false
		}
		if p == candidate {
			return true
		}
	}
	return false
}

func deliveryLabel(d sessionstore.Delivery, t theme) string {
	switch d.Status {
	case sessionstore.DeliveryApplied:
		return t.tr("applied") + hashSuffix(d.AppliedHead)
	case sessionstore.DeliveryBlocked:
		return t.tr("blocked") + hashSuffix(d.Reason)
	case sessionstore.DeliveryPending:
		return t.tr("pending")
	default:
		return t.tr("not requested")
	}
}

func hashSuffix(h string) string {
	if h == "" {
		return ""
	}
	if len(h) > 8 {
		h = h[:8]
	}
	return "  " + h
}

func readyMark(v bool) string {
	if v {
		return "✓"
	}
	return "…"
}

type agentCard struct {
	ui.Block
	theme    theme
	id       protocol.AgentID
	av       session.AgentView
	frame    int
	compact  bool
	expanded bool
	now      time.Time
	model    string
	thinking string
}

func newAgentCard(t theme, id protocol.AgentID) *agentCard {
	a := &agentCard{Block: *ui.NewBlock(), theme: t, id: id, now: time.Now()}
	a.BorderRounded = true
	color := t.agentColor(string(id))
	a.BorderStyle = ui.Style{Fg: color, Bg: t.Panel}
	a.BackgroundColor = t.Panel
	a.Title = expandableTitle(strings.ToUpper(string(id)), false)
	a.TitleStyle = ui.Style{Fg: color, Bg: t.Panel, Modifier: ui.ModifierBold}
	a.PaddingLeft, a.PaddingRight = 1, 1
	return a
}

func (a *agentCard) Draw(buf *ui.Buffer) {
	a.Title = expandableTitle(strings.ToUpper(string(a.id)), a.expanded)
	a.Block.Draw(buf)
	if a.Inner.Dx() <= 2 || a.Inner.Dy() <= 0 {
		return
	}
	x, y := a.Inner.Min.X, a.Inner.Min.Y
	line := func(text string, style ui.Style) bool {
		for _, wrapped := range wrapText(text, style, a.Inner.Dx()) {
			if y >= a.Inner.Max.Y {
				return false
			}
			buf.SetString(wrapped.text(), style, image.Pt(x, y))
			y++
		}
		return true
	}
	state, stateStyle := agentState(a.av, a.id, a.theme)
	spin := ""
	if a.av.Runtime.Busy || a.av.Runtime.ProviderActive || a.av.Runtime.ToolDepth > 0 {
		spin = spinnerFrame(a.frame) + " "
	}
	line(spin+state, stateStyle)
	model := a.model
	if model == "" {
		model = a.av.Model
	}
	if model == "" && a.av.Manifest != nil {
		model = a.av.Manifest.DefaultModel
	}
	if model == "" {
		model = a.theme.tr("model not reported")
	}
	line(a.theme.tr("model")+"    "+model, a.theme.Body)
	driver := a.av.Driver
	if driver == "" {
		driver = a.theme.tr("driver")
	}
	thinking := a.thinking
	if thinking == "" {
		thinking = a.av.Thinking
	}
	if thinking == "" {
		thinking = a.theme.tr("default")
	}
	line(a.theme.tr("driver")+"   "+driver, a.theme.Subtle)
	line(a.theme.tr("thinking")+" "+thinking, a.theme.Subtle)
	rt := a.av.Runtime
	if rt.ContextWindow > 0 {
		pct := float64(rt.ContextTokens) / float64(rt.ContextWindow) * 100
		// The coordinator owns the pressure judgement (ContextWarned is set when
		// the occupancy crosses the configured threshold and cleared when it falls
		// back); the card only colours the figure it already shows.
		contextStyle := a.theme.Subtle
		if rt.ContextWarned {
			contextStyle = a.theme.Error
		}
		line(fmt.Sprintf("%s  %s / %s  %.0f%%", a.theme.tr("context"), humanTokens(rt.ContextTokens), humanTokens(rt.ContextWindow), pct), contextStyle)
	} else {
		line(a.theme.tr("context")+"  "+a.theme.tr("not reported"), a.theme.Subtle)
	}
	if rt.ToolDepth > 0 || rt.Tool != "" {
		tool := rt.Tool
		if tool == "" {
			tool = a.theme.tr("tool")
		}
		if rt.ToolDetail != "" {
			tool += "  " + rt.ToolDetail
		}
		dur := ""
		if !rt.ToolStarted.IsZero() && rt.ToolDepth > 0 {
			dur = "  " + shortDuration(a.now.Sub(rt.ToolStarted))
		}
		line(a.theme.tr("tool")+"     "+tool+dur, a.theme.Warning)
	}
	if rt.Busy && !rt.TurnStarted.IsZero() {
		line(fmt.Sprintf("%s     %s  ·  %d %s", a.theme.tr("turn"), shortDuration(a.now.Sub(rt.TurnStarted)), rt.Tools, a.theme.tr("tools")), a.theme.Subtle)
	} else if !rt.LastActivity.IsZero() {
		line(a.theme.tr("idle")+"     "+shortDuration(a.now.Sub(rt.LastActivity))+" "+a.theme.tr("ago"), a.theme.Subtle)
	}
	if !a.compact {
		for i := len(rt.Recent) - 1; i >= 0 && i >= len(rt.Recent)-2; i-- {
			n := rt.Recent[i]
			mark := "✓"
			st := a.theme.Success
			if !n.OK {
				mark = "✗"
				st = a.theme.Error
			}
			line("  "+mark+" "+n.Name+"  "+shortDuration(n.Duration), st)
		}
		if rt.LastError != "" {
			line("error    "+rt.LastError, a.theme.Error)
		}
	}
}

func agentState(av session.AgentView, id protocol.AgentID, t theme) (string, ui.Style) {
	color := t.agentColor(string(id))
	base := ui.Style{Fg: color, Bg: t.Panel}
	switch {
	case !av.Present:
		return t.tr("not configured"), t.Subtle
	case av.Process == agent.ProcessFailed:
		return t.tr("failed"), t.Error
	case av.Process == agent.ProcessExited:
		return t.tr("exited"), t.Warning
	case av.Process == agent.ProcessStarting:
		return t.tr("starting"), t.Warning
	case av.Process == agent.ProcessStopping:
		return t.tr("stopping"), t.Warning
	case !av.Connected && av.Capabilities.SelfReports:
		return t.tr("connecting"), t.Warning
	}
	rt := av.Runtime
	switch {
	case rt.ToolDepth > 0:
		return t.tr("working · tool"), base
	case rt.Busy && rt.ProviderActive:
		return t.tr("thinking"), base
	case rt.Busy:
		return t.tr("working"), base
	default:
		return t.tr("ready"), t.Success
	}
}

type footerBar struct {
	ui.Block
	theme   theme
	status  string
	isError bool
	hint    string
	right   string
}

func newFooter(t theme) *footerBar {
	f := &footerBar{Block: *ui.NewBlock(), theme: t}
	f.Border = false
	f.BackgroundColor = t.Panel
	return f
}

func (f *footerBar) Draw(buf *ui.Buffer) {
	fillBlock(buf, f.GetRect(), ui.Style{Fg: f.theme.Muted, Bg: f.theme.Panel})
	style := f.theme.Subtle
	text := f.status
	if text == "" {
		text = f.hint
	}
	if f.isError {
		style = f.theme.Error
	}
	rightLabel := ""
	rightWidth := 0
	if f.right != "" && f.Dx() > 30 {
		rightLabel = "· " + f.right
		rightWidth = rw.StringWidth(rightLabel)
	}
	leftWidth := max(8, f.Dx()-2)
	if rightWidth > 0 {
		leftWidth = max(8, f.Dx()-rightWidth-3)
	}
	if text != "" {
		buf.SetString(fitText(text, leftWidth), style, image.Pt(f.Min.X+1, f.Min.Y))
	}
	if rightLabel != "" {
		buf.SetString(rightLabel, f.theme.Timestamp, image.Pt(max(f.Min.X+1, f.Max.X-rightWidth-1), f.Min.Y))
	}
}

func runtimeActive(rt harness.AgentRuntime) bool {
	return rt.Busy || rt.ProviderActive || rt.ToolDepth > 0
}
