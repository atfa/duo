package guiipc

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The GUI host is the second frontend, so it is where the seam is most tempting
// to widen: the shortest way to render agent activity is to hold the objects that
// own it. These checks keep the widening from happening by accident. They are a
// parser check rather than a code review convention because the failure mode is
// one forgotten import, and a comment cannot stop it.
//
// They mirror internal/tui/decoupling_test.go deliberately: the terminal
// interface is held to the same boundary, and a rule enforced on only one of two
// frontends is not a boundary.

const (
	hostDir = "internal/guiipc"
	tuiDir  = "internal/tui"
)

// forbiddenImports own live session state, a socket, or the delivery handoff, and
// have no value type the host needs. The terminal interface is on the list too:
// the GUI protocol is the only coupling between the two frontends, and importing
// one from the other would make the protocol a decoration rather than the
// boundary.
var forbiddenImports = []string{
	"github.com/atfa/duo/internal/tui",
	"github.com/atfa/duo/internal/coordinator",
	"github.com/atfa/duo/internal/transport",
	"github.com/atfa/duo/internal/delivery",
	"github.com/atfa/duo/internal/recovery",
}

// liveNames are the live objects inside packages the host may still import for a
// value type. session.View, session.Command and sessionstore.TUIEntry are values;
// session.Service is the thing that owns the workflow, and sessionstore.Store is
// the thing that owns the log.
var liveNames = map[string][]string{
	"github.com/atfa/duo/internal/session":      {"Service", "New"},
	"github.com/atfa/duo/internal/sessionstore": {"Store", "New", "Open", "NewLogWriter", "LogWriter", "Logger", "EventLog", "Lock"},
	"github.com/atfa/duo/internal/workspace":    {"Manager", "NewGitManager"},
	"github.com/atfa/duo/internal/agent":        {"Manager", "NewManager", "NewSession", "Session"},
	"github.com/atfa/duo/internal/harness":      {"NewTracker", "Tracker"},
	"github.com/atfa/duo/internal/project":      {"State", "NewState", "NewStateFor"},
}

// source is one parsed file together with the local name it gave each import, so a
// renamed import cannot smuggle a forbidden symbol past the checks.
type source struct {
	name    string
	file    *ast.File
	aliases map[string]string
}

// resolve turns a selector's package identifier into the import path it stands
// for, which is what makes an aliased import visible.
func (s source) resolve(pkg *ast.Ident) string {
	if path, ok := s.aliases[pkg.Name]; ok {
		return path
	}
	return pkg.Name
}

// parseDir parses every non-test Go file in dir.
func parseDir(t *testing.T, dir string) []source {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	var out []source
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		aliases := map[string]string{}
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			local := path
			if idx := strings.LastIndex(local, "/"); idx >= 0 {
				local = local[idx+1:]
			}
			if imp.Name != nil {
				local = imp.Name.Name
			}
			aliases[local] = path
		}
		out = append(out, source{name: name, file: file, aliases: aliases})
	}
	return out
}

// TestHostDoesNotImportTheSessionOwners is the load-bearing half: a package that
// cannot name the coordinator cannot hold one.
func TestHostDoesNotImportTheSessionOwners(t *testing.T) {
	sources := parseDir(t, ".")
	if len(sources) == 0 {
		t.Fatal("no host sources found, so this guard proves nothing")
	}
	for _, src := range sources {
		for _, imp := range src.file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			for _, forbidden := range forbiddenImports {
				if path == forbidden {
					t.Errorf("%s/%s imports %s; the host must read a projection, not its owner", hostDir, src.name, path)
				}
			}
			// A dot import would make every live name unqualified and invisible to
			// the selector check below.
			if imp.Name != nil && imp.Name.Name == "." {
				if _, ok := liveNames[path]; ok {
					t.Errorf("%s/%s dot-imports %s; the host must qualify what it uses", hostDir, src.name, path)
				}
			}
		}
	}
}

// TestHostDoesNotReachForLiveObjects closes the other half: an import may be
// legitimate for a value type while the object inside it is not.
func TestHostDoesNotReachForLiveObjects(t *testing.T) {
	for _, src := range parseDir(t, ".") {
		ast.Inspect(src.file, func(node ast.Node) bool {
			sel, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			path := src.resolve(pkg)
			for _, name := range liveNames[path] {
				if sel.Sel.Name == name {
					t.Errorf("%s/%s names %s.%s; the host depends on the Session seam, not the live object",
						hostDir, src.name, pkg.Name, name)
				}
			}
			return true
		})
	}
}

// TestTerminalInterfaceDoesNotImportTheHost is the reverse direction. Without it,
// the terminal interface could start rendering the GUI protocol and the two
// frontends would be coupled through an import rather than through duplicating
// what they each need from the session.
func TestTerminalInterfaceDoesNotImportTheHost(t *testing.T) {
	const hostImport = "github.com/atfa/duo/internal/guiipc"
	sources := parseDir(t, filepath.Join("..", "tui"))
	if len(sources) == 0 {
		t.Fatal("no terminal interface sources found, so this guard proves nothing")
	}
	for _, src := range sources {
		for _, imp := range src.file.Imports {
			if strings.Trim(imp.Path.Value, `"`) == hostImport {
				t.Errorf("%s/%s imports %s; the two frontends are separate readers of one session seam",
					tuiDir, src.name, hostImport)
			}
		}
	}
}

// TestHostDependsOnTheValuePackagesItClaims pins the imports the guard allows, so
// the package cannot quietly grow a dependency while the parsed set above stays
// green.
func TestHostDependsOnTheValuePackagesItClaims(t *testing.T) {
	allowed := map[string]bool{
		"bufio":                                 true,
		"bytes":                                 true,
		"context":                               true,
		"encoding/json":                         true,
		"fmt":                                   true,
		"io":                                    true,
		"os/exec":                               true,
		"strconv":                               true,
		"strings":                               true,
		"sync":                                  true,
		"time":                                  true,
		"errors":                                true,
		"github.com/atfa/duo/internal/events":   true,
		"github.com/atfa/duo/internal/harness":  true,
		"github.com/atfa/duo/internal/models":   true,
		"github.com/atfa/duo/internal/protocol": true,
		"github.com/atfa/duo/internal/project":  true,
		"github.com/atfa/duo/internal/session":  true,
		"github.com/atfa/duo/internal/sessionstore": true,
	}
	for _, src := range parseDir(t, ".") {
		for _, imp := range src.file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if !allowed[path] {
				t.Errorf("%s/%s imports %s, which this guard does not know about; add it deliberately or drop it",
					hostDir, src.name, path)
			}
		}
	}
}
