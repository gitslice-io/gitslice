package cli

import (
	"os/exec"
	"strings"
	"testing"
)

func TestCmdGSBinaryDoesNotLinkServerStorageStack(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "gitslice.io/gitslice/cmd/gs").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps gitslice.io/gitslice/cmd/gs: %v\n%s", err, out)
	}
	deps := strings.Split(strings.TrimSpace(string(out)), "\n")
	forbidden := []string{
		"gitslice.io/gitslice/internal/postgres",
		"gitslice.io/gitslice/internal/storage",
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
	}
	for _, pkg := range []string{
		"gitslice.io/gitslice/internal/workspace",
		"gitslice.io/gitslice/internal/remoteshell",
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
