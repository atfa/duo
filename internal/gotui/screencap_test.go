package gotui

import (
	"fmt"
	"image/color"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v3"
)

// Screenshot geometry in SVG user units. 8.4x18 at 14px approximates a 0.6em
// monospace advance; glyphs are positioned individually, so a viewer that falls
// back to another monospace font cannot shift the cell grid.
const (
	shotCellW  = 8.4
	shotCellH  = 18.0
	shotFontSz = 14
	shotPad    = 16.0
	shotFamily = "'JetBrains Mono','MesloLGS Nerd Font Mono','Noto Sans Mono','DejaVu Sans Mono',ui-monospace,SFMono-Regular,Menlo,Consolas,monospace"
)

// screenSVG exports a simulated screen as a standalone SVG that keeps the
// terminal palette and per-cell glyph positions. It is the colour-preserving
// counterpart of trimmedScreen: both read the same frame of the real app.
func screenSVG(sim tcell.SimulationScreen, th theme) string {
	cells, cols, rows := sim.GetContents()
	return cellsSVG(cells, cols, rows, th)
}

// shotStyle is the resolved look of one cell. Reverse video is applied while
// resolving so grouping and drawing need only the final colours.
type shotStyle struct {
	fg, bg                                   string
	bold, italic, underline, strike, reverse bool
}

func shotStyleOf(s tcell.Style, th theme) shotStyle {
	st := shotStyle{
		fg:        shotColor(s.GetForeground(), th.Fg),
		bg:        shotColor(s.GetBackground(), th.Bg),
		bold:      s.HasBold(),
		italic:    s.HasItalic(),
		underline: s.HasUnderline(),
		strike:    s.HasStrikeThrough(),
		reverse:   s.HasReverse(),
	}
	if st.reverse {
		st.fg, st.bg = st.bg, st.fg
	}
	return st
}

// shotColor renders a cell colour as CSS hex. Terminal defaults (an unset or
// invalid colour) fall back to the theme so frames never render transparent.
func shotColor(c color.Color, fallback tcell.Color) string {
	if c != nil {
		if r, g, b, a := c.RGBA(); a != 0 {
			return fmt.Sprintf("#%02x%02x%02x", uint8(r>>8), uint8(g>>8), uint8(b>>8))
		}
	}
	r, g, b, _ := fallback.RGBA()
	return fmt.Sprintf("#%02x%02x%02x", uint8(r>>8), uint8(g>>8), uint8(b>>8))
}

// shotGlyph returns the printable rune a cell contributes. Wide characters keep
// their rune in the first cell, so continuation and blank cells are skipped.
func shotGlyph(c tcell.SimCell) (rune, bool) {
	if len(c.Runes) == 0 {
		return 0, false
	}
	r := c.Runes[0]
	if r < 0x20 || r == 0x7f {
		return 0, false
	}
	return r, true
}

func cellsSVG(cells []tcell.SimCell, cols, rows int, th theme) string {
	width := shotPad*2 + float64(cols)*shotCellW
	height := shotPad*2 + float64(rows)*shotCellH
	base := shotStyleOf(tcell.StyleDefault.Foreground(th.Fg).Background(th.Bg), th)

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%s" height="%s" viewBox="0 0 %s %s">`+"\n",
		shotNum(width), shotNum(height), shotNum(width), shotNum(height))
	fmt.Fprintf(&b, `<rect width="%s" height="%s" fill="%s"/>`+"\n", shotNum(width), shotNum(height), base.bg)
	fmt.Fprintf(&b, `<g font-family="%s" font-size="%d">`+"\n", shotFamily, shotFontSz)

	for y := 0; y < rows; y++ {
		for x := 0; x < cols; {
			if _, ok := shotGlyph(cells[y*cols+x]); !ok {
				x++
				continue
			}
			st := shotStyleOf(cells[y*cols+x].Style, th)
			start := x
			var runes []rune
			var dx []float64
			prev := x - 1
			for ; x < cols; x++ {
				r, ok := shotGlyph(cells[y*cols+x])
				if !ok || shotStyleOf(cells[y*cols+x].Style, th) != st {
					break
				}
				runes = append(runes, r)
				dx = append(dx, float64(x-prev-1)*shotCellW)
				prev = x
			}

			if st.bg == base.bg {
				// A run on the base background only needs glyphs: blank
				// runs carry no pixels and trailing spaces would be noise.
				for len(runes) > 0 && runes[len(runes)-1] == ' ' {
					runes, dx = runes[:len(runes)-1], dx[:len(dx)-1]
				}
			}
			if len(runes) == 0 {
				continue
			}
			// Non-base runs paint a rectangle behind every cell, including
			// the trailing spaces that were just trimmed for text.
			if st.bg != base.bg {
				end := start + len(runes)
				fmt.Fprintf(&b, `<rect x="%s" y="%s" width="%s" height="%s" fill="%s"/>`+"\n",
					shotNum(shotPad+float64(start)*shotCellW), shotNum(shotPad+float64(y)*shotCellH),
					shotNum(float64(end-start)*shotCellW), shotNum(shotCellH), st.bg)
			}

			var attrs strings.Builder
			if st.bold {
				attrs.WriteString(` font-weight="600"`)
			}
			if st.italic {
				attrs.WriteString(` font-style="italic"`)
			}
			switch {
			case st.underline && st.strike:
				attrs.WriteString(` text-decoration="underline line-through"`)
			case st.underline:
				attrs.WriteString(` text-decoration="underline"`)
			case st.strike:
				attrs.WriteString(` text-decoration="line-through"`)
			}

			positions := ""
			if len(dx) > 1 {
				parts := make([]string, len(dx))
				for i, d := range dx {
					parts[i] = shotNum(d)
				}
				positions = fmt.Sprintf(` dx="%s"`, strings.Join(parts, " "))
			}
			fmt.Fprintf(&b, `<text x="%s" y="%s" fill="%s"%s%s>%s</text>`+"\n",
				shotNum(shotPad+float64(start)*shotCellW), shotNum(shotPad+float64(y)*shotCellH+shotCellH*0.78),
				st.fg, attrs.String(), positions, shotEscape(runes))
		}
	}
	b.WriteString("</g>\n</svg>\n")
	return b.String()
}

func shotNum(v float64) string { return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64) }

func shotEscape(runes []rune) string {
	var b strings.Builder
	for _, r := range runes {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestScreenSVG(t *testing.T) {
	th := newTheme()
	const cols, rows = 6, 2
	cells := make([]tcell.SimCell, cols*rows)
	put := func(x, y int, s tcell.Style, r rune) {
		cells[y*cols+x] = tcell.SimCell{Style: s, Runes: []rune{r}}
	}
	// Row 0: an agent-coloured bold glyph, escaped markup, then trailing blanks.
	put(0, 0, tcell.StyleDefault.Foreground(th.Austin).Bold(true), 'A')
	put(1, 0, tcell.StyleDefault, '&')
	put(2, 0, tcell.StyleDefault, '<')
	for x := 3; x < cols; x++ {
		put(x, 0, tcell.StyleDefault, ' ')
	}
	// Row 1: a wide glyph whose continuation cell is empty, then the next
	// cell must still land on its own column.
	put(0, 1, tcell.StyleDefault, '你')
	cells[1*cols+1] = tcell.SimCell{Style: tcell.StyleDefault}
	put(2, 1, tcell.StyleDefault.Underline(true), 'u')
	put(3, 1, tcell.StyleDefault.Reverse(true), 'r')

	svg := cellsSVG(cells, cols, rows, th)

	for _, want := range []string{
		`width="82.4"`,                     // 16*2 + 6*8.4
		`fill="#60c8ff" font-weight="600"`, // Austin + bold
		`fill="#dce3ec"`,                   // theme foreground for default cells
		`&amp;`, `&lt;`,
		`text-decoration="underline"`,
		`x="32.8"`, // column 2: 16 + 2*8.4, after the wide-glyph skip
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("svg missing %q:\n%s", want, svg)
		}
	}
	// Reverse video swaps the theme colours instead of emitting defaults.
	if !strings.Contains(svg, `fill="#dce3ec"`) || !strings.Contains(svg, `fill="#0a0e14"`) {
		t.Errorf("reverse video did not swap colours:\n%s", svg)
	}
	// One <text> per visible run: bold A, default "&<", 你, underlined u,
	// reversed r. The blank runs and trailing spaces must not add elements.
	if got := strings.Count(svg, "<text "); got != 5 {
		t.Errorf("got %d text runs, want 5:\n%s", got, svg)
	}
	if strings.Contains(svg, "   </text>") || strings.Contains(svg, " </text>") {
		t.Errorf("trailing blanks were not trimmed:\n%s", svg)
	}
}
