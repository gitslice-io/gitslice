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
		"gitslice.io/gitslice/internal/selfupdate": false,
		"gitslice.io/gitslice/internal/mcpserver":  false,
		"gitslice.io/gitslice/internal/mountd":     false,
		"gitslice.io/gitslice/internal/agentd":     false,
		"gitslice.io/gitslice/internal/workspace":  false,
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

func TestInternalWorkspaceHasNoCLIDependencies(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "gitslice.io/gitslice/internal/workspace").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps gitslice.io/gitslice/internal/workspace: %v\n%s", err, out)
	}
	forbidden := []string{
		"github.com/spf13/cobra",
		"gitslice.io/gitslice/internal/cli",
		"gitslice.io/gitslice/internal/postgres",
		"gitslice.io/gitslice/internal/storage",
	}
	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		dep = strings.TrimSpace(dep)
		for _, bad := range forbidden {
			if dep == bad || strings.HasPrefix(dep, bad+"/") {
				t.Fatalf("internal/workspace unexpectedly depends on %q (matches forbidden %q)", dep, bad)
			}
		}
	}
}
