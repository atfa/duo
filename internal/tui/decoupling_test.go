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

// forbiddenImports own live session state, a socket, or the delivery handoff, and
// have no value type the interface needs. Importing one is the whole failure: a
// frontend that can name the coordinator can hold one.
var forbiddenImports = []string{
	"github.com/atfa/duo/internal/coordinator",
	"github.com/atfa/duo/internal/transport",
	"github.com/atfa/duo/internal/delivery",
	"github.com/atfa/duo/internal/recovery",
}

// liveNames are the live objects inside packages the interface may still import
// for a value type. workspace.Set is a projection of the Git worktrees;
// workspace.Manager is the thing that makes them. agent.ProcessState is an enum;
// agent.Manager owns the processes.
var liveNames = map[string][]string{
	"github.com/atfa/duo/internal/workspace": {"Manager", "NewGitManager"},
	"github.com/atfa/duo/internal/agent":     {"Manager", "NewManager", "NewSession", "Session"},
	"github.com/atfa/duo/internal/harness":   {"NewTracker", "Tracker"},
	"github.com/atfa/duo/internal/session":   {"Service", "New"},
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

// tuiSources parses every non-test Go file in the interface package.
func tuiSources(t *testing.T) []source {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read %s: %v", tuiDir, err)
	}
	fset := token.NewFileSet()
	var out []source
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
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
	if len(out) == 0 {
		t.Fatal("no interface sources found, so this guard proves nothing")
	}
	return out
}

// TestInterfaceDoesNotImportTheSessionOwners is the load-bearing half: a package
// that cannot name the coordinator cannot hold one.
func TestInterfaceDoesNotImportTheSessionOwners(t *testing.T) {
	for _, src := range tuiSources(t) {
		for _, imp := range src.file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			for _, forbidden := range forbiddenImports {
				if path == forbidden {
					t.Errorf("%s/%s imports %s; the interface must read a projection, not its owner", tuiDir, src.name, path)
				}
			}
			// A dot import would make every live name unqualified and invisible to
			// the selector check below.
			if imp.Name != nil && imp.Name.Name == "." {
				for path := range liveNames {
					if strings.Trim(imp.Path.Value, `"`) == path {
						t.Errorf("%s/%s dot-imports %s; the interface must qualify what it uses",
							tuiDir, src.name, path)
					}
				}
			}
		}
	}
}

// TestInterfaceDoesNotReachForLiveObjects closes the other half: an import may be
// legitimate for a value type while the object inside it is not.
func TestInterfaceDoesNotReachForLiveObjects(t *testing.T) {
	for _, src := range tuiSources(t) {
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
					t.Errorf("%s/%s names %s.%s; the interface depends on the Session seam, not the live object",
						tuiDir, src.name, pkg.Name, name)
				}
			}
			return true
		})
	}
}

// TestAppHoldsOnlyTheSeam pins the field list, which is what a second frontend has
// to be able to write for itself. The checks above would not notice a field typed
// by an alias or by a struct in a permitted package.
func TestAppHoldsOnlyTheSeam(t *testing.T) {
	var model source
	found := false
	for _, src := range tuiSources(t) {
		if src.name == "model.go" {
			model, found = src, true
		}
	}
	if !found {
		t.Fatal("model.go is gone; this guard needs a new home")
	}

	var app *ast.StructType
	ast.Inspect(model.file, func(node ast.Node) bool {
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

	for _, field := range app.Fields.List {
		var buf bytes.Buffer
		if err := printer.Fprint(&buf, token.NewFileSet(), field.Type); err != nil {
			t.Fatalf("print App field: %v", err)
		}
		rendered := buf.String()
		for _, name := range field.Names {
			if offender := model.liveTypeIn(field.Type); offender != "" {
				t.Errorf("App.%s has type %s; the interface must hold the Session seam and its own view state",
					name.Name, offender)
			}
			if name.Name == "svc" && rendered != "Session" {
				t.Errorf("App.svc has type %s; it must be the Session interface so a frontend can be driven by a test",
					rendered)
			}
		}
	}
}

// liveTypeIn reports the first live object named anywhere in a type expression,
// resolving import aliases and looking through pointers, slices and maps.
func (s source) liveTypeIn(expr ast.Expr) string {
	var offender string
	ast.Inspect(expr, func(node ast.Node) bool {
		if offender != "" {
			return false
		}
		sel, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		path := s.resolve(pkg)
		for _, forbidden := range forbiddenImports {
			if path == forbidden {
				offender = pkg.Name + "." + sel.Sel.Name
			}
		}
		for _, name := range liveNames[path] {
			if sel.Sel.Name == name {
				offender = pkg.Name + "." + sel.Sel.Name
			}
		}
		return true
	})
	return offender
}
