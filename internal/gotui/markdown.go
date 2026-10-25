package gotui

import (
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v3"
	ui "github.com/metaspartan/gotui/v5"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	gtext "github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// mdParser parses every rendered message. Duo delegates the grammar itself
// (tables, nesting, escapes, entities) to goldmark's CommonMark + GFM parser and
// keeps only the projection into styled runs: gotui draws terminal cells, so a
// run carries the theme style, the clickable target and the wrapping, none of
// which survives an ANSI string.
var mdParser = goldmark.New(goldmark.WithExtensions(extension.GFM))

// markdownRuns renders text as styled runs. width is the transcript body width;
// tables use it to wrap cells before the ordinary line wrapper sees the
// box-drawing borders.
func markdownRuns(text string, base ui.Style, t theme, width int) []textRun {
	src := []byte(strings.ReplaceAll(text, "\r\n", "\n"))
	doc := mdParser.Parser().Parse(gtext.NewReader(src))
	r := &mdRenderer{src: src, base: base, t: t, width: width}
	r.blocks(doc, true)
	return trimRuns(r.out)
}

type mdRenderer struct {
	src   []byte
	base  ui.Style
	t     theme
	width int
	out   []textRun
}

func (r *mdRenderer) write(s string, style ui.Style) {
	if s == "" {
		return
	}
	r.out = append(r.out, textRun{s, style, linkTarget{}})
}

// newline terminates a line. wrapRuns treats it as a hard break, which is why
// block boundaries never run together.
func (r *mdRenderer) newline() {
	r.out = append(r.out, textRun{"\n", r.base, linkTarget{}})
}

// linked writes s with a clickable target, so the label opens the URL even when
// wrapping splits it across rows.
func (r *mdRenderer) linked(s string, style ui.Style, target linkTarget) {
	if s == "" {
		return
	}
	r.out = append(r.out, textRun{s, style, target})
}

// blocks renders the block children of parent. gap separates them with one blank
// line, which is how a Markdown source reads; a tight list is the exception and
// packs its items.
func (r *mdRenderer) blocks(parent ast.Node, gap bool) {
	first := true
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		if !first && gap {
			r.newline()
		}
		r.block(child)
		first = false
	}
}

func (r *mdRenderer) block(n ast.Node) {
	switch v := n.(type) {
	case *ast.Heading:
		r.heading(v)
	case *ast.Paragraph, *ast.TextBlock:
		r.inlineChildren(n, r.base)
		r.newline()
	case *ast.Blockquote:
		r.blockquote(v)
	case *ast.List:
		r.list(v, 0)
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		r.code(n)
	case *ast.ThematicBreak:
		r.write(strings.Repeat("─", 16), r.t.Subtle)
		r.newline()
	case *east.Table:
		r.table(v)
	case *ast.HTMLBlock:
		r.htmlBlock(v)
	default:
		if n.HasChildren() {
			r.blocks(n, true)
		}
	}
}

func (r *mdRenderer) heading(n *ast.Heading) {
	style := ui.Style{Fg: r.t.Fg, Bg: r.base.Bg, Modifier: tcell.AttrBold}
	if n.Level >= 3 {
		style = r.t.Subtle
	}
	r.inlineChildren(n, style)
	r.newline()
}

func (r *mdRenderer) code(n ast.Node) {
	var lines *gtext.Segments
	switch v := n.(type) {
	case *ast.FencedCodeBlock:
		lines = v.Lines()
	case *ast.CodeBlock:
		lines = v.Lines()
	}
	for i := 0; i < lines.Len(); i++ {
		segment := lines.At(i)
		r.write("  "+strings.TrimRight(string(segment.Value(r.src)), "\n"), r.t.Code)
		r.newline()
	}
}

func (r *mdRenderer) htmlBlock(n *ast.HTMLBlock) {
	lines := n.Lines()
	for i := 0; i < lines.Len(); i++ {
		segment := lines.At(i)
		r.write(strings.TrimRight(string(segment.Value(r.src)), "\n"), r.base)
		r.newline()
	}
	if n.HasClosure() {
		r.write(strings.TrimRight(string(n.ClosureLine.Value(r.src)), "\n"), r.base)
		r.newline()
	}
}

// blockquote renders the quote's own blocks and then prefixes every line with a
// gutter, so nested blocks and nested quotes keep their structure instead of
// being flattened into one line.
func (r *mdRenderer) blockquote(n *ast.Blockquote) {
	inner := &mdRenderer{src: r.src, base: r.t.BodyMuted, t: r.t, width: max(1, r.width-2)}
	inner.blocks(n, true)

	gutter := textRun{"│ ", r.t.Subtle, linkTarget{}}
	atLineStart := true
	for _, run := range trimRuns(inner.out) {
		for i, segment := range strings.Split(run.text, "\n") {
			if i > 0 {
				// A blank line inside the quote keeps the gutter, so the quote
				// reads as one block instead of ending at the first paragraph.
				if atLineStart {
					r.out = append(r.out, gutter)
				}
				r.newline()
				atLineStart = true
			}
			if segment == "" {
				continue
			}
			if atLineStart {
				r.out = append(r.out, gutter)
				atLineStart = false
			}
			r.out = append(r.out, textRun{segment, run.style, run.link})
		}
	}
	if !atLineStart {
		r.newline()
	}
}

func (r *mdRenderer) list(n *ast.List, depth int) {
	number := n.Start
	if number <= 0 {
		number = 1
	}
	for item := n.FirstChild(); item != nil; item = item.NextSibling() {
		li, ok := item.(*ast.ListItem)
		if !ok {
			continue
		}
		marker := "• "
		if n.IsOrdered() {
			marker = strconv.Itoa(number) + ". "
			number++
		}
		// A GFM task list owns its checkbox, so it replaces the bullet instead
		// of appearing next to it.
		if box := taskCheckBox(li); box != nil {
			if box.IsChecked {
				marker = "☑ "
			} else {
				marker = "☐ "
			}
		}
		r.listItem(li, marker, depth, !n.IsTight)
	}
}

func (r *mdRenderer) listItem(li *ast.ListItem, marker string, depth int, gap bool) {
	indent := strings.Repeat("  ", depth)
	r.write(indent+marker, r.t.Subtle)
	first := true
	for child := li.FirstChild(); child != nil; child = child.NextSibling() {
		switch child.(type) {
		case *ast.Paragraph, *ast.TextBlock:
			if !first {
				r.newline()
				if gap {
					r.newline()
				}
				r.write(strings.Repeat("  ", depth+1), r.base)
			}
			r.inlineChildren(child, r.base)
			r.newline()
		default:
			if !first && gap {
				r.newline()
			}
			if nested, ok := child.(*ast.List); ok {
				r.list(nested, depth+1)
			} else {
				r.block(child)
			}
		}
		first = false
	}
}

// taskCheckBox reports the GFM task marker of an item, if the item has one.
func taskCheckBox(li *ast.ListItem) *east.TaskCheckBox {
	for child := li.FirstChild(); child != nil; child = child.NextSibling() {
		if box, ok := child.FirstChild().(*east.TaskCheckBox); ok {
			return box
		}
	}
	return nil
}

func (r *mdRenderer) table(n *east.Table) {
	aligns := make([]tableAlign, len(n.Alignments))
	for i, alignment := range n.Alignments {
		switch alignment {
		case east.AlignCenter:
			aligns[i] = tableCenter
		case east.AlignRight:
			aligns[i] = tableRight
		default:
			aligns[i] = tableLeft
		}
	}
	var header [][]textRun
	var rows [][][]textRun
	for row := n.FirstChild(); row != nil; row = row.NextSibling() {
		switch v := row.(type) {
		case *east.TableHeader:
			header = r.tableCells(v, true)
		case *east.TableRow:
			rows = append(rows, r.tableCells(v, false))
		}
	}
	r.out = append(r.out, tableRuns(header, rows, aligns, r.width, r.base, r.t)...)
}

// tableCells renders one row's cells to styled runs. The header is bold so the
// table keeps its emphasis even though the border carries no style of its own.
func (r *mdRenderer) tableCells(row ast.Node, header bool) [][]textRun {
	style := r.base
	if header {
		style.Modifier |= tcell.AttrBold
	}
	var cells [][]textRun
	for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
		inner := &mdRenderer{src: r.src, base: style, t: r.t, width: r.width}
		inner.inlineChildren(cell, style)
		cells = append(cells, trimRuns(inner.out))
	}
	return cells
}

func (r *mdRenderer) inlineChildren(n ast.Node, style ui.Style) {
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		r.inline(child, style)
	}
}

func (r *mdRenderer) inline(n ast.Node, style ui.Style) {
	switch v := n.(type) {
	case *ast.Text:
		r.write(textValue(v.Value(r.src), false), style)
		switch {
		case v.HardLineBreak():
			r.newline()
		case v.SoftLineBreak():
			// A soft break is a space in CommonMark; the wrapper owns reflow.
			r.write(" ", style)
		}
	case *ast.String:
		r.write(textValue(v.Value, v.IsCode()), style)
	case *ast.CodeSpan:
		r.codeSpan(v)
	case *ast.Emphasis:
		if v.Level >= 2 {
			style.Modifier |= tcell.AttrBold
		} else {
			style.Modifier |= tcell.AttrItalic
		}
		r.inlineChildren(v, style)
	case *east.Strikethrough:
		style.Modifier |= tcell.AttrStrikeThrough
		r.inlineChildren(v, style)
	case *ast.Link:
		r.link(v, style, v.Destination)
	case *ast.Image:
		r.link(v, style, v.Destination)
	case *ast.AutoLink:
		label := string(v.Label(r.src))
		target, ok := urlTarget(string(v.URL(r.src)))
		if ok {
			style.Fg = r.t.Accent
		}
		r.linked(label, style, target)
	case *east.TaskCheckBox:
		// The list marker carries the checkbox, so it is not inlined here.
	case *ast.RawHTML:
		for i := 0; i < v.Segments.Len(); i++ {
			segment := v.Segments.At(i)
			r.write(string(segment.Value(r.src)), style)
		}
	default:
		if n.HasChildren() {
			r.inlineChildren(n, style)
		}
	}
}

func (r *mdRenderer) codeSpan(n *ast.CodeSpan) {
	var b strings.Builder
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		switch v := child.(type) {
		case *ast.Text:
			b.WriteString(textValue(v.Value(r.src), true))
		case *ast.String:
			b.WriteString(string(v.Value))
		}
	}
	r.write(b.String(), r.t.Code)
}

// link renders the label, tags every run it emitted with the target so a click
// anywhere in the label opens it, and then shows the URL because gotui cells
// cannot carry an OSC 8 hyperlink.
func (r *mdRenderer) link(n ast.Node, style ui.Style, destination []byte) {
	url := textValue(destination, false)
	target, linked := urlTarget(url)
	label := style
	if linked {
		label.Fg = r.t.Accent
	}
	start := len(r.out)
	r.inlineChildren(n, label)
	for i := start; i < len(r.out); i++ {
		r.out[i].link = target
	}
	if url != "" && url != r.plainText(n) {
		urlStyle := r.t.Subtle
		if linked {
			urlStyle.Fg = r.t.Accent
		}
		r.linked(" ("+url+")", urlStyle, target)
	}
}

func (r *mdRenderer) plainText(n ast.Node) string {
	var b strings.Builder
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		switch v := child.(type) {
		case *ast.Text:
			b.WriteString(textValue(v.Value(r.src), false))
		case *ast.String:
			b.WriteString(textValue(v.Value, v.IsCode()))
		default:
			b.WriteString(r.plainText(child))
		}
	}
	return b.String()
}

// textValue decodes what the parser keeps in the source segment: entity
// references always, backslash escapes only outside a code span, as CommonMark
// specifies. Without it a table cell would show `a \| b` verbatim.
func textValue(raw []byte, inCode bool) string {
	v := raw
	if !inCode {
		v = util.UnescapePunctuations(v)
	}
	v = util.ResolveNumericReferences(v)
	v = util.ResolveEntityNames(v)
	return string(v)
}

func trimRuns(runs []textRun) []textRun {
	for len(runs) > 0 && runs[len(runs)-1].text == "\n" {
		runs = runs[:len(runs)-1]
	}
	return runs
}
