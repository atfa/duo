package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/clidoc"
)

// The Help screen is the only in-app reference for the composer commands, the
// keybindings and the command line, so it is asserted against the code that
// implements them: a new slash command, a newly routed key, or a new duo
// command must reach helpLines or these tests fail.
func TestHelpDocumentsEverySlashCommand(t *testing.T) {
	help := strings.Join(newTestApp().helpLines(200), "\n")
	for _, command := range slashCommandList {
		if !strings.Contains(help, command.Name) {
			t.Errorf("Help does not document the %s command", command.Name)
		}
	}
	// /exit is handled by runSlashCommand as an alias of /quit but has no
	// palette entry, so the Help text is the only place it can be found.
	if !strings.Contains(help, "/exit") {
		t.Error("Help does not document the /exit alias")
	}
}

// mainFooter derives the always-on hint line from keyBindings, so a binding
// that carries a ShortLabel must actually reach the footer.
func TestMainFooterCoversShortLabels(t *testing.T) {
	footer := mainFooter()
	for _, binding := range keyBindings {
		if binding.ShortLabel == "" {
			continue
		}
		if !strings.Contains(footer, binding.ShortLabel) {
			t.Errorf("footer %q is missing the short label %q", footer, binding.ShortLabel)
		}
	}
}

func TestHelpDocumentsEveryCommandLineSignature(t *testing.T) {
	help := strings.Join(newTestApp().helpLines(400), "\n")
	// One line per duo command, from the table `duo --help` also renders, plus
	// the alias and pointer lines helpCommandLineLines appends.
	want := append(clidoc.SignatureLines(),
		"The bare word 'resume' is an alias of --resume.",
		"Run duo --help for every flag, its default, and the DUO_* environment variables.",
	)
	for _, line := range want {
		if !strings.Contains(help, line) {
			t.Errorf("Help does not document the command line %q", line)
		}
	}
}

// overlayOnlyKeys are routed by the Help and session-overview handler. They are
// documented by the "Help navigation" block of the Keyboard section rather than
// by a keyBinding row, so they carry no Dispatch entry.
var overlayOnlyKeys = map[string]string{
	"k": "vim alias for ↑ in the Help/overview scroll",
	"j": "vim alias for ↓ in the Help/overview scroll",
	"g": "vim alias for Home in the Help/overview scroll",
	"G": "vim alias for End in the Help/overview scroll",
}

// TestEveryRoutedKeyIsDocumented walks the case labels of the two key routers,
// so binding a new key in the TUI without documenting it in Help fails here.
func TestEveryRoutedKeyIsDocumented(t *testing.T) {
	documented := map[string]bool{}
	for _, group := range [][]keyBinding{keyBindings, modelPickerKeys} {
		for _, binding := range group {
			for _, key := range binding.Dispatch {
				documented[key] = true
			}
		}
	}
	for _, key := range routedKeys(t) {
		if documented[key] {
			continue
		}
		if reason, ok := overlayOnlyKeys[key]; ok {
			if reason == "" {
				t.Errorf("key %q is exempt from documentation with no reason", key)
			}
			continue
		}
		t.Errorf("key %q is routed by the TUI but no keyBinding.Dispatch entry documents it", key)
	}
}

// TestEveryDetachKeyIsDocumented checks the three keys that return from a
// native Pi attach. They are matched by codepoint rather than by a normalised
// key name, so they carry no Dispatch entry.
func TestEveryDetachKeyIsDocumented(t *testing.T) {
	labels := map[int64]string{92: "Ctrl+\\", 93: "Ctrl+]", 12305: "Ctrl+】"}
	documented := strings.Join(newTestApp().helpLines(200), "\n")
	for _, codepoint := range detachCodepoints(t) {
		label, ok := labels[codepoint]
		if !ok {
			t.Errorf("native Pi detach codepoint %d is not documented in Help", codepoint)
			continue
		}
		if !strings.Contains(documented, label) {
			t.Errorf("Help does not document the native Pi detach key %s", label)
		}
	}
}

// routedKeys returns every string case label of handleKey and handleModelKey,
// the key tokens the app actually dispatches on.
func routedKeys(t *testing.T) []string {
	t.Helper()
	return collectCaseLabels(t, map[string]bool{"handleKey": true, "handleModelKey": true})
}

// detachCodepoints returns the codepoints nativeDetachCodepoint accepts.
func detachCodepoints(t *testing.T) []int64 {
	t.Helper()
	fset := token.NewFileSet()
	for _, path := range packageFiles(t, fset) {
		if !strings.HasSuffix(path, "app.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		var out []int64
		ast.Inspect(file, func(n ast.Node) bool {
			decl, ok := n.(*ast.FuncDecl)
			if !ok || decl.Name.Name != "nativeDetachCodepoint" {
				return true
			}
			ast.Inspect(decl, func(m ast.Node) bool {
				lit, ok := m.(*ast.BasicLit)
				if !ok || lit.Kind != token.INT {
					return true
				}
				if v, err := strconv.ParseInt(lit.Value, 10, 64); err == nil {
					out = append(out, v)
				}
				return true
			})
			return false
		})
		if len(out) > 0 {
			return out
		}
	}
	t.Fatal("nativeDetachCodepoint not found")
	return nil
}

func collectCaseLabels(t *testing.T, want map[string]bool) []string {
	t.Helper()
	fset := token.NewFileSet()
	var out []string
	for _, path := range packageFiles(t, fset) {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			decl, ok := decl.(*ast.FuncDecl)
			if !ok || !want[decl.Name.Name] || decl.Body == nil {
				continue
			}
			ast.Inspect(decl.Body, func(n ast.Node) bool {
				clause, ok := n.(*ast.CaseClause)
				if !ok {
					return true
				}
				for _, expr := range clause.List {
					lit, ok := expr.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					if key, err := strconv.Unquote(lit.Value); err == nil {
						out = append(out, key)
					}
				}
				return true
			})
		}
	}
	return out
}

func packageFiles(t *testing.T, fset *token.FileSet) []string {
	t.Helper()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	var out []string
	for _, pkg := range pkgs {
		if pkg.Name != "tui" {
			continue
		}
		for path := range pkg.Files {
			if !strings.HasSuffix(path, "_test.go") {
				out = append(out, path)
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no tui source files found")
	}
	return out
}
