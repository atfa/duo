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
	online, total         int
	status                string
	statusErr             bool
	compact               bool
}

func newHeader(t theme) *headerBar {
	h := &headerBar{Block: *ui.NewBlock(), theme: t}
	h.Border = false
	h.BackgroundColor = t.Panel
	return h
}

func (h *headerBar) Draw(buf *ui.Buffer) {
	fillBlock(buf, h.GetRect(), ui.Style{Fg: h.theme.Fg, Bg: h.theme.Panel})
	y := h.Min.Y
	x := h.Min.X + 1
	width := h.Dx()
	buf.SetString("◆", ui.Style{Fg: h.theme.Accent, Bg: h.theme.Panel, Modifier: ui.ModifierBold}, image.Pt(x, y))
	x += 2
	buf.SetString("DUO", ui.Style{Fg: h.theme.Fg, Bg: h.theme.Panel, Modifier: ui.ModifierBold}, image.Pt(x, y))
	x += 3
	if h.version != "" {
		buf.SetString(" "+h.version+" ", ui.Style{Fg: h.theme.Faint, Bg: h.theme.Panel}, image.Pt(x, y))
		x += len(h.version) + 2
	}
	where := h.repo
	if h.branch != "" {
		where += "  ·  " + h.branch
	}
	if x < h.Max.X-10 {
		buf.SetString(fitText(where, h.Max.X-x-18), h.theme.Subtle, image.Pt(x, y))
	}
	badge := strings.ToUpper(h.mode) + "  ·  " + h.phase
	if h.mode == "" {
		badge = h.phase
	}
	badgeStyle := ui.Style{Fg: h.theme.Accent, Bg: h.theme.Panel, Modifier: ui.ModifierBold}
	if h.phase == "DONE" {
		badgeStyle = ui.Style{Fg: h.theme.OK, Bg: h.theme.Panel, Modifier: ui.ModifierBold}
	}
	if width >= 40 {
		buf.SetString(badge, badgeStyle, image.Pt(max(h.Min.X+1, h.Max.X-rw.StringWidth(badge)-2), y))
	}
	y++
	if !h.compact {
		line := "session " + h.session
		if h.online > 0 {
			line += fmt.Sprintf("   ·   %d/%d agents online", h.online, h.total)
		}
		status := h.status
		statusText := ""
		statusWidth := 0
		if status != "" {
			statusText = fitText(status, max(10, width/2))
			statusWidth = rw.StringWidth(statusText) + 2
		}
		if statusWidth > 0 {
			line = fitText(line, max(8, width-2-statusWidth))
		}
		buf.SetString(fitText(line, width-2), h.theme.Subtle, image.Pt(h.Min.X+1, y))
		if statusText != "" {
			st := h.theme.BodyMuted
			if h.statusErr {
				st = h.theme.Error
			}
			buf.SetString(statusText, st, image.Pt(max(h.Min.X+1, h.Max.X-statusWidth+1), y))
		}
		y++
	}
	if y < h.Max.Y {
		for xx := h.Min.X; xx < h.Max.X; xx++ {
			buf.SetCell(ui.NewCell('─', ui.Style{Fg: h.theme.Border, Bg: h.theme.Panel}), image.Pt(xx, y))
		}
	}
}

type workflowPanel struct {
	ui.Block
	theme   theme
	view    session.View
	compact bool
}

func newWorkflow(t theme) *workflowPanel {
	w := &workflowPanel{Block: *ui.NewBlock(), theme: t}
	w.BorderRounded = true
	w.BorderStyle = ui.Style{Fg: t.Border, Bg: t.Panel}
	w.BackgroundColor = t.Panel
	w.Title = " WORKFLOW "
	w.TitleStyle = ui.Style{Fg: t.Accent, Bg: t.Panel, Modifier: ui.ModifierBold}
	w.PaddingLeft, w.PaddingRight = 1, 1
	return w
}

func (w *workflowPanel) Draw(buf *ui.Buffer) {
	w.Block.Draw(buf)
	if w.Inner.Dx() <= 2 || w.Inner.Dy() <= 0 {
		return
	}
	x, y := w.Inner.Min.X, w.Inner.Min.Y
	line := func(text string, style ui.Style) bool {
		if y >= w.Inner.Max.Y {
			return false
		}
		buf.SetString(fitText(text, w.Inner.Dx()), style, image.Pt(x, y))
		y++
		return true
	}
	mode := w.view.EffectiveMode()
	phase := string(w.view.Phase())
	chain := modeChain(mode.String())
	chainStyle := ui.Style{Fg: w.theme.Accent, Bg: w.theme.Panel, Modifier: ui.ModifierBold}
	chainLine := ""
	for _, p := range chain {
		mark := "○"
		switch {
		case p == phase:
			mark = "●"
		case phaseDone(mode, phase, p):
			mark = "✓"
		}
		token := mark + " " + p
		if chainLine != "" && rw.StringWidth(chainLine)+2+rw.StringWidth(token) > w.Inner.Dx() {
			if !line(chainLine, chainStyle) {
				return
			}
			chainLine = token
			continue
		}
		if chainLine == "" {
			chainLine = token
		} else {
			chainLine += "  " + token
		}
	}
	if chainLine != "" {
		line(chainLine, chainStyle)
	}
	if mode == project.ModeFast {
		line("verify    "+w.view.Project.Verification.Label(), w.verifyStyle())
		line("delivery  "+deliveryLabel(w.view.Delivery), w.deliveryStyle())
	} else {
		line(fmt.Sprintf("plan v%d  ·  %d bytes", w.view.Project.PlanVersion, len(strings.TrimSpace(w.view.Project.Plan))), w.theme.Subtle)
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
			line(fmt.Sprintf("steps         %d/%d landed", done, len(w.view.Project.Steps)), w.theme.Subtle)
			if cur <= len(w.view.Project.Steps) {
				line(fmt.Sprintf("current       %d. %s", cur, w.view.Project.Steps[cur-1].Title), w.theme.BodyMuted)
			}
		} else {
			line(fmt.Sprintf("ready         A:%s  T:%s", readyMark(w.view.Project.Ready[protocol.Austin]), readyMark(w.view.Project.Ready[protocol.Tony])), w.theme.Subtle)
		}
		line("delivery      "+deliveryLabel(w.view.Delivery), w.deliveryStyle())
	}
	if w.Inner.Dy() >= 7 {
		root := w.view.Worktrees.Root
		if root == "" {
			root = w.view.Worktrees.Repository
		}
		line("worktrees     "+shortPath(root, max(8, w.Inner.Dx()-14)), w.theme.Subtle)
	}
	if !w.compact {
		if note := strings.TrimSpace(w.view.Project.Verification.Note); note != "" && mode == project.ModeFast {
			line("note          "+strings.ReplaceAll(note, "\n", " "), w.theme.Warning)
		}
		if w.view.Delivery.Reason != "" && w.view.Delivery.Status == sessionstore.DeliveryBlocked {
			line("delivery note "+w.view.Delivery.Reason, w.theme.Warning)
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

func deliveryLabel(d sessionstore.Delivery) string {
	switch d.Status {
	case sessionstore.DeliveryApplied:
		return "applied" + hashSuffix(d.AppliedHead)
	case sessionstore.DeliveryBlocked:
		return "blocked" + hashSuffix(d.Reason)
	case sessionstore.DeliveryPending:
		return "pending"
	default:
		return "not requested"
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
	a.Title = " " + strings.ToUpper(string(id)) + " "
	a.TitleStyle = ui.Style{Fg: color, Bg: t.Panel, Modifier: ui.ModifierBold}
	a.PaddingLeft, a.PaddingRight = 1, 1
	return a
}

func (a *agentCard) Draw(buf *ui.Buffer) {
	a.Block.Draw(buf)
	if a.Inner.Dx() <= 2 || a.Inner.Dy() <= 0 {
		return
	}
	x, y := a.Inner.Min.X, a.Inner.Min.Y
	line := func(text string, style ui.Style) bool {
		if y >= a.Inner.Max.Y {
			return false
		}
		buf.SetString(fitText(text, a.Inner.Dx()), style, image.Pt(x, y))
		y++
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
		model = "model not reported"
	}
	line("model    "+model, a.theme.Body)
	driver := a.av.Driver
	if driver == "" {
		driver = "driver"
	}
	thinking := a.thinking
	if thinking == "" {
		thinking = a.av.Thinking
	}
	if thinking == "" {
		thinking = "default"
	}
	line("driver   "+driver, a.theme.Subtle)
	line("thinking "+thinking, a.theme.Subtle)
	rt := a.av.Runtime
	if rt.ContextWindow > 0 {
		pct := float64(rt.ContextTokens) / float64(rt.ContextWindow) * 100
		line(fmt.Sprintf("context  %s / %s  %.0f%%", humanTokens(rt.ContextTokens), humanTokens(rt.ContextWindow), pct), a.theme.Subtle)
	} else {
		line("context  not reported", a.theme.Subtle)
	}
	if rt.ToolDepth > 0 || rt.Tool != "" {
		tool := rt.Tool
		if tool == "" {
			tool = "tool"
		}
		if rt.ToolDetail != "" {
			tool += "  " + rt.ToolDetail
		}
		dur := ""
		if !rt.ToolStarted.IsZero() && rt.ToolDepth > 0 {
			dur = "  " + shortDuration(a.now.Sub(rt.ToolStarted))
		}
		line("tool     "+tool+dur, a.theme.Warning)
	}
	if rt.Busy && !rt.TurnStarted.IsZero() {
		line("turn     "+shortDuration(a.now.Sub(rt.TurnStarted))+fmt.Sprintf("  ·  %d tools", rt.Tools), a.theme.Subtle)
	} else if !rt.LastActivity.IsZero() {
		line("idle     "+shortDuration(a.now.Sub(rt.LastActivity))+" ago", a.theme.Subtle)
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
		return "not configured", t.Subtle
	case av.Process == agent.ProcessFailed:
		return "failed", t.Error
	case av.Process == agent.ProcessExited:
		return "exited", t.Warning
	case av.Process == agent.ProcessStarting:
		return "starting", t.Warning
	case av.Process == agent.ProcessStopping:
		return "stopping", t.Warning
	case !av.Connected && av.Capabilities.SelfReports:
		return "connecting", t.Warning
	}
	rt := av.Runtime
	switch {
	case rt.ToolDepth > 0:
		return "working · tool", base
	case rt.Busy && rt.ProviderActive:
		return "thinking", base
	case rt.Busy:
		return "working", base
	default:
		return "ready", t.Success
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
