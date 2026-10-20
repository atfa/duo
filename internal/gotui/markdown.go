package gotui

import (
	"strings"
	"unicode"

	"github.com/gdamore/tcell/v3"
	ui "github.com/metaspartan/gotui/v5"
)

// markdownRuns is a deliberately small Markdown renderer. It covers the forms
// Duo messages actually use: headings, lists, quotes, fenced code, inline code,
// emphasis and links. Unlike the Legacy renderer it emits styled runs instead of
// ANSI sequences, so gotui can draw them as real terminal cells.
func markdownRuns(text string, base ui.Style, t theme) []textRun {
	var out []textRun
	inCode := false
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCode = !inCode
			continue
		}
		if inCode {
			out = append(out, textRun{"  " + line, t.Code})
			out = append(out, textRun{"\n", base})
			continue
		}
		switch {
		case trimmed == "":
			out = append(out, textRun{"\n", base})
			continue
		case isRule(trimmed):
			out = append(out, textRun{"────────────────", t.Subtle})
			out = append(out, textRun{"\n", base})
			continue
		case strings.HasPrefix(trimmed, "#"):
			level := 0
			for level < len(trimmed) && trimmed[level] == '#' {
				level++
			}
			title := strings.TrimSpace(trimmed[level:])
			style := ui.Style{Fg: t.Fg, Bg: base.Bg, Modifier: tcell.AttrBold}
			if level >= 3 {
				style = t.Subtle
			}
			out = append(out, textRun{title, style}, textRun{"\n", base})
			continue
		case strings.HasPrefix(trimmed, ">"):
			quote := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
			out = append(out, textRun{"│ ", t.Subtle})
			out = append(out, inlineRuns(quote, t.BodyMuted, t)...)
			out = append(out, textRun{"\n", base})
			continue
		case isBullet(trimmed):
			body := strings.TrimLeftFunc(trimmed, func(r rune) bool { return r == '-' || r == '*' || r == '+' || unicode.IsSpace(r) })
			out = append(out, textRun{"  • ", t.Subtle})
			out = append(out, inlineRuns(body, base, t)...)
			out = append(out, textRun{"\n", base})
			continue
		}
		if n := leadingNumber(trimmed); n != "" {
			rest := strings.TrimSpace(trimmed[len(n):])
			out = append(out, textRun{"  " + n + " ", t.Subtle})
			out = append(out, inlineRuns(rest, base, t)...)
			out = append(out, textRun{"\n", base})
			continue
		}
		out = append(out, inlineRuns(line, base, t)...)
		out = append(out, textRun{"\n", base})
	}
	return trimRuns(out)
}

func isRule(s string) bool {
	if len(s) < 3 {
		return false
	}
	r := rune(s[0])
	if r != '-' && r != '*' && r != '_' {
		return false
	}
	for _, c := range s {
		if c != r && !unicode.IsSpace(c) {
			return false
		}
	}
	return true
}

func isBullet(s string) bool {
	if len(s) < 2 {
		return false
	}
	return (s[0] == '-' || s[0] == '*' || s[0] == '+') && unicode.IsSpace(rune(s[1]))
}

func leadingNumber(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(s) || (s[i] != '.' && s[i] != ')') {
		return ""
	}
	if i+1 < len(s) && !unicode.IsSpace(rune(s[i+1])) {
		return ""
	}
	return s[:i+1]
}

func trimRuns(runs []textRun) []textRun {
	for len(runs) > 0 && runs[len(runs)-1].text == "\n" {
		runs = runs[:len(runs)-1]
	}
	return runs
}

// inlineRuns handles **bold**, *italic*, `code` and [label](url). Styles are
// applied only for the span; a link shows its target so the URL stays readable
// even though gotui cells cannot carry OSC 8 hyperlinks.
func inlineRuns(s string, base ui.Style, t theme) []textRun {
	var out []textRun
	var plain strings.Builder
	flush := func() {
		if plain.Len() > 0 {
			out = append(out, textRun{plain.String(), base})
			plain.Reset()
		}
	}
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "**"):
			if end := strings.Index(s[i+2:], "**"); end >= 0 {
				flush()
				style := base
				style.Modifier |= tcell.AttrBold
				out = append(out, textRun{s[i+2 : i+2+end], style})
				i += end + 4
				continue
			}
		case s[i] == '`':
			if end := strings.IndexByte(s[i+1:], '`'); end >= 0 {
				flush()
				out = append(out, textRun{s[i+1 : i+1+end], t.Code})
				i += end + 2
				continue
			}
		case strings.HasPrefix(s[i:], "["):
			if close := strings.IndexByte(s[i+1:], ']'); close >= 0 {
				labelStart := i + 1
				labelEnd := labelStart + close
				if labelEnd+1 < len(s) && s[labelEnd+1] == '(' {
					if urlEnd := strings.IndexByte(s[labelEnd+2:], ')'); urlEnd >= 0 {
						flush()
						linkStyle := base
						linkStyle.Fg = t.Accent
						out = append(out, textRun{s[labelStart:labelEnd], linkStyle})
						url := s[labelEnd+2 : labelEnd+2+urlEnd]
						if url != "" && url != s[labelStart:labelEnd] {
							out = append(out, textRun{" (" + url + ")", t.Subtle})
						}
						i = labelEnd + 2 + urlEnd + 1
						continue
					}
				}
			}
		case s[i] == '*':
			if end := strings.IndexByte(s[i+1:], '*'); end >= 0 {
				flush()
				style := base
				style.Modifier |= tcell.AttrItalic
				out = append(out, textRun{s[i+1 : i+1+end], style})
				i += end + 2
				continue
			}
		}
		plain.WriteByte(s[i])
		i++
	}
	flush()
	return out
}
