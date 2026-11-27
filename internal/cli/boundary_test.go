package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestCmdGSBinaryDoesNotLinkServerStorageStack(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "gitslice.io/gitslice/cmd/gs").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps gitslice.io/gitslice/cmd/gs: %v\n%s", err, out)
	}
	deps := strings.Split(strings.TrimSpace(string(out)), "\n")
	forbidden := []string{
		"gitslice.io/gitslice/server",
		"gitslice.io/gitslice/service",
		"gitslice.io/gitslice/internal/postgres",
		"gitslice.io/gitslice/internal/storage",
		"gitslice.io/gitslice/internal/automations",
		"gitslice.io/gitslice/internal/gitcompat",
		"gitslice.io/gitslice/internal/gitimport",
		"gitslice.io/gitslice/internal/gitmirror",
		"gitslice.io/gitslice/internal/objectstore/r2",
		"gitslice.io/gitslice/internal/objectstore/filesystem",
		"gitslice.io/gitslice/internal/objectstore/packed",
		"gitslice.io/gitslice/internal/objectstore/cache",
		"gitslice.io/gitslice/internal/objectstore/latency",
		"gitslice.io/gitslice/internal/treestore",
		"gitslice.io/gitslice/internal/indexworker",
		"gitslice.io/gitslice/internal/socialcard",
		"gitslice.io/gitslice/internal/events",
		"gitslice.io/gitslice/internal/auth/clerk",
		"github.com/jackc/pgx",
	}
	required := map[string]bool{
		"gitslice.io/gitslice/internal/selfupdate":        false,
		"gitslice.io/gitslice/internal/mcpserver":         false,
		"gitslice.io/gitslice/internal/mountd":            false,
		"gitslice.io/gitslice/internal/agentd":            false,
		"gitslice.io/gitslice/internal/workspace":         false,
		"gitslice.io/gitslice/internal/remoteshell":       false,
		"gitslice.io/gitslice/internal/cli/clibase":       false,
		"gitslice.io/gitslice/internal/cli/cmd/account":   false,
		"gitslice.io/gitslice/internal/cli/cmd/configcmd": false,
		"gitslice.io/gitslice/internal/cli/cmd/feedback":  false,
		"gitslice.io/gitslice/internal/cli/cmd/secret":    false,
	}
	for _, dep := range deps {
		dep = strings.TrimSpace(dep)
		if _, ok := required[dep]; ok {
			required[dep] = true
		}
		for _, bad := range forbidden {
			if dep == bad || strings.HasPrefix(dep, bad+"/") {
				t.Fatalf("cmd/gs unexpectedly depends on %q (matches forbidden %q)", dep, bad)
			}
		}
	}
	for pkg, seen := range required {
		if !seen {
			t.Fatalf("cmd/gs is missing expected extracted CLI subpackage %q", pkg)
		}
	}
}

func TestExtractedCLILibrariesHaveNoCLIDependencies(t *testing.T) {
	forbidden := []string{
		"github.com/spf13/cobra",
		"gitslice.io/gitslice/internal/cli",
		"gitslice.io/gitslice/internal/postgres",
		"gitslice.io/gitslice/internal/storage",
		"gitslice.io/gitslice/server",
		"gitslice.io/gitslice/service",
	}
	for _, pkg := range []string{
		"gitslice.io/gitslice/internal/workspace",
		"gitslice.io/gitslice/internal/remoteshell",
		"gitslice.io/gitslice/internal/agentd",
		"gitslice.io/gitslice/internal/mountd",
		"gitslice.io/gitslice/internal/lazyfs",
		"gitslice.io/gitslice/internal/mcpserver",
		"gitslice.io/gitslice/internal/clientcache",
		"gitslice.io/gitslice/internal/selfupdate",
		"gitslice.io/gitslice/internal/checkexec",
	} {
		out, err := exec.Command("go", "list", "-deps", pkg).CombinedOutput()
		if err != nil {
			t.Fatalf("go list -deps %s: %v\n%s", pkg, err, out)
		}
		for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			dep = strings.TrimSpace(dep)
			for _, bad := range forbidden {
				if dep == bad || strings.HasPrefix(dep, bad+"/") {
					t.Fatalf("%s unexpectedly depends on %q (matches forbidden %q)", pkg, dep, bad)
				}
			}
		}
	}
}

func TestCmdGSExecUsesStdlibOnly(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", "{{range .Deps}}{{.}}\n{{end}}", "gitslice.io/gitslice/cmd/gs-exec").CombinedOutput()
	if err != nil {
		t.Fatalf("go list cmd/gs-exec: %v\n%s", err, out)
	}
	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		dep = strings.TrimSpace(dep)
		if dep == "" {
			continue
		}
		if strings.Contains(dep, ".") && !strings.HasPrefix(dep, "vendor/") {
			t.Fatalf("cmd/gs-exec must remain stdlib-only to keep container image tiny, found non-stdlib dep %q", dep)
		}
	}
}

func TestNoDirectWriteJSONInCLICommands(t *testing.T) {
	allowed := map[string]bool{
		"cli.go":            true,
		"output.go":         true,
		"clibase_bridge.go": true,
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || allowed[name] {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Clean(name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "writeJSON" {
				t.Errorf("internal/cli/%s calls raw writeJSON(); use r.writeJSONOutput(opts, ...) or writeSchemaOutput so --jq/--template/--fields work", name)
			}
			return true
		})
	}
}

func TestCobraGroupCommandsRequireSubcommand(t *testing.T) {
	root := (Runner{}).rootCommand()
	var walk func(cmd *cobra.Command, path string)
	walk = func(cmd *cobra.Command, path string) {
		if len(cmd.Commands()) > 0 && path != "" && !cmd.Hidden && cmd.Name() != "help" && cmd.Name() != "completion" {
			if cmd.RunE == nil && cmd.Run == nil {
				t.Errorf("Cobra group command %q has subcommands but no RunE (missing requireSubcommand)", path)
			}
		}
		for _, sub := range cmd.Commands() {
			next := sub.Name()
			if path != "" {
				next = path + " " + next
			}
			walk(sub, next)
		}
	}
	walk(root, "")
}

func TestCLICommandSubpackagesAreIsolated(t *testing.T) {
	const (
		cliRoot   = "gitslice.io/gitslice/internal/cli"
		clibasePk = "gitslice.io/gitslice/internal/cli/clibase"
		cmdPrefix = "gitslice.io/gitslice/internal/cli/cmd/"
	)

	// clibase must not depend on internal/cli root or any cmd/* subpackage.
	baseDeps, err := exec.Command("go", "list", "-deps", clibasePk).CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps %s: %v\n%s", clibasePk, err, baseDeps)
	}
	for _, dep := range strings.Split(strings.TrimSpace(string(baseDeps)), "\n") {
		dep = strings.TrimSpace(dep)
		if dep == cliRoot || strings.HasPrefix(dep, cmdPrefix) {
			t.Fatalf("%s unexpectedly depends on %q", clibasePk, dep)
		}
	}

	// Discover every subpackage under internal/cli/cmd/... and verify each one
	// depends neither on internal/cli root nor on any sibling cmd/* package.
	pkgsOut, err := exec.Command("go", "list", "./cmd/...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list ./cmd/...: %v\n%s", err, pkgsOut)
	}
	pkgs := strings.Fields(string(pkgsOut))
	if len(pkgs) == 0 {
		t.Fatal("expected at least one package under internal/cli/cmd/...")
	}
	for _, pkg := range pkgs {
		depsOut, err := exec.Command("go", "list", "-deps", pkg).CombinedOutput()
		if err != nil {
			t.Fatalf("go list -deps %s: %v\n%s", pkg, err, depsOut)
		}
		for _, dep := range strings.Split(strings.TrimSpace(string(depsOut)), "\n") {
			dep = strings.TrimSpace(dep)
			if dep == cliRoot {
				t.Fatalf("command subpackage %s unexpectedly depends on %s", pkg, cliRoot)
			}
			if strings.HasPrefix(dep, cmdPrefix) && dep != pkg {
				t.Fatalf("command subpackage %s unexpectedly depends on sibling %s", pkg, dep)
			}
		}
	}
}
