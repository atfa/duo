package gotui

import (
	"fmt"
	"hash/fnv"
	"image"
	"strings"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/session"
	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui/v5"
)

// planOverlay is a read-only view of the shared plan and the Step queue. It
// reads session.View only: no command, no core state, no second source of truth.
// The queue lines are built here from Step fields instead of project.Steps.Render,
// which is English prose written for the agents.
type planOverlay struct {
	*ui.Block
	theme    theme
	view     session.View
	offset   int
	lines    []renderLine
	width    int
	cacheKey string
	box      image.Rectangle
}

func newPlan(t theme) *planOverlay {
	p := &planOverlay{Block: newModalBlock(t), theme: t}
	p.Title = " PLAN & STEPS "
	return p
}

func (p *planOverlay) place(l layout, view session.View, active bool) {
	p.box = centered(l, 92, 30)
	p.Block.SetRect(p.box.Min.X, p.box.Min.Y, p.box.Max.X, p.box.Max.Y)
	if !active {
		return
	}
	p.view = view
	width := max(1, p.Inner.Dx())
	lang := "en"
	if p.theme.Lang != nil {
		lang = *p.theme.Lang
	}
	if key := planCacheKey(view, width, lang); width != p.width || key != p.cacheKey {
		p.width = width
		p.cacheKey = key
		p.rebuild()
		// A shorter plan, a revised queue or a smaller window shrinks the content;
		// keep the viewport inside it instead of drawing an empty panel.
		if max := p.maxOffset(); p.offset > max {
			p.offset = max
		}
	}
}

// begin opens the overlay with the snapshot at that moment. The first place()
// call replaces the provisional width with the real one.
func (p *planOverlay) begin(view session.View) {
	p.view = view
	p.offset = 0
	p.cacheKey = ""
	if p.width < 1 {
		p.width = 84
	}
	p.rebuild()
}

// planCacheKey keeps markdown rebuilding lazy: place() runs on every refresh,
// but the rendered lines only change with the snapshot, width or language.
func planCacheKey(view session.View, width int, lang string) string {
	snap := view.Project
	return fmt.Sprintf("%s|%s|%d|%d|%d|%d|%s|%s|%s|%d|%s|%d|%s|%x",
		snap.EffectiveMode(), snap.Phase, snap.PlanVersion, len(snap.Plan), len(snap.Steps), width,
		lang, snap.Verification.Status, snap.Verification.Head, len(snap.Verification.Note),
		view.Delivery.Status, len(view.Delivery.Reason), view.Delivery.AppliedHead,
		stepsSignature(snap.Steps))
}

// stepsSignature folds every per-step field the queue lines render, so a queue
// revision, a submission or a landing rebuilds the panel even when the step
// count, phase and plan version stay the same.
func stepsSignature(steps project.Steps) uint64 {
	h := fnv.New64a()
	for _, step := range steps {
		fmt.Fprintf(h, "%s\x00%t\x00%s\x00%s\x00%s\x00%s\x00",
			step.Title, step.Done, step.EffectivePolicy(), step.Head,
			step.Verification.Status, step.DeliveryHead)
	}
	return h.Sum64()
}

func (p *planOverlay) rebuild() {
	p.lines = nil
	width := max(1, p.width)
	snap := p.view.Project
	mode := snap.EffectiveMode()
	add := func(text string, style ui.Style) {
		p.lines = append(p.lines, wrapText(text, style, width)...)
	}
	heading := func(text string) {
		p.lines = append(p.lines, renderLine{})
		add(p.theme.tr(text), ui.Style{Fg: p.theme.Accent, Bg: p.theme.Panel, Modifier: ui.ModifierBold})
	}

	plan := strings.TrimSpace(snap.Plan)
	header := fmt.Sprintf("%s  %s   ·   %s  %s",
		p.theme.tr("MODE"), mode.Display(),
		p.theme.tr("PHASE"), string(snap.Phase))
	if plan != "" {
		// A round without a plan has no version to show: "plan v0" beside "FAST
		// has no shared plan." would contradict itself.
		header += fmt.Sprintf("   ·   %s v%d", p.theme.tr("plan"), snap.PlanVersion)
	}
	add(header, p.theme.Subtle)
	if plan != "" {
		heading("PLAN")
		runs := markdownRuns(plan, p.theme.Body, p.theme, width)
		p.lines = append(p.lines, wrapRuns(runs, width)...)
	} else {
		p.lines = append(p.lines, renderLine{})
		switch mode {
		case project.ModeFast:
			add(p.theme.tr("FAST has no shared plan."), p.theme.Body)
			add(p.theme.tr("Austin implements, Tony verifies and Duo lands the diff."), p.theme.BodyMuted)
			add(p.theme.tr("verify")+"  "+p.theme.tr(snap.Verification.Label()), p.theme.Subtle)
			add(p.theme.tr("delivery")+"  "+deliveryLabel(p.view.Delivery, p.theme), p.theme.Subtle)
		case project.ModeGoal:
			add(p.theme.tr("This round has no shared plan yet."), p.theme.Body)
			add(p.theme.tr("GOAL starts in PLAN: both agents publish the plan and approve it before work begins."), p.theme.BodyMuted)
		case project.ModeStep:
			add(p.theme.tr("This round has no plan or step queue yet."), p.theme.Body)
			add(p.theme.tr("STEP starts in PLAN: both agents publish the plan and the queue before work begins."), p.theme.BodyMuted)
		}
		if snap.Phase == project.PhaseDone {
			add(p.theme.tr("This round is DONE; its plan was cleared."), p.theme.Subtle)
		}
		add(p.theme.tr("Ctrl+O opens session details."), p.theme.Subtle)
	}

	if mode == project.ModeStep {
		heading("STEPS")
		if len(snap.Steps) == 0 {
			add(p.theme.tr("No step queue yet."), p.theme.Subtle)
			return
		}
		current := project.FirstOpenStep(snap.Steps)
		for i, step := range snap.Steps {
			mark, style := "○", p.theme.Body
			switch {
			case step.Done || step.DeliveryHead != "":
				mark, style = "✓", p.theme.Success
			case i+1 == current:
				mark = "●"
				style = ui.Style{Fg: p.theme.Accent, Bg: p.theme.Panel, Modifier: ui.ModifierBold}
			}
			line := fmt.Sprintf("%s %d. [%s] %s", mark, i+1, strings.ToUpper(step.EffectivePolicy().String()), step.Title)
			switch {
			case step.DeliveryHead != "":
				line += "  ·  " + p.theme.tr("landed") + hashSuffix(step.DeliveryHead)
			case step.Head != "":
				line += "  ·  " + p.theme.tr("submitted") + hashSuffix(step.Head)
				if step.Verification.Status != "" {
					line += "  ·  " + p.theme.tr(step.Verification.Label())
				}
			case i+1 == current:
				line += "  ·  " + p.theme.tr("current")
			default:
				line += "  ·  " + p.theme.tr("todo")
			}
			p.lines = append(p.lines, wrapText(line, style, width)...)
		}
	}
}

func (p *planOverlay) Draw(buf *ui.Buffer) {
	fillBlock(buf, p.GetRect(), ui.Style{Fg: p.theme.Fg, Bg: p.theme.PanelHigh})
	p.Title = " " + p.theme.tr("PLAN & STEPS") + " "
	p.Block.Draw(buf)
	defer drawModalClose(buf, p.Block, p.theme)
	height := max(0, p.Inner.Dy()-1)
	for i := 0; i < height; i++ {
		idx := p.offset + i
		if idx >= len(p.lines) {
			break
		}
		x := p.Inner.Min.X
		for _, run := range p.lines[idx].runs {
			buf.SetString(run.text, run.style, image.Pt(x, p.Inner.Min.Y+i))
			x += rw.StringWidth(run.text)
		}
	}
	hint := p.theme.tr("↑/↓ scroll  ·  Esc close")
	buf.SetString(fitText(hint, p.Inner.Dx()), p.theme.Timestamp, image.Pt(p.Inner.Min.X, p.Inner.Max.Y-1))
}

func (p *planOverlay) maxOffset() int {
	return max(0, len(p.lines)-max(1, p.Inner.Dy()-1))
}

func (p *planOverlay) Scroll(delta int) {
	p.offset += delta
	if max := p.maxOffset(); p.offset > max {
		p.offset = max
	}
	if p.offset < 0 {
		p.offset = 0
	}
}
