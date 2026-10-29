package gotui

import (
	"fmt"
	"image"
	"sort"

	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/session"
	"github.com/atfa/duo/internal/sessionstore"
	rw "github.com/mattn/go-runewidth"
	ui "github.com/metaspartan/gotui/v5"
)

// sessionCostLine renders the session cost total through the ledger's own
// CostLine, the one sentence shared by the cost panel, the overview and the
// coordinator's final summary, so the three can never drift apart.
func sessionCostLine(t theme, total sessionstore.UsageTotals) string {
	zh := t.Lang != nil && *t.Lang == "zh"
	return total.CostLine(zh)
}

// costOverlay is a read-only view of the session's cost ledger. It reads
// session.View only: the ledger is the coordinator's, projected through the
// snapshot, so the panel never keeps a count of its own. A nil ledger means
// nothing was measured — a driver that cannot measure cost — and the panel says
// so rather than showing a cost of zero.
type costOverlay struct {
	*ui.Block
	theme theme
	lines []renderLine
	width int
	box   image.Rectangle
}

func newCost(t theme) *costOverlay {
	c := &costOverlay{Block: newModalBlock(t), theme: t}
	c.Title = " COST "
	return c
}

func (c *costOverlay) place(l layout) {
	c.box = centered(l, 80, 20)
	c.Block.SetRect(c.box.Min.X, c.box.Min.Y, c.box.Max.X, c.box.Max.Y)
	if c.width != c.Inner.Dx() {
		c.width = c.Inner.Dx()
	}
}

func (c *costOverlay) begin(view session.View) {
	c.rebuild(view)
}

func (c *costOverlay) rebuild(view session.View) {
	c.lines = nil
	add := func(text string, style ui.Style) {
		c.lines = append(c.lines, wrapText(text, style, max(1, c.Inner.Dx()))...)
	}
	add(c.theme.tr("AGENTS"), ui.Style{Fg: c.theme.Accent, Bg: c.theme.Panel, Modifier: ui.ModifierBold})
	ledger := view.Usage
	if ledger == nil {
		add(c.theme.tr("no usage measured yet"), c.theme.Subtle)
		return
	}
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		totals, ok := ledger.Agents[agent]
		if !ok {
			continue
		}
		add(fmt.Sprintf("%-7s %s", agent, c.theme.tr(usageFigures(totals))), c.theme.Body)
	}
	c.lines = append(c.lines, renderLine{})
	add(c.theme.tr("PHASES"), ui.Style{Fg: c.theme.Accent, Bg: c.theme.Panel, Modifier: ui.ModifierBold})
	phases := make([]string, 0, len(ledger.Phases))
	for phase := range ledger.Phases {
		phases = append(phases, phase)
	}
	sort.Strings(phases)
	for _, phase := range phases {
		add(fmt.Sprintf("%-12s %s", phase, c.theme.tr(usageFigures(ledger.Phases[phase]))), c.theme.Body)
	}
	if len(phases) == 0 {
		add(c.theme.tr("no usage measured yet"), c.theme.Subtle)
	}
	c.lines = append(c.lines, renderLine{})
	add(sessionCostLine(c.theme, ledger.Total()), ui.Style{Fg: c.theme.Accent, Bg: c.theme.Panel, Modifier: ui.ModifierBold})
}

// usageFigures renders one ledger entry: turns, tokens, cost.
func usageFigures(u sessionstore.UsageTotals) string {
	return fmt.Sprintf("%d turns · %s tok · $%.2f", u.Turns, sessionstore.HumanTokens(u.TotalTokens), u.CostUSD)
}

func (c *costOverlay) Draw(buf *ui.Buffer) {
	fillBlock(buf, c.GetRect(), ui.Style{Fg: c.theme.Fg, Bg: c.theme.PanelHigh})
	c.Title = " " + c.theme.tr("COST") + " "
	c.Block.Draw(buf)
	defer drawModalClose(buf, c.Block, c.theme)
	height := max(0, c.Inner.Dy()-1)
	for i := 0; i < height && i < len(c.lines); i++ {
		x := c.Inner.Min.X
		for _, run := range c.lines[i].runs {
			buf.SetString(run.text, run.style, image.Pt(x, c.Inner.Min.Y+i))
			x += rw.StringWidth(run.text)
		}
	}
	buf.SetString(fitText(c.theme.tr("Esc close"), c.Inner.Dx()), c.theme.Timestamp, image.Pt(c.Inner.Min.X, c.Inner.Max.Y-1))
}
