package server

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type goListPackage struct {
	ImportPath string   `json:"ImportPath"`
	Imports    []string `json:"Imports"`
}

func repoRootFromServer(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, ".."))
}

func listModulePackages(t *testing.T, root string) []goListPackage {
	t.Helper()
	cmd := exec.Command("go", "list", "-json", "./cmd/...", "./server/...", "./service/...", "./internal/...", "./proto/...")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list failed: %v\n%s", err, out)
	}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	var pkgs []goListPackage
	for dec.More() {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode go list package: %v", err)
		}
		pkgs = append(pkgs, pkg)
	}
	return pkgs
}

func TestGoBackendArchitectureBoundaries(t *testing.T) {
	root := repoRootFromServer(t)
	const mod = "gitslice.io/gitslice/"

	// 1. Zero dead grpc-gateway generated files under proto/.
	err := filepath.WalkDir(filepath.Join(root, "proto"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".pb.gw.go") {
			t.Errorf("dead grpc-gateway stub found (ConnectRPC is canonical): %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk proto/: %v", err)
	}

	concreteObjectStoreDrivers := []string{
		mod + "internal/objectstore/filesystem",
		mod + "internal/objectstore/r2",
		mod + "internal/objectstore/cache",
		mod + "internal/objectstore/packed",
		mod + "internal/objectstore/latency",
	}

	pkgs := listModulePackages(t, root)
	for _, pkg := range pkgs {
		rel := strings.TrimPrefix(pkg.ImportPath, mod)

		// 2. Zero grpc-gateway imports anywhere in the module.
		for _, imp := range pkg.Imports {
			if strings.HasPrefix(imp, "github.com/grpc-ecosystem/grpc-gateway") {
				t.Errorf("package %s imports deprecated grpc-gateway: %s", rel, imp)
			}
		}

		// 3. No internal/* package imports service/, server/, or cmd/.
		if strings.HasPrefix(rel, "internal/") {
			for _, imp := range pkg.Imports {
				impRel := strings.TrimPrefix(imp, mod)
				if impRel == "service" || strings.HasPrefix(impRel, "service/") ||
					impRel == "server" || strings.HasPrefix(impRel, "server/") ||
					strings.HasPrefix(impRel, "cmd/") {
					t.Errorf("layer violation: %s must not import %s", rel, impRel)
				}
			}
			// Concrete objectstore drivers must not leak into other internal/* packages.
			// Exception: internal/postgres implements packed.Index (PackedObjectStore).
			if !strings.HasPrefix(rel, "internal/objectstore/") {
				for _, imp := range pkg.Imports {
					if rel == "internal/postgres" && imp == mod+"internal/objectstore/packed" {
						continue
					}
					if slices.Contains(concreteObjectStoreDrivers, imp) {
						t.Errorf("driver leak: %s must use internal/objectstore instead of %s", rel, strings.TrimPrefix(imp, mod))
					}
				}
			}
		}

		// 4. service/ must not import server/, cmd/, internal/postgres, or concrete objectstore drivers.
		if rel == "service" || strings.HasPrefix(rel, "service/") {
			for _, imp := range pkg.Imports {
				impRel := strings.TrimPrefix(imp, mod)
				if impRel == "server" || strings.HasPrefix(impRel, "server/") ||
					strings.HasPrefix(impRel, "cmd/") ||
					impRel == "internal/postgres" || strings.HasPrefix(impRel, "internal/postgres/") ||
					slices.Contains(concreteObjectStoreDrivers, imp) {
					t.Errorf("layer violation: %s must not import %s", rel, impRel)
				}
			}
		}

		// 5. internal/storage must not depend on postgres, events, automations, service, or server.
		if rel == "internal/storage" {
			forbidden := []string{
				mod + "internal/postgres",
				mod + "internal/events",
				mod + "internal/automations",
				mod + "service",
				mod + "server",
			}
			for _, imp := range pkg.Imports {
				for _, f := range forbidden {
					if imp == f || strings.HasPrefix(imp, f+"/") {
						t.Errorf("layer violation: internal/storage must not import %s", strings.TrimPrefix(imp, mod))
					}
				}
			}
		}
	}
}

func TestServerPackageWiringOnlyInvariants(t *testing.T) {
	root := repoRootFromServer(t)
	serverDir := filepath.Join(root, "server")
	entries, err := os.ReadDir(serverDir)
	if err != nil {
		t.Fatalf("readdir server/: %v", err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		filePath := filepath.Join(serverDir, name)
		f, err := parser.ParseFile(fset, filePath, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			impPath := strings.Trim(imp.Path.Value, `"`)
			if impPath == "database/sql" {
				t.Errorf("server/%s must not import database/sql directly", name)
			}
			if impPath == "gitslice.io/gitslice/internal/postgres" && name != "server.go" {
				t.Errorf("server/%s must not import internal/postgres (only composition root server.go may)", name)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if ok && id.Name == "activity" && sel.Sel.Name == "Default" {
				t.Errorf("server/%s references global activity.Default; inject per-App *activity.Gate instead", name)
			}
			return true
		})
	}
}
