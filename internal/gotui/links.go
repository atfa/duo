package gotui

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/workspace"
)

type linkKind uint8

const (
	linkNone linkKind = iota
	linkURL
	linkFile
)

type linkTarget struct {
	kind  linkKind
	value string
}

func urlTarget(raw string) (linkTarget, bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return linkTarget{}, false
	}
	return linkTarget{kind: linkURL, value: raw}, true
}

// linkifyRuns finds URLs and existing paths in already parsed Markdown runs.
// It is deliberately a frontend concern: the agent's text is the source, and no
// extra structured output or prompt change is required.
func linkifyRuns(runs []textRun, roots []string, t theme) []textRun {
	var out []textRun
	for _, run := range runs {
		if run.text == "" || run.link.kind != linkNone {
			out = append(out, run)
			continue
		}
		out = append(out, linkifyText(run, roots, t)...)
	}
	return out
}

func linkifyText(run textRun, roots []string, t theme) []textRun {
	text := run.text
	var out []textRun
	last := 0
	for start := 0; start < len(text); {
		rel := strings.IndexFunc(text[start:], unicode.IsSpace)
		end := len(text)
		if rel >= 0 {
			end = start + rel
		}
		token := text[start:end]
		inner, lead, trail := trimLinkToken(token)
		if inner != "" {
			if target, ok := tokenLink(inner, roots); ok {
				if start+lead > last {
					out = append(out, textRun{text[last : start+lead], run.style, linkTarget{}})
				}
				style := run.style
				style.Fg = t.Accent
				out = append(out, textRun{inner, style, target})
				last = start + trail
			}
		}
		if rel < 0 {
			break
		}
		start = end + 1
	}
	if last < len(text) {
		out = append(out, textRun{text[last:], run.style, linkTarget{}})
	}
	return out
}

func trimLinkToken(token string) (inner string, lead, trail int) {
	start, end := 0, len(token)
	leading := "([{<\"'“‘（【《"
	trailing := ".,;:!?)]}>\"'”’）】》，。；：！？、"
	for start < end {
		r, size := utf8.DecodeRuneInString(token[start:])
		if !strings.ContainsRune(leading, r) {
			break
		}
		start += size
	}
	for end > start {
		r, size := utf8.DecodeLastRuneInString(token[:end])
		if !strings.ContainsRune(trailing, r) {
			break
		}
		end -= size
	}
	return token[start:end], start, end
}

func tokenLink(token string, roots []string) (linkTarget, bool) {
	if target, ok := urlTarget(token); ok {
		return target, true
	}
	if !looksLikePath(token) {
		return linkTarget{}, false
	}
	if path, ok := resolvePathToken(token, roots); ok {
		return linkTarget{kind: linkFile, value: path}, true
	}
	return linkTarget{}, false
}

func looksLikePath(token string) bool {
	if token == "" || strings.Contains(token, "://") || !hasLetter(token) {
		return false
	}
	if strings.HasPrefix(token, "/") || strings.HasPrefix(token, "~/") ||
		strings.HasPrefix(token, "./") || strings.HasPrefix(token, "../") {
		return true
	}
	if strings.Contains(token, "/") {
		return true
	}
	ext := filepath.Ext(token)
	return ext != "" && len(ext) <= 9
}

func hasLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func resolvePathToken(token string, roots []string) (string, bool) {
	if strings.HasPrefix(token, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		token = filepath.Join(home, strings.TrimPrefix(token, "~/"))
	}
	candidates := []string{token}
	if !filepath.IsAbs(token) {
		candidates = candidates[:0]
		for _, root := range roots {
			if strings.TrimSpace(root) == "" {
				continue
			}
			candidates = append(candidates, filepath.Join(root, token))
		}
	}
	for _, candidate := range candidates {
		path, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if info, err := os.Stat(path); err == nil && (info.Mode().IsRegular() || info.IsDir()) {
			return path, true
		}
	}
	return "", false
}

func linkRoots(set workspace.Set, pane string) []string {
	var roots []string
	seen := map[string]bool{}
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		path, err := filepath.Abs(path)
		if err != nil || seen[path] {
			return
		}
		seen[path] = true
		roots = append(roots, path)
	}
	repo := set.Repository
	if repo == "" {
		repo = set.Root
	}
	addAgent := func(id protocol.AgentID) {
		if dir, ok, err := set.AgentDir(id); err == nil && ok {
			add(dir)
		}
		if wt, ok := set.For(id); ok {
			add(wt.Path)
		}
	}
	addRepo := func() {
		add(repo)
		if repo != "" && strings.TrimSpace(set.ScopePath) != "." {
			add(filepath.Join(repo, set.ScopePath))
		}
	}
	switch strings.ToLower(pane) {
	case "austin":
		addAgent(protocol.Austin)
		addRepo()
	case "tony":
		addAgent(protocol.Tony)
		addRepo()
	default:
		addRepo()
		addAgent(protocol.Austin)
		addAgent(protocol.Tony)
	}
	return roots
}

func (a *App) openLink(target linkTarget) {
	var err error
	var label string
	switch target.kind {
	case linkURL:
		if _, ok := urlTarget(target.value); !ok {
			err = fmt.Errorf("unsupported URL")
		} else if a.openURL == nil {
			err = fmt.Errorf("no URL opener configured")
		} else {
			err = a.openURL(target.value)
			label = target.value
		}
	case linkFile:
		if _, statErr := os.Stat(target.value); statErr != nil {
			err = statErr
		} else if a.openFile == nil {
			err = fmt.Errorf("no file opener configured")
		} else {
			err = a.openFile(target.value)
			label = target.value
		}
	default:
		return
	}
	if err != nil {
		a.setStatus(fmt.Sprintf(a.theme.tr("open failed: %s"), err.Error()), true)
		return
	}
	if target.kind == linkURL {
		a.setStatus(fmt.Sprintf(a.theme.tr("opened %s"), fitText(label, max(16, a.width-20))), false)
		return
	}
	a.setStatus(fmt.Sprintf(a.theme.tr("opened %s"), shortPath(label, max(16, a.width-20))), false)
}

func openURLDefault(target string) error {
	switch runtime.GOOS {
	case "darwin":
		return launch("open", target)
	case "windows":
		return launch("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		return launch("xdg-open", target)
	}
}

func openFileDefault(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if info.IsDir() {
		dir = path
	}
	switch runtime.GOOS {
	case "darwin":
		if info.IsDir() {
			return launch("open", dir)
		}
		return launch("open", "-R", path)
	case "windows":
		if info.IsDir() {
			return launch("explorer", dir)
		}
		return launch("explorer", "/select,"+path)
	default:
		return launch("xdg-open", dir)
	}
}

func launch(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// linkAt resolves the target under a transcript cell. The link travels with the
// styled run, so wrapping a URL across rows does not lose its target.
func (v *transcript) linkAt(x, y int) (linkTarget, bool) {
	line, col, ok := v.point(x, y)
	if !ok {
		return linkTarget{}, false
	}
	for _, run := range v.lines[line].runs {
		width := runsWidth([]textRun{run})
		if col < width {
			if run.link.kind != linkNone {
				return run.link, true
			}
			return linkTarget{}, false
		}
		col -= width
	}
	return linkTarget{}, false
}
