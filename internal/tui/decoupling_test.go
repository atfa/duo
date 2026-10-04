package tui

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"strings"
	"testing"
)

// The interface used to reach straight into Duo Core: it held the coordinator, the
// workflow state, the agent manager, the bridge and the tracker, and it mutated
// some of them from the render path. That is what made a second frontend
// impossible, because any other frontend would have had to reproduce those
// mutations to behave the same.
//
// These checks keep the seam closed. They are a parser check rather than a
// convention because the failure mode is one forgotten import, and a comment
// cannot stop it.

const tuiDir = "internal/tui"

// forbiddenImports own live session state, a socket, or the delivery handoff. A
// frontend may read a projection of the session, but holding the owner itself
// means two owners of the workflow, and two owners disagree.
var forbiddenImports = []string{
	"github.com/atfa/duo/internal/coordinator",
	"github.com/atfa/duo/internal/transport",
	"github.com/atfa/duo/internal/delivery",
	"github.com/atfa/duo/internal/recovery",
}

// forbiddenSelectors are the live objects inside packages the interface may still
// import for a value type: workspace.Set is a projection of the Git worktrees,
// workspace.Manager is the thing that makes them.
var forbiddenSelectors = []string{
	"workspace.Manager",
	"agent.Manager",
	"agent.NewManager",
	"agent.NewSession",
	"harness.NewTracker",
	"session.Service",
	"session.New",
}

// tuiSources parses every non-test Go file in the interface package.
func tuiSources(t *testing.T) map[string]*ast.File {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read %s: %v", tuiDir, err)
	}
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files[name] = file
	}
	if len(files) == 0 {
		t.Fatal("no interface sources found, so this guard proves nothing")
	}
	return files
}

// TestInterfaceDoesNotImportTheSessionOwners is the load-bearing half: a package
// that cannot name the coordinator cannot hold one.
func TestInterfaceDoesNotImportTheSessionOwners(t *testing.T) {
	for name, file := range tuiSources(t) {
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			for _, forbidden := range forbiddenImports {
				if path == forbidden {
					t.Errorf("%s/%s imports %s; the interface must read a projection, not its owner", tuiDir, name, path)
				}
			}
		}
	}
}

// TestInterfaceDoesNotReachForLiveObjects closes the other half: an import may be
// legitimate for a value type while the object inside it is not.
func TestInterfaceDoesNotReachForLiveObjects(t *testing.T) {
	for name, file := range tuiSources(t) {
		ast.Inspect(file, func(node ast.Node) bool {
			sel, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			qualified := pkg.Name + "." + sel.Sel.Name
			for _, forbidden := range forbiddenSelectors {
				if qualified == forbidden {
					t.Errorf("%s/%s names %s; the interface depends on the Session seam, not the live object",
						tuiDir, name, qualified)
				}
			}
			return true
		})
	}
}

// TestAppHoldsOnlyTheSeam pins the field list, which is what a second frontend has
// to be able to write for itself. The import checks above would not notice a field
// typed by an alias or by a struct in a permitted package.
func TestAppHoldsOnlyTheSeam(t *testing.T) {
	file, ok := tuiSources(t)["model.go"]
	if !ok {
		t.Fatal("model.go is gone; this guard needs a new home")
	}

	var app *ast.StructType
	ast.Inspect(file, func(node ast.Node) bool {
		decl, ok := node.(*ast.GenDecl)
		if !ok || decl.Tok != token.TYPE {
			return true
		}
		for _, spec := range decl.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "App" {
				continue
			}
			app, _ = ts.Type.(*ast.StructType)
		}
		return true
	})
	if app == nil {
		t.Fatal("no App struct found, so this guard proves nothing")
	}

	// Types that belong to the workflow or to the process it runs, not to a view.
	owners := []string{
		"coordinator.", "transport.", "workspace.Manager", "delivery.", "recovery.",
		"harness.Tracker", "agent.Manager", "agent.Session", "session.Service",
	}
	for _, field := range app.Fields.List {
		var buf bytes.Buffer
		if err := printer.Fprint(&buf, token.NewFileSet(), field.Type); err != nil {
			t.Fatalf("print App field: %v", err)
		}
		rendered := buf.String()
		for _, owner := range owners {
			if strings.Contains(rendered, owner) {
				for _, name := range field.Names {
					t.Errorf("App.%s has type %s; the interface must hold the Session seam and its own view state",
						name.Name, rendered)
				}
			}
		}
	}

	// And it must hold the seam, not the concrete service.
	for _, field := range app.Fields.List {
		var buf bytes.Buffer
		if err := printer.Fprint(&buf, token.NewFileSet(), field.Type); err != nil {
			t.Fatalf("print App field: %v", err)
		}
		for _, name := range field.Names {
			if name.Name == "svc" && buf.String() != "Session" {
				t.Errorf("App.svc has type %s; it must be the Session interface so a frontend can be driven by a test",
					buf.String())
			}
		}
	}
}
