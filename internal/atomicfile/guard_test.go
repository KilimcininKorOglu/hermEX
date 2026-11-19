package atomicfile

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// directWrites are, per import path, the calls that create or replace a file
// without this package's temporary-file, sync and rename sequence.
var directWrites = map[string]map[string]bool{
	"os":        {"WriteFile": true, "Create": true, "CreateTemp": true, "Rename": true, "OpenFile": true},
	"io/ioutil": {"WriteFile": true, "TempFile": true},
}

// allowedDirectWrites names each production file that may still call one of
// directWrites, with the reason it is not a content replacement.
var allowedDirectWrites = map[string]string{
	"internal/atomicfile/atomicfile.go": "the helper itself",
	"internal/objectstore/lock.go":      "opens the advisory lock file, whose content is never read",
	"internal/objectstore/integrity.go": "opens the advisory lock file, whose content is never read",
	"internal/initlock/initlock.go":     "opens the advisory lock file, whose content is never read",
	"internal/logging/spill.go":         "an append-only log; a torn tail line is skipped on replay",
	"internal/objectstore/recover.go":   "moves the damaged database aside; the rebuilt one goes through SyncRename",
	"internal/tlstest/tlstest.go":       "test-support certificates in a test's own temporary directory",
}

// TestNoDirectFileWrites keeps every write path on this package: it parses the
// production Go files under internal/ and cmd/ and fails on a direct os write
// call outside the allowed files, so reverting any converted site fails the gate.
func TestNoDirectFileWrites(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, dir := range []string{"internal", "cmd"} {
		if err := filepath.WalkDir(filepath.Join(root, dir), visitor(t, root)); err != nil {
			t.Fatal(err)
		}
	}
}

// visitor checks each production Go file a walk of root meets.
func visitor(t *testing.T, root string) fs.WalkDirFunc {
	return func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == "testdata") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		checkFile(t, path, filepath.ToSlash(rel))
		return nil
	}
}

// checkFile reports each direct os write call in one file that is not allowed.
func checkFile(t *testing.T, path, rel string) {
	t.Helper()
	if _, ok := allowedDirectWrites[rel]; ok {
		return
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	names := writeImports(t, fset, f)
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && directWrites[names[pkg.Name]][sel.Sel.Name] {
			t.Errorf("%s: %s.%s writes a file directly; use atomicfile", fset.Position(sel.Pos()), names[pkg.Name], sel.Sel.Name)
		}
		return true
	})
}

// writeImports maps the name each guarded package is called by in f, its own or
// an alias, to the package's import path, so a renamed import cannot hide a
// direct write. A dot import would hide the package name altogether and fails.
func writeImports(t *testing.T, fset *token.FileSet, f *ast.File) map[string]string {
	t.Helper()
	names := map[string]string{}
	for _, imp := range f.Imports {
		p := strings.Trim(imp.Path.Value, `"`)
		if directWrites[p] == nil {
			continue
		}
		name := filepath.Base(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == "." {
			t.Errorf("%s: dot import of %s hides direct file writes from this check", fset.Position(imp.Pos()), p)
		}
		names[name] = p
	}
	return names
}
