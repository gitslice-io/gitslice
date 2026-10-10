package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSliceServesGoModuleFetches covers what the go command does to fetch a
// module from a slice (design/21_self_hosting.md, "Go modules"): it drops the
// ".git" from gitslice.io/git/<account>/<slice>.git/... and asks the Git
// endpoint without it, then lists tags and fetches the module's version tag,
// prefixed with the slice's included path, shallowly.
func TestSliceServesGoModuleFetches(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	payment := t.TempDir()
	loginTestCLI(t, ts, home, payment)
	token := readToken(t, home)
	runCLI(t, home, payment, "workspace", "init", "acme/payment")

	writeWorkspaceFile(t, payment, "go.mod", "module gitslice.io/acme/payment\n\ngo 1.22\n")
	writeWorkspaceFile(t, payment, "pay.go", "package payment\n\nfunc Pay() int { return 1 }\n")
	runCLI(t, home, payment, "cs", "create", "--title", "payment module")
	runCLI(t, home, payment, "cs", "submit")
	runCLI(t, home, payment, "tag", "create", "v1.0.0", "-m", "first module release")

	withoutSuffix := "http://" + ts.gitAddr + "/git/acme/payment"
	refs := gitWithAuth(t, token, "", "ls-remote", withoutSuffix)
	if !strings.Contains(refs, "refs/tags/acme/payment/v1.0.0") || !strings.Contains(refs, "refs/tags/v1.0.0") {
		t.Fatalf("ls-remote without .git = %q, want the module's prefixed tag", refs)
	}

	// The go command's fetch of one version.
	bare := t.TempDir()
	runGit(t, bare, "init", "--bare", "-q")
	runGit(t, bare, "remote", "add", "origin", withoutSuffix)
	gitWithAuth(t, token, bare, "fetch", "-f", "--depth=1", "origin", "refs/tags/acme/payment/v1.0.0:refs/tags/acme/payment/v1.0.0")
	if out := runGit(t, bare, "show", "acme/payment/v1.0.0:acme/payment/go.mod"); !strings.Contains(out, "module gitslice.io/acme/payment") {
		t.Fatalf("go.mod at the tag = %q", out)
	}

	clone := filepath.Join(t.TempDir(), "payment")
	gitWithAuth(t, token, "", "clone", "-q", withoutSuffix, clone)
	if _, err := os.Stat(filepath.Join(clone, "acme", "payment", "pay.go")); err != nil {
		t.Fatalf("clone without .git: %v", err)
	}
}
