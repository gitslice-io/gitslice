package cli_test

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/gitslice-io/gitslice/internal/auth/servicetoken"
	"github.com/gitslice-io/gitslice/internal/cli"
	"github.com/gitslice-io/gitslice/internal/postgres"
	"github.com/gitslice-io/gitslice/proto/core/v1"
	"github.com/gitslice-io/gitslice/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	grpcstatus "google.golang.org/grpc/status"
)

func TestMinimalCLIJourney(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	_, subjectID := loginTestCLI(t, ts, home, workspace)
	authStatus := runCLI(t, home, workspace, "auth", "status")
	if !strings.Contains(authStatus, "signed in as "+subjectID) || !strings.Contains(authStatus, "server: "+ts.addr) {
		t.Fatalf("unexpected auth status:\n%s", authStatus)
	}
	authStatusJSON := runCLI(t, home, workspace, "auth", "status", "--json")
	if strings.Contains(authStatusJSON, "token") {
		t.Fatalf("auth status leaked token data:\n%s", authStatusJSON)
	}
	runCLI(t, home, workspace, "init", "acme/payment")
	status := runCLI(t, home, workspace, "status")
	if !strings.Contains(status, "status: clean") {
		t.Fatalf("expected clean status, got:\n%s", status)
	}
	writeWorkspaceFile(t, workspace, "app.go", "package payment\n")
	status = runCLI(t, home, workspace, "status")
	if !strings.Contains(status, "/acme/payment/app.go") {
		t.Fatalf("expected app.go to be dirty, got:\n%s", status)
	}
	largeContent := bytes.Repeat([]byte("large streaming workspace file\n"), 180000)
	writeWorkspaceFileBytes(t, workspace, "large.txt", largeContent)
	runCLI(t, home, workspace, "cs", "create")
	runCLI(t, home, workspace, "cs", "submit")
	csStatus := runCLI(t, home, workspace, "cs", "status")
	if !strings.Contains(csStatus, "status: submitted") {
		t.Fatalf("expected submitted changeset, got:\n%s", csStatus)
	}
	metricsText := httpGet(t, ts.httpAddr, "/metrics")
	assertMetricPositive(t, metricsText, `gitslice_submit_total{result="accepted",reason="none"}`)
	assertMetricPositive(t, metricsText, `gitslice_publish_batches_total{result="success"}`)
	assertMetricPositive(t, metricsText, "gitslice_published_changesets_total")
	status = runCLI(t, home, workspace, "status")
	if !strings.Contains(status, "status: clean") {
		t.Fatalf("expected clean status after submit, got:\n%s", status)
	}
	hydratedWorkspace := t.TempDir()
	runCLI(t, home, hydratedWorkspace, "init", "acme/payment")
	gotLarge, err := os.ReadFile(filepath.Join(hydratedWorkspace, "acme", "payment", "large.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotLarge, largeContent) {
		t.Fatalf("hydrated large file length = %d, want %d", len(gotLarge), len(largeContent))
	}
}

func TestWorkspaceInitHydrateUsesGlobalClientObjectCache(t *testing.T) {
	ts := startTestServer(t)
	seedHome := t.TempDir()
	seedWorkspace := t.TempDir()
	loginTestCLI(t, ts, seedHome, seedWorkspace)
	runCLI(t, seedHome, seedWorkspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, seedWorkspace, "cached.go", "package payment\nconst Cached = true\n")
	runCLI(t, seedHome, seedWorkspace, "cs", "create", "--title", "seed cached")
	runCLI(t, seedHome, seedWorkspace, "cs", "submit")

	home := t.TempDir()
	firstWorkspace := t.TempDir()
	secondWorkspace := t.TempDir()
	loginTestCLI(t, ts, home, firstWorkspace)
	first := runCLI(t, home, firstWorkspace, "workspace", "init", "acme/payment")
	if !strings.Contains(first, "hydrated 1 file(s) through cache (0 hit(s), 1 miss(es))") {
		t.Fatalf("expected first hydrate to miss cache, got:\n%s", first)
	}
	assertWorkspaceFile(t, firstWorkspace, "acme/payment/cached.go", "package payment\nconst Cached = true\n")

	second := runCLI(t, home, secondWorkspace, "workspace", "init", "acme/payment")
	if !strings.Contains(second, "hydrated 1 file(s) through cache (1 hit(s), 0 miss(es))") {
		t.Fatalf("expected second hydrate to hit cache, got:\n%s", second)
	}
	assertWorkspaceFile(t, secondWorkspace, "acme/payment/cached.go", "package payment\nconst Cached = true\n")
}

func TestWorkspaceInitMaterializesCanonicalLayoutAndRequiresEmptyDirectory(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	outsideWorkspace := t.TempDir()
	workspace := t.TempDir()
	nonEmptyWorkspace := t.TempDir()

	runCLISignupThroughWeb(t, home, outsideWorkspace, ts, "init_user")
	runCLI(t, home, outsideWorkspace, "fs", "mkdir", "/init-user/hello")
	runCLI(t, home, outsideWorkspace, "fs", "mkdir", "/init-user/hello/empty")
	runCLI(t, home, outsideWorkspace, "fs", "write", "/init-user/hello/readme.txt", "--text", "hello workspace\n")
	runCLI(t, home, outsideWorkspace, "slice", "create", "init-user/hello", "--include", "/init-user/hello")

	initOutput := runCLI(t, home, workspace, "init", "init-user/hello")
	if !strings.Contains(initOutput, "hydrated 1 file(s) through cache") {
		t.Fatalf("unexpected workspace init output:\n%s", initOutput)
	}
	assertWorkspaceMetadataDoesNotContainToken(t, workspace, readToken(t, home))
	assertWorkspaceFile(t, workspace, "init-user/hello/readme.txt", "hello workspace\n")
	assertWorkspaceDir(t, workspace, "init-user/hello/empty")
	status := runCLI(t, home, workspace, "status")
	if !strings.Contains(status, "status: clean") {
		t.Fatalf("expected clean canonical workspace after init, got:\n%s", status)
	}

	writeWorkspaceFile(t, nonEmptyWorkspace, "existing.txt", "do not overwrite\n")
	_, stderr := runCLIFails(t, home, nonEmptyWorkspace, "init", "init-user/hello")
	if !strings.Contains(stderr, "workspace init requires an empty directory") {
		t.Fatalf("expected non-empty workspace init rejection, got:\n%s", stderr)
	}

	nestedEmpty := filepath.Join(workspace, "init-user", "hello", "nested-empty")
	if err := os.MkdirAll(nestedEmpty, 0o755); err != nil {
		t.Fatal(err)
	}
	_, stderr = runCLIFails(t, home, nestedEmpty, "init", "init-user/hello")
	if !strings.Contains(stderr, "already inside a gitslice workspace") {
		t.Fatalf("expected nested workspace init rejection, got:\n%s", stderr)
	}
}

func TestWorkspaceCommandsWorkFromSubdirectories(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()

	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")

	subdir := filepath.Join(workspace, "acme", "payment", "pkg")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, workspace, "acme/payment/pkg/subdir.go", "package pkg\nconst FromSubdir = true\n")

	status := runCLI(t, home, subdir, "status")
	if !strings.Contains(status, "/acme/payment/pkg/subdir.go") {
		t.Fatalf("expected subdir status to scan workspace root, got:\n%s", status)
	}
	runCLI(t, home, subdir, "cs", "create", "--title", "subdir edit")
	runCLI(t, home, subdir, "cs", "submit")
	status = runCLI(t, home, subdir, "status")
	if !strings.Contains(status, "status: clean") {
		t.Fatalf("expected clean status from subdir after submit, got:\n%s", status)
	}
}

func TestServerShellInspectsServerFilesWithoutLocalFile(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "server_only.go", "package payment\nconst ServerOnly = true\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "server shell seed")
	runCLI(t, home, workspace, "cs", "submit")
	if err := os.Remove(filepath.Join(workspace, "server_only.go")); err != nil {
		t.Fatal(err)
	}

	stdout, stderr := runCLIStreamsWithInput(t, home, workspace, strings.Join([]string{
		"pwd",
		"ls",
		"cat server_only.go",
		"stat server_only.go",
		"quit",
	}, "\n")+"\n", "shell")
	if stderr != "" {
		t.Fatalf("expected empty shell stderr, got:\n%s", stderr)
	}
	for _, want := range []string{
		"server shell: acme:payment @",
		"gs acme:payment:/> /",
		"server_only.go",
		"package payment\nconst ServerOnly = true\n",
		"kind: file",
		"shell_path: /server_only.go",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("shell output missing %q:\n%s", want, stdout)
		}
	}
}

func TestServerShellNavigationAndSliceBoundary(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "nested/a.go", "package nested\nconst A = true\n")
	writeWorkspaceFile(t, workspace, "nested/deep/b.go", "package deep\nconst B = true\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "server shell nested")
	runCLI(t, home, workspace, "cs", "submit")

	stdout, stderr := runCLIStreamsWithInput(t, home, workspace, strings.Join([]string{
		"ls /",
		"cd nested",
		"pwd",
		"ls",
		"cat a.go",
		"cat /nested/deep/b.go",
		"stat /acme/payment/nested/deep/b.go",
		"cat /acme/backend/secret.go",
		"quit",
	}, "\n")+"\n", "shell")
	for _, want := range []string{
		"nested/",
		"gs acme:payment:/nested> /nested",
		"a.go",
		"deep/",
		"package nested\nconst A = true\n",
		"package deep\nconst B = true\n",
		"shell_path: /nested/deep/b.go",
		"kind: file",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("shell output missing %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stderr, "outside the workspace slice") {
		t.Fatalf("expected outside-slice error in stderr, got:\n%s", stderr)
	}
}

func TestServerShellCommitPinning(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "versioned.go", "package payment\nconst Version = 1\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "server shell v1")
	firstSubmit := runCLI(t, home, workspace, "cs", "submit", "--json")
	firstCommitID := submittedRefCommitID(t, firstSubmit)

	writeWorkspaceFile(t, workspace, "versioned.go", "package payment\nconst Version = 2\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "server shell v2")
	runCLI(t, home, workspace, "cs", "submit")

	pinned, pinnedStderr := runCLIStreamsWithInput(t, home, workspace, "cat versioned.go\nquit\n", "shell", "--commit", firstCommitID)
	if pinnedStderr != "" {
		t.Fatalf("expected empty pinned shell stderr, got:\n%s", pinnedStderr)
	}
	if !strings.Contains(pinned, "const Version = 1") || strings.Contains(pinned, "const Version = 2") {
		t.Fatalf("expected pinned shell to show version 1 only, got:\n%s", pinned)
	}
	current, currentStderr := runCLIStreamsWithInput(t, home, workspace, "cat versioned.go\nquit\n", "shell")
	if currentStderr != "" {
		t.Fatalf("expected empty current shell stderr, got:\n%s", currentStderr)
	}
	if !strings.Contains(current, "const Version = 2") {
		t.Fatalf("expected current shell to show version 2, got:\n%s", current)
	}
}

func TestServerShellRunsOutsideWorkspace(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	outsideWorkspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "global_shell.go", "package payment\nconst GlobalShell = true\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "global shell seed")
	runCLI(t, home, workspace, "cs", "submit")

	// Outside a workspace, the shell attaches to the signed-in user's personal
	// home slice (the test identity's personal account is acme), scoped at the
	// global root with the account projected in.
	stdout, stderr := runCLIStreamsWithInput(t, home, outsideWorkspace, strings.Join([]string{
		"pwd",
		"ls /",
		"ls /acme",
		"cd acme/payment",
		"pwd",
		"cat global_shell.go",
		"stat /acme/payment/global_shell.go",
		"quit",
	}, "\n")+"\n", "shell")
	if stderr != "" {
		t.Fatalf("expected empty shell stderr, got:\n%s", stderr)
	}
	for _, want := range []string{
		"server shell: acme:home @",
		"gs /> /",
		"acme/",
		"payment/",
		"gs /acme/payment> /acme/payment",
		"package payment\nconst GlobalShell = true\n",
		"shell_path: /acme/payment/global_shell.go",
		"kind: file",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("global shell output missing %q:\n%s", want, stdout)
		}
	}
}

func TestServerShellAttachesExplicitSlice(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	outsideWorkspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "explicit.go", "package payment\nconst ExplicitShell = true\n")
	writeWorkspaceFile(t, workspace, "custom/nested.go", "package custom\nconst Nested = true\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "explicit shell seed")
	runCLI(t, home, workspace, "cs", "submit")
	runCLI(t, home, outsideWorkspace, "slice", "create", "acme/new-slice", "--include", "/acme/payment/custom")

	stdout, stderr := runCLIStreamsWithInput(t, home, outsideWorkspace, strings.Join([]string{
		"pwd",
		"ls",
		"cd acme",
		"pwd",
		"ls",
		"cd payment",
		"ls",
		"cd custom",
		"pwd",
		"cat nested.go",
		"cat ../explicit.go",
		"mkdir explicit-dir",
		"write explicit-dir/from_shell.txt hello explicit slice",
		"cat explicit-dir/from_shell.txt",
		"cat /acme/backend/secret.go",
		"quit",
	}, "\n")+"\n", "shell", "--slice", "acme/new-slice")
	for _, want := range []string{
		"server shell: acme:new-slice @",
		"gs acme:new-slice:/> /",
		"gs acme:new-slice:/acme> /acme",
		"gs acme:new-slice:/acme/payment/custom> /acme/payment/custom",
		"acme/",
		"payment/",
		"custom/",
		"package custom\nconst Nested = true\n",
		"ok created @",
		"ok wrote @",
		"hello explicit slice",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("explicit slice shell output missing %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stderr, "outside the attached slice") {
		t.Fatalf("expected explicit slice boundary error in stderr, got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "/acme/payment/explicit.go") {
		t.Fatalf("expected custom projection to reject sibling file, got:\n%s", stderr)
	}
}

func TestHTTPGatewayLoginAndListSlices(t *testing.T) {
	ts := startTestServer(t)
	statusCode, _, body := httpGatewayPostRaw(t, ts.httpAddr, "/gitslice.core.v1.SliceService/ListSlices", "", map[string]any{
		"account": "acme",
	})
	if statusCode != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated ListSlices to return 401, got %d:\n%s", statusCode, string(body))
	}
	statusCode, _, body = httpGatewayPostRaw(t, ts.httpAddr, "/gitslice.core.v1.SliceService/ListSlices", "not-a-token", map[string]any{
		"account": "acme",
	})
	if statusCode != http.StatusUnauthorized {
		t.Fatalf("expected invalid-token ListSlices to return 401, got %d:\n%s", statusCode, string(body))
	}
	statusCode, headers := httpGatewayOptions(t, ts.httpAddr, "/gitslice.core.v1.SliceService/ListSlices", "http://web.test")
	if statusCode != http.StatusNoContent {
		t.Fatalf("expected CORS preflight to return 204, got %d", statusCode)
	}
	if got := headers.Get("Access-Control-Allow-Origin"); got != "http://web.test" {
		t.Fatalf("expected CORS allow-origin for web app, got %q", got)
	}

	token, subjectID := ts.defaultAcmeCredentials(t)
	if subjectID == "" {
		t.Fatal("expected service-token provisioning to return subject id")
	}

	response := httpGatewayPost(t, ts.httpAddr, "/gitslice.core.v1.SliceService/ListSlices", token, map[string]any{
		"account": "acme",
	})
	slices, ok := response["slices"].([]any)
	if !ok || len(slices) == 0 {
		t.Fatalf("expected slices in response: %#v", response)
	}
	for _, raw := range slices {
		slice, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		ref, _ := slice["ref"].(map[string]any)
		if ref["account"] == "acme" && ref["slice"] == "payment" {
			return
		}
	}
	t.Fatalf("expected acme:payment slice in response: %#v", response)
}

func TestCLISliceCRUD(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "docs/seed.txt", "docs\n")
	writeWorkspaceFile(t, workspace, "docs/archive/seed.txt", "archive\n")
	writeWorkspaceFile(t, workspace, "multi-a/seed.txt", "multi a\n")
	writeWorkspaceFile(t, workspace, "multi-b/seed.txt", "multi b\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "slice crud seed")
	runCLI(t, home, workspace, "cs", "submit")

	_, stderr := runCLIFails(t, home, workspace, "slice", "create", "acme/missing", "--include", "/acme/payment/missing")
	if !strings.Contains(stderr, "included path does not exist: /acme/payment/missing") {
		t.Fatalf("missing include stderr = %q, want path existence error", stderr)
	}

	created := runCLI(t, home, workspace, "slice", "create", "acme/docs", "--include", "/acme/payment/docs", "--visibility", "private")
	for _, want := range []string{"created slice acme:docs", "visibility: private", "/acme/payment/docs"} {
		if !strings.Contains(created, want) {
			t.Fatalf("created slice output missing %q:\n%s", want, created)
		}
	}
	docsHistory := runCLI(t, home, workspace, "log", "--slice", "acme/docs")
	if !strings.Contains(docsHistory, "slice crud seed") {
		t.Fatalf("docs slice commit history missing seed commit:\n%s", docsHistory)
	}

	listed := runCLI(t, home, workspace, "slice", "list", "acme")
	for _, want := range []string{"slices for account acme:", "acme:payment", "acme:docs", "visibility: private", "included paths: /acme/payment/docs"} {
		if !strings.Contains(listed, want) {
			t.Fatalf("slice list output missing %q:\n%s", want, listed)
		}
	}

	info := runCLI(t, home, workspace, "slice", "info", "acme/docs")
	for _, want := range []string{"ref: acme:docs", "id: slice_acme_docs", "definition_hash:"} {
		if !strings.Contains(info, want) {
			t.Fatalf("slice info output missing %q:\n%s", want, info)
		}
	}

	paths := strings.TrimSpace(runCLI(t, home, workspace, "slice", "paths", "acme/docs"))
	if paths != "/acme/payment/docs" {
		t.Fatalf("slice paths = %q, want /acme/payment/docs", paths)
	}

	runCLI(t, home, workspace, "slice", "create", "acme/multi", "--include", "/acme/payment/multi-a,/acme/payment/multi-b")
	multiPaths := strings.TrimSpace(runCLI(t, home, workspace, "slice", "paths", "acme/multi"))
	if multiPaths != "/acme/payment/multi-a\n/acme/payment/multi-b" {
		t.Fatalf("comma-separated slice paths = %q, want two paths", multiPaths)
	}

	updated := runCLI(t, home, workspace, "slice", "update", "acme/docs",
		"--include", "/acme/payment/docs,/acme/payment/docs/archive",
		"--visibility", "public",
	)
	for _, want := range []string{"updated slice acme:docs", "visibility: public", "/acme/payment/docs/archive"} {
		if !strings.Contains(updated, want) {
			t.Fatalf("slice update output missing %q:\n%s", want, updated)
		}
	}

	_, stderr = runCLIFails(t, home, workspace, "slice", "delete", "acme/docs")
	if !strings.Contains(stderr, "requires --yes") {
		t.Fatalf("slice delete without --yes stderr missing confirmation requirement:\n%s", stderr)
	}

	deleted := runCLI(t, home, workspace, "slice", "delete", "acme/docs", "--yes")
	if !strings.Contains(deleted, "deleted slice acme:docs") {
		t.Fatalf("unexpected delete output:\n%s", deleted)
	}
	_, stderr = runCLIFails(t, home, workspace, "slice", "info", "acme/docs")
	if !strings.Contains(stderr, "not found") {
		t.Fatalf("slice info after delete stderr = %q, want not found", stderr)
	}
}

func TestCLISignupShellDefaultsToPersonalHome(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	outsideWorkspace := t.TempDir()
	homeWorkspace := t.TempDir()

	runCLISignupThroughWeb(t, home, outsideWorkspace, ts, "other_user")
	runCLI(t, home, outsideWorkspace, "fs", "mkdir", "/other-user/docs")
	runCLISignupThroughWeb(t, home, outsideWorkspace, ts, "shell_user")
	initOutput := runCLI(t, home, homeWorkspace, "init", "home")
	if !strings.Contains(initOutput, "initialized workspace for shell-user:home") {
		t.Fatalf("unexpected home workspace init output:\n%s", initOutput)
	}
	paths := strings.TrimSpace(runCLI(t, home, outsideWorkspace, "slice", "paths", "home"))
	if paths != "/shell-user" {
		t.Fatalf("bare slice paths = %q, want /shell-user", paths)
	}
	stdout, stderr := runCLIStreamsWithInput(t, home, outsideWorkspace, strings.Join([]string{
		"pwd",
		"ls",
		"cd other-user",
		"cd shell-user",
		"pwd",
		"quit",
	}, "\n")+"\n", "shell")
	if !strings.Contains(stderr, "path is outside the attached slice: /other-user") {
		t.Fatalf("expected other-user to be hidden from shell-user home shell, got stderr:\n%s", stderr)
	}
	for _, want := range []string{
		"server shell: shell-user:home @",
		"shell-user/",
		"/shell-user",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("signup shell output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "other-user/") {
		t.Fatalf("shell-user home shell leaked other-user folder:\n%s", stdout)
	}
	explicit, explicitStderr := runCLIStreamsWithInput(t, home, outsideWorkspace, "pwd\nquit\n", "shell", "--slice", "home")
	if explicitStderr != "" {
		t.Fatalf("expected empty explicit bare-slice shell stderr, got:\n%s", explicitStderr)
	}
	if !strings.Contains(explicit, "server shell: shell-user:home @") || !strings.Contains(explicit, "/") {
		t.Fatalf("explicit bare-slice shell output missing home scope:\n%s", explicit)
	}
}

func TestCLIFileAndShellMutationsStayInHome(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	dir := t.TempDir()

	runCLISignupThroughWeb(t, home, dir, ts, "file_user")
	runCLI(t, home, dir, "fs", "mkdir", "/file-user/docs")
	runCLI(t, home, dir, "fs", "write", "/file-user/docs/readme.md", "--text", "hello from file command\n")
	runCLI(t, home, dir, "fs", "mv", "/file-user/docs/readme.md", "/file-user/docs/today.md")
	runCLI(t, home, dir, "fs", "mkdir", "/file-user/empty")
	runCLI(t, home, dir, "fs", "mv", "/file-user/empty", "/file-user/archive")

	fsList, fsListStderr := runCLIStreams(t, home, dir, "fs", "ls", "/file-user")
	if fsListStderr != "" {
		t.Fatalf("explicit fs ls should not print a default-path diagnostic, got:\n%s", fsListStderr)
	}
	for _, want := range []string{"archive/", "docs/"} {
		if !strings.Contains(fsList, want) {
			t.Fatalf("fs ls output missing %q:\n%s", want, fsList)
		}
	}
	defaultFSList, defaultFSListStderr := runCLIStreams(t, home, dir, "fs", "ls")
	if !strings.Contains(defaultFSListStderr, "remote: listing file-user:home at /file-user") {
		t.Fatalf("default fs ls stderr should name the remote home root, got:\n%s", defaultFSListStderr)
	}
	for _, want := range []string{"archive/", "docs/"} {
		if !strings.Contains(defaultFSList, want) {
			t.Fatalf("default fs ls output missing %q:\n%s", want, defaultFSList)
		}
	}
	defaultFSListJSON, defaultFSListJSONStderr := runCLIStreams(t, home, dir, "--json", "fs", "ls")
	if defaultFSListJSONStderr != "" {
		t.Fatalf("json fs ls should keep stderr empty, got:\n%s", defaultFSListJSONStderr)
	}
	if !strings.Contains(defaultFSListJSON, `"path": "/file-user"`) {
		t.Fatalf("json fs ls should report the resolved remote path, got:\n%s", defaultFSListJSON)
	}
	fsCat := runCLI(t, home, dir, "fs", "cat", "/file-user/docs/today.md")
	if fsCat != "hello from file command\n" {
		t.Fatalf("unexpected fs cat output:\n%s", fsCat)
	}
	followHistory := runCLI(t, home, dir, "log", "--", "/file-user/docs/today.md")
	if !strings.Contains(followHistory, "file write /file-user/docs/readme.md") || !strings.Contains(followHistory, "file mv") {
		t.Fatalf("follow-moves commit history missing write or move:\n%s", followHistory)
	}
	literalHistory := runCLI(t, home, dir, "log", "--no-follow-moves", "--", "/file-user/docs/today.md")
	if strings.Contains(literalHistory, "file write /file-user/docs/readme.md") || !strings.Contains(literalHistory, "file mv") {
		t.Fatalf("literal commit history should include move but not pre-move write:\n%s", literalHistory)
	}

	stdout, stderr := runCLIStreamsWithInput(t, home, dir, strings.Join([]string{
		"ls /file-user",
		"ls /file-user/docs",
		"cat /file-user/docs/today.md",
		"cd /file-user/docs",
		"pwd",
		"cat today.md",
		"cd /file-user",
		"mkdir /file-user/shell",
		"cd shell",
		"write note.txt hello from shell",
		"mv note.txt final.txt",
		"cat final.txt",
		"rm final.txt",
		"ls /file-user/shell",
		"quit",
	}, "\n")+"\n", "shell")
	if stderr != "" {
		t.Fatalf("expected empty shell stderr, got:\n%s", stderr)
	}
	for _, want := range []string{
		"archive/",
		"docs/",
		"today.md",
		"hello from file command\n",
		"gs /file-user/docs> /file-user/docs",
		"ok created @",
		"ok wrote @",
		"ok moved @",
		"ok removed @",
		"hello from shell",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("mutation shell output missing %q:\n%s", want, stdout)
		}
	}

	_, stderr = runCLIFails(t, home, dir, "fs", "write", "relative.txt", "--text", "no")
	if !strings.Contains(stderr, "paths must be absolute") {
		t.Fatalf("expected absolute-path error, got:\n%s", stderr)
	}
	_, stderr = runCLIFails(t, home, dir, "fs", "write", "/alice/hack.txt", "--text", "no")
	if !strings.Contains(stderr, "outside the home slice") {
		t.Fatalf("expected outside-home error, got:\n%s", stderr)
	}
}

func TestCLIUploadLargeDirectory(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	dir := t.TempDir()

	runCLISignupThroughWeb(t, home, dir, ts, "upload_user")
	sourceRoot := filepath.Join(t.TempDir(), "source")
	targetFiles := uploadTestFileCount(t)
	filesPerDir := 8
	dirCount := (targetFiles + filesPerDir - 1) / filesPerDir
	totalFiles := 0
	for d := 0; d < dirCount; d++ {
		subdir := filepath.Join(sourceRoot, fmt.Sprintf("dir-%02d", d), fmt.Sprintf("sub-%02d", d%4))
		if err := os.MkdirAll(subdir, 0o755); err != nil {
			t.Fatal(err)
		}
		for f := 0; f < filesPerDir && totalFiles < targetFiles; f++ {
			name := fmt.Sprintf("file-%02d.txt", f)
			content := fmt.Sprintf("dir=%02d sub=%02d file=%02d\n", d, d%4, f)
			if err := os.WriteFile(filepath.Join(subdir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			totalFiles++
		}
	}
	for i := 0; i < 10; i++ {
		if err := os.MkdirAll(filepath.Join(sourceRoot, "empty", fmt.Sprintf("leaf-%02d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	_, stderr := runCLIFails(t, home, dir, "fs", "upload", sourceRoot, "/upload-user/not-recursive")
	if !strings.Contains(stderr, "Pass --recursive") {
		t.Fatalf("directory upload without --recursive stderr missing hint:\n%s", stderr)
	}

	output := runCLI(t, home, dir, "fs", "upload", sourceRoot, "/upload-user/bulk", "--recursive", "--concurrency", "8")
	wantChangedPaths := totalFiles + 10
	wantSummary := fmt.Sprintf("uploaded %d paths in upload-user:home", wantChangedPaths)
	if !strings.Contains(output, wantSummary) {
		t.Fatalf("unexpected upload output:\n%s", output)
	}
	lastDir := (targetFiles - 1) / filesPerDir
	lastFile := (targetFiles - 1) % filesPerDir
	lastSubdir := lastDir % 4
	lastRemotePath := fmt.Sprintf("/upload-user/bulk/dir-%02d/sub-%02d/file-%02d.txt", lastDir, lastSubdir, lastFile)
	wantContent := fmt.Sprintf("dir=%02d sub=%02d file=%02d\n", lastDir, lastSubdir, lastFile)
	if got := runCLI(t, home, dir, "fs", "cat", lastRemotePath); got != wantContent {
		t.Fatalf("uploaded file content = %q", got)
	}

	token := readToken(t, home)
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	repo := corev1.NewRepositoryServiceClient(conn)
	ref, err := repo.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	files, dirs := countRemoteTree(t, ctx, repo, ref.CommitId, "/upload-user/bulk")
	if files != totalFiles {
		t.Fatalf("uploaded file count = %d, want %d", files, totalFiles)
	}
	wantMinDirs := dirCount*2 + 11
	if dirs < wantMinDirs {
		t.Fatalf("uploaded directory count = %d, want at least %d", dirs, wantMinDirs)
	}
	resolved, err := repo.ResolvePath(ctx, &corev1.ResolvePathRequest{CommitId: ref.CommitId, Path: "/upload-user/bulk/empty/leaf-09"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Entry == nil || resolved.Entry.Kind != corev1.EntryKind_ENTRY_KIND_DIRECTORY {
		t.Fatalf("empty directory was not preserved: %#v", resolved.Entry)
	}

	singleFile := filepath.Join(t.TempDir(), "single.txt")
	if err := os.WriteFile(singleFile, []byte("single upload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCLI(t, home, dir, "fs", "upload", singleFile, "/upload-user/single.txt")
	if got := runCLI(t, home, dir, "fs", "cat", "/upload-user/single.txt"); got != "single upload\n" {
		t.Fatalf("single uploaded file content = %q", got)
	}

	_, stderr = runCLIFails(t, home, dir, "fs", "upload", singleFile, "/alice/hack.txt")
	if !strings.Contains(stderr, "outside the home slice") {
		t.Fatalf("expected outside-home upload error, got:\n%s", stderr)
	}
}

func TestHTTPGatewayWriteChangesetFlow(t *testing.T) {
	ts := startTestServer(t)
	token, _ := ts.defaultAcmeCredentials(t)

	content := []byte("package payment\nconst GatewayWrite = true\n")
	upload := httpGatewayPost(t, ts.httpAddr, "/gitslice.core.v1.BlobService/UploadBlob", token, map[string]any{
		"data":  base64.StdEncoding.EncodeToString(content),
		"slice": map[string]string{"account": "acme", "slice": "payment"},
	})
	blobID, _ := upload["blobId"].(string)
	contentHash, _ := upload["contentHash"].(string)
	if blobID == "" || contentHash == "" {
		t.Fatalf("expected uploaded blob id and hash: %#v", upload)
	}

	blobStatus := httpGatewayPost(t, ts.httpAddr, "/gitslice.core.v1.BlobService/GetBlobStatus", token, map[string]any{
		"contentHashes": []string{contentHash, "sha256:missing"},
		"slice":         map[string]string{"account": "acme", "slice": "payment"},
	})
	records, ok := blobStatus["blobs"].([]any)
	if !ok || len(records) != 2 {
		t.Fatalf("expected two blob status records: %#v", blobStatus)
	}
	if first, _ := records[0].(map[string]any); first["state"] != "available" {
		t.Fatalf("expected uploaded blob to be available: %#v", blobStatus)
	}
	if second, _ := records[1].(map[string]any); second["state"] != "missing" {
		t.Fatalf("expected unknown blob to be missing: %#v", blobStatus)
	}

	cs := httpGatewayPost(t, ts.httpAddr, "/gitslice.core.v1.ChangesetService/CreateChangeset", token, map[string]any{
		"authoringSlice": map[string]string{"account": "acme", "slice": "payment"},
		"title":          "gateway write",
		"description":    "created through HTTP gateway",
	})
	changesetID, _ := cs["id"].(string)
	baseCommitID, _ := cs["baseCommitId"].(string)
	if changesetID == "" || baseCommitID == "" {
		t.Fatalf("expected changeset id and base commit: %#v", cs)
	}

	patchset := httpGatewayPost(t, ts.httpAddr, "/gitslice.core.v1.ChangesetService/UpdateChangeset", token, map[string]any{
		"changesetId":  changesetID,
		"baseCommitId": baseCommitID,
		"fileEdits": []map[string]any{{
			"op":          "add",
			"path":        "/acme/payment/gateway_write.go",
			"blobId":      blobID,
			"contentHash": contentHash,
			"mode":        420,
		}},
	})
	patchsetID, _ := patchset["id"].(string)
	if patchsetID == "" {
		t.Fatalf("expected patchset id: %#v", patchset)
	}

	submit := httpGatewayPost(t, ts.httpAddr, "/gitslice.core.v1.ChangesetService/SubmitChangeset", token, map[string]any{
		"changesetId":               changesetID,
		"expectedCurrentPatchsetId": patchsetID,
	})
	if status, _ := submit["status"].(string); status != "pending_publish" && status != "submitted" {
		t.Fatalf("unexpected submit response: %#v", submit)
	}
	published := waitForGatewayChangesetStatus(t, ts.httpAddr, token, changesetID, "submitted")
	if commitID, _ := published["commitId"].(string); commitID == "" {
		t.Fatalf("expected published commit id: %#v", published)
	}
}

func TestChangesetUpdateAndDelete(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "draft.go", "package payment\nconst Version = 1\n")
	runCLI(t, home, workspace, "cs", "create")
	writeWorkspaceFile(t, workspace, "draft.go", "package payment\nconst Version = 2\n")
	runCLI(t, home, workspace, "cs", "update")
	runCLI(t, home, workspace, "cs", "submit")
	status := runCLI(t, home, workspace, "status")
	if !strings.Contains(status, "status: clean") {
		t.Fatalf("expected clean status after update submit, got:\n%s", status)
	}

	if err := os.Remove(filepath.Join(workspace, "draft.go")); err != nil {
		t.Fatal(err)
	}
	status = runCLI(t, home, workspace, "status")
	if !strings.Contains(status, "/acme/payment/draft.go") {
		t.Fatalf("expected delete to be dirty, got:\n%s", status)
	}
	runCLI(t, home, workspace, "cs", "create", "--title", "delete draft")
	runCLI(t, home, workspace, "cs", "submit")
	status = runCLI(t, home, workspace, "status")
	if !strings.Contains(status, "status: clean") {
		t.Fatalf("expected clean status after delete submit, got:\n%s", status)
	}
}

func TestChangesetWorkflowCommandsAndServerDiff(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")

	writeWorkspaceFile(t, workspace, "workflow.go", "package payment\nconst Version = 1\n")
	localDiff := runCLI(t, home, workspace, "diff")
	for _, want := range []string{"diff --git", "a/acme/payment/workflow.go", "+const Version = 1"} {
		if !strings.Contains(localDiff, want) {
			t.Fatalf("workspace diff missing %q:\n%s", want, localDiff)
		}
	}
	nameOnly := runCLI(t, home, workspace, "diff", "--name-only")
	if strings.TrimSpace(nameOnly) != "/acme/payment/workflow.go" {
		t.Fatalf("workspace diff --name-only = %q", nameOnly)
	}
	stat := runCLI(t, home, workspace, "diff", "--stat")
	if !strings.Contains(stat, "1 changed path(s)") || !strings.Contains(stat, "/acme/payment/workflow.go") {
		t.Fatalf("workspace diff --stat missing path summary:\n%s", stat)
	}

	createOut := runCLI(t, home, workspace, "cs", "create", "--title", "workflow commands", "--json")
	_, firstPatchsetID, changesetHandle, _ := parseChangesetOutput(t, createOut)
	_, createAgainErr := runCLIFails(t, home, workspace, "cs", "create", "--title", "accidental duplicate")
	if !strings.Contains(createAgainErr, "workspace already has draft changeset "+changesetHandle) ||
		!strings.Contains(createAgainErr, "Run gs cs update to add a new patchset") {
		t.Fatalf("duplicate create did not prompt update:\n%s", createAgainErr)
	}
	show := runCLI(t, home, workspace, "cs", "show")
	for _, want := range []string{changesetHandle, "title: workflow commands", "patchsets:", "/acme/payment/workflow.go"} {
		if !strings.Contains(show, want) {
			t.Fatalf("cs show missing %q:\n%s", want, show)
		}
	}
	list := runCLI(t, home, workspace, "cs", "list", "--status", "draft")
	if !strings.Contains(list, changesetHandle) || !strings.Contains(list, "workflow commands") {
		t.Fatalf("cs list missing changeset:\n%s", list)
	}
	versions := runCLI(t, home, workspace, "cs", "versions")
	if !strings.Contains(versions, "1 current changed=1") || strings.Contains(versions, firstPatchsetID) {
		t.Fatalf("cs versions missing first patchset:\n%s", versions)
	}
	versions = runCLI(t, home, workspace, "cs", "versions", strings.TrimPrefix(changesetHandle, "acme/payment"))
	if !strings.Contains(versions, "1 current changed=1") {
		t.Fatalf("cs versions shorthand missing first patchset:\n%s", versions)
	}
	firstDiff := runCLI(t, home, workspace, "cs", "diff", "--patchset", "1")
	if !strings.Contains(firstDiff, "+const Version = 1") {
		t.Fatalf("server diff for first patchset missing addition:\n%s", firstDiff)
	}

	writeWorkspaceFile(t, workspace, "workflow.go", "package payment\nconst Version = 2\n")
	updateOut := runCLI(t, home, workspace, "cs", "update", "--json")
	_, secondPatchsetID, _, _ := parseChangesetOutput(t, updateOut)
	versions = runCLI(t, home, workspace, "cs", "patchsets", changesetHandle)
	if !strings.Contains(versions, "2 current changed=1") || strings.Contains(versions, secondPatchsetID) {
		t.Fatalf("cs patchsets missing second patchset:\n%s", versions)
	}
	between := runCLI(t, home, workspace, "cs", "diff", changesetHandle, "--from", "1", "--to", "2")
	for _, want := range []string{"-const Version = 1", "+const Version = 2"} {
		if !strings.Contains(between, want) {
			t.Fatalf("server patchset diff missing %q:\n%s", want, between)
		}
	}
	serverNameOnly := runCLI(t, home, workspace, "cs", "diff", "--name-only")
	if strings.TrimSpace(serverNameOnly) != "/acme/payment/workflow.go" {
		t.Fatalf("cs diff --name-only = %q", serverNameOnly)
	}
	explain := runCLI(t, home, workspace, "cs", "explain")
	for _, want := range []string{"validation:", "submit_requirements:", "read_set:", "write_set:"} {
		if !strings.Contains(explain, want) {
			t.Fatalf("cs explain missing %q:\n%s", want, explain)
		}
	}
	status := runCLI(t, home, workspace, "cs", "status", changesetHandle)
	if !strings.Contains(status, "status: draft") {
		t.Fatalf("cs status <id> missing draft status:\n%s", status)
	}

	runCLI(t, home, workspace, "cs", "submit", changesetHandle)
	status = runCLI(t, home, workspace, "cs", "status", changesetHandle)
	if !strings.Contains(status, "status: submitted") {
		t.Fatalf("cs status <id> missing submitted status:\n%s", status)
	}

	writeWorkspaceFile(t, workspace, "abandon.go", "package payment\nconst Abandon = true\n")
	abandonOut := runCLI(t, home, workspace, "cs", "create", "--title", "abandon workflow", "--json")
	_, _, abandonHandle, _ := parseChangesetOutput(t, abandonOut)
	runCLI(t, home, workspace, "cs", "abandon", "--reason", "test cleanup")
	status = runCLI(t, home, workspace, "cs", "status", abandonHandle)
	if !strings.Contains(status, "status: abandoned") {
		t.Fatalf("cs abandon did not mark changeset abandoned:\n%s", status)
	}
}

func TestChangesetStatusWatchAfterNoWatchSubmit(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "watch.go", "package payment\nconst Watch = true\n")
	createOut := runCLI(t, home, workspace, "cs", "create", "--title", "watch submit", "--json")
	changesetID, _, changesetHandle, _ := parseChangesetOutput(t, createOut)

	submitOut := runCLI(t, home, workspace, "cs", "submit", "--no-watch", "--json")
	var submit struct {
		ChangesetID string `json:"changeset_id"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal([]byte(submitOut), &submit); err != nil {
		t.Fatalf("submit output is not JSON: %v\n%s", err, submitOut)
	}
	if submit.ChangesetID != changesetID {
		t.Fatalf("submit changed changeset id: %#v want %s", submit, changesetID)
	}
	if submit.Status != "pending_publish" && submit.Status != "submitted" {
		t.Fatalf("unexpected submit status: %#v", submit)
	}

	status := runCLI(t, home, workspace, "cs", "status", "--watch", "--watch-timeout", "5s", changesetHandle)
	if !strings.Contains(status, "status: submitted") {
		t.Fatalf("cs status --watch did not reach submitted:\n%s", status)
	}
}

func TestOutsideSliceEditRejected(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "acme/backend/app.go", "package backend\n")
	_, stderr := runCLIFails(t, home, workspace, "status")
	if !strings.Contains(stderr, "outside the workspace slice") {
		t.Fatalf("expected outside-slice rejection, got stderr:\n%s", stderr)
	}
}

func TestDisjointStaleChangesetsCanSubmit(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	loginTestCLI(t, ts, home, workspaceA)
	runCLI(t, home, workspaceA, "workspace", "init", "acme/payment")
	runCLI(t, home, workspaceB, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspaceA, "a.go", "package payment\nconst A = 1\n")
	writeWorkspaceFile(t, workspaceB, "b.go", "package payment\nconst B = 1\n")
	runCLI(t, home, workspaceA, "cs", "create", "--title", "add a")
	runCLI(t, home, workspaceB, "cs", "create", "--title", "add b")
	runCLI(t, home, workspaceA, "cs", "submit")
	runCLI(t, home, workspaceB, "cs", "submit")
}

func TestSamePathConflictRejected(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	loginTestCLI(t, ts, home, workspaceA)
	runCLI(t, home, workspaceA, "workspace", "init", "acme/payment")
	runCLI(t, home, workspaceB, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspaceA, "conflict.go", "package payment\nconst Conflict = 1\n")
	writeWorkspaceFile(t, workspaceB, "conflict.go", "package payment\nconst Conflict = 2\n")
	runCLI(t, home, workspaceA, "cs", "create", "--title", "conflict one")
	runCLI(t, home, workspaceB, "cs", "create", "--title", "conflict two")
	runCLI(t, home, workspaceA, "cs", "submit")
	_, stderr := runCLIFails(t, home, workspaceB, "cs", "submit")
	if !strings.Contains(stderr, "FailedPrecondition") && !strings.Contains(stderr, "conflict") {
		t.Fatalf("expected same-path conflict, got stderr:\n%s", stderr)
	}
}

func TestWorkspaceSyncUpdatesCleanWorkspace(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	loginTestCLI(t, ts, home, workspaceA)
	runCLI(t, home, workspaceA, "workspace", "init", "acme/payment")
	runCLI(t, home, workspaceB, "workspace", "init", "acme/payment")

	const remoteContent = "package payment\nconst SyncedRemote = true\n"
	writeWorkspaceFile(t, workspaceB, "remote_sync.go", remoteContent)
	runCLI(t, home, workspaceB, "cs", "create", "--title", "remote sync")
	runCLI(t, home, workspaceB, "cs", "submit")

	out := runCLI(t, home, workspaceA, "sync")
	if !strings.Contains(out, "updated 1 remote path(s)") {
		t.Fatalf("sync output did not report remote update:\n%s", out)
	}
	assertWorkspaceFile(t, workspaceA, "acme/payment/remote_sync.go", remoteContent)
	status := runCLI(t, home, workspaceA, "status")
	if !strings.Contains(status, "status: clean") {
		t.Fatalf("expected clean status after sync, got:\n%s", status)
	}
}

func TestWorkspaceSyncRebasesDraftChangeset(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	loginTestCLI(t, ts, home, workspaceA)
	runCLI(t, home, workspaceA, "workspace", "init", "acme/payment")
	runCLI(t, home, workspaceB, "workspace", "init", "acme/payment")

	const localContent = "package payment\nconst LocalSync = true\n"
	writeWorkspaceFile(t, workspaceA, "local_sync.go", localContent)
	runCLI(t, home, workspaceA, "cs", "create", "--title", "local sync draft")

	const remoteContent = "package payment\nconst RemoteSync = true\n"
	writeWorkspaceFile(t, workspaceB, "remote_sync.go", remoteContent)
	runCLI(t, home, workspaceB, "cs", "create", "--title", "remote sync")
	runCLI(t, home, workspaceB, "cs", "submit")

	raw := runCLI(t, home, workspaceA, "sync", "--json")
	var synced struct {
		Status         string   `json:"status"`
		PatchsetNumber int64    `json:"patchset_number"`
		UpdatedPaths   []string `json:"updated_paths"`
		LocalPaths     []string `json:"local_paths"`
		ConflictCount  int      `json:"conflict_count"`
	}
	if err := json.Unmarshal([]byte(raw), &synced); err != nil {
		t.Fatalf("sync output is not JSON: %v\n%s", err, raw)
	}
	if synced.Status != "synced" || synced.PatchsetNumber != 2 || synced.ConflictCount != 0 {
		t.Fatalf("unexpected sync output: %#v raw=%s", synced, raw)
	}
	if !containsString(synced.UpdatedPaths, "/acme/payment/remote_sync.go") || !containsString(synced.LocalPaths, "/acme/payment/local_sync.go") {
		t.Fatalf("sync output missing expected path sets: %#v", synced)
	}
	assertWorkspaceFile(t, workspaceA, "local_sync.go", localContent)
	assertWorkspaceFile(t, workspaceA, "acme/payment/remote_sync.go", remoteContent)

	runCLI(t, home, workspaceA, "cs", "submit")
	verifyWorkspace := t.TempDir()
	runCLI(t, home, verifyWorkspace, "workspace", "init", "acme/payment")
	assertWorkspaceFile(t, verifyWorkspace, "acme/payment/local_sync.go", localContent)
	assertWorkspaceFile(t, verifyWorkspace, "acme/payment/remote_sync.go", remoteContent)
}

func TestWorkspaceSyncLineMergesNonOverlappingTextByDefault(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	seedWorkspace := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	loginTestCLI(t, ts, home, seedWorkspace)
	runCLI(t, home, seedWorkspace, "workspace", "init", "acme/payment")

	const baseContent = "package payment\nconst LocalValue = \"base\"\nconst RemoteValue = \"base\"\n"
	writeWorkspaceFile(t, seedWorkspace, "merge_default.go", baseContent)
	runCLI(t, home, seedWorkspace, "cs", "create", "--title", "seed merge base")
	runCLI(t, home, seedWorkspace, "cs", "submit")

	runCLI(t, home, workspaceA, "workspace", "init", "acme/payment")
	runCLI(t, home, workspaceB, "workspace", "init", "acme/payment")

	const localContent = "package payment\nconst LocalValue = \"local\"\nconst RemoteValue = \"base\"\n"
	writeWorkspaceFile(t, workspaceA, "acme/payment/merge_default.go", localContent)
	runCLI(t, home, workspaceA, "cs", "create", "--title", "local merge edit")

	const remoteContent = "package payment\nconst LocalValue = \"base\"\nconst RemoteValue = \"remote\"\n"
	writeWorkspaceFile(t, workspaceB, "acme/payment/merge_default.go", remoteContent)
	runCLI(t, home, workspaceB, "cs", "create", "--title", "remote merge edit")
	runCLI(t, home, workspaceB, "cs", "submit")

	raw := runCLI(t, home, workspaceA, "sync", "--json")
	var synced struct {
		Status        string   `json:"status"`
		MergeStrategy string   `json:"merge_strategy"`
		MergedPaths   []string `json:"merged_paths"`
		ConflictCount int      `json:"conflict_count"`
	}
	if err := json.Unmarshal([]byte(raw), &synced); err != nil {
		t.Fatalf("sync output is not JSON: %v\n%s", err, raw)
	}
	if synced.Status != "synced" || synced.MergeStrategy != "line" || synced.ConflictCount != 0 {
		t.Fatalf("unexpected sync output: %#v raw=%s", synced, raw)
	}
	if !containsString(synced.MergedPaths, "/acme/payment/merge_default.go") {
		t.Fatalf("sync output missing merged path: %#v", synced)
	}
	const mergedContent = "package payment\nconst LocalValue = \"local\"\nconst RemoteValue = \"remote\"\n"
	assertWorkspaceFile(t, workspaceA, "acme/payment/merge_default.go", mergedContent)

	runCLI(t, home, workspaceA, "cs", "submit")
	verifyWorkspace := t.TempDir()
	runCLI(t, home, verifyWorkspace, "workspace", "init", "acme/payment")
	assertWorkspaceFile(t, verifyWorkspace, "acme/payment/merge_default.go", mergedContent)
}

func TestWorkspaceSyncRecordsAndResolvesConflicts(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	loginTestCLI(t, ts, home, workspaceA)
	runCLI(t, home, workspaceA, "workspace", "init", "acme/payment")
	runCLI(t, home, workspaceB, "workspace", "init", "acme/payment")

	writeWorkspaceFile(t, workspaceA, "sync_conflict.go", "package payment\nconst Value = \"local\"\n")
	runCLI(t, home, workspaceA, "cs", "create", "--title", "local sync conflict")

	writeWorkspaceFile(t, workspaceB, "sync_conflict.go", "package payment\nconst Value = \"remote\"\n")
	runCLI(t, home, workspaceB, "cs", "create", "--title", "remote sync conflict")
	runCLI(t, home, workspaceB, "cs", "submit")

	out := runCLI(t, home, workspaceA, "sync")
	if !strings.Contains(out, "conflicts: 1 path(s)") || !strings.Contains(out, "/acme/payment/sync_conflict.go") {
		t.Fatalf("sync output missing conflict:\n%s", out)
	}
	conflicted, err := os.ReadFile(filepath.Join(workspaceA, "sync_conflict.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<<<<<<< gitslice local", "const Value = \"local\"", "const Value = \"remote\"", ">>>>>>> gitslice remote"} {
		if !strings.Contains(string(conflicted), want) {
			t.Fatalf("conflict file missing %q:\n%s", want, string(conflicted))
		}
	}
	status := runCLI(t, home, workspaceA, "status")
	if !strings.Contains(status, "conflicts: 1 unresolved path(s)") {
		t.Fatalf("status did not report conflict:\n%s", status)
	}
	_, submitErr := runCLIFails(t, home, workspaceA, "cs", "submit")
	if !strings.Contains(submitErr, "unresolved sync conflicts") {
		t.Fatalf("submit did not reject unresolved conflicts:\n%s", submitErr)
	}

	const resolvedContent = "package payment\nconst Value = \"resolved\"\n"
	writeWorkspaceFile(t, workspaceA, "sync_conflict.go", resolvedContent)
	runCLI(t, home, workspaceA, "cs", "update")
	runCLI(t, home, workspaceA, "cs", "submit")

	verifyWorkspace := t.TempDir()
	runCLI(t, home, verifyWorkspace, "workspace", "init", "acme/payment")
	assertWorkspaceFile(t, verifyWorkspace, "acme/payment/sync_conflict.go", resolvedContent)
}

func TestRepositoryReadAPIs(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	const content = "package api\nconst ReadAPI = true\n"
	writeWorkspaceFile(t, workspace, "api/read_api.go", content)
	runCLI(t, home, workspace, "cs", "create", "--title", "repository api read")
	runCLI(t, home, workspace, "cs", "submit")

	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	repository := corev1.NewRepositoryServiceClient(conn)

	ref, err := repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Name != postgres.DefaultTargetRef || ref.CommitId == "" {
		t.Fatalf("unexpected default ref: %#v", ref)
	}
	commit, err := repository.GetCommit(ctx, &corev1.GetCommitRequest{CommitId: ref.CommitId})
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(commit.ChangedPaths, "/acme/payment/api/read_api.go") {
		t.Fatalf("expected commit to include changed path, got %#v", commit.ChangedPaths)
	}
	root, err := repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{CommitId: ref.CommitId, Path: "/"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasEntry(root.Entries, "acme", corev1.EntryKind_ENTRY_KIND_DIRECTORY) {
		t.Fatalf("expected root to contain acme directory: %#v", root.Entries)
	}
	payment, err := repository.ListDirectory(ctx, &corev1.ListDirectoryRequest{CommitId: ref.CommitId, Path: "/acme/payment"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasEntry(payment.Entries, "api", corev1.EntryKind_ENTRY_KIND_DIRECTORY) {
		t.Fatalf("expected payment directory to contain api directory: %#v", payment.Entries)
	}
	resolvedDir, err := repository.ResolvePath(ctx, &corev1.ResolvePathRequest{CommitId: ref.CommitId, Path: "/acme/payment/api"})
	if err != nil {
		t.Fatal(err)
	}
	if resolvedDir.Entry.Kind != corev1.EntryKind_ENTRY_KIND_DIRECTORY {
		t.Fatalf("expected api to resolve as directory: %#v", resolvedDir.Entry)
	}
	resolvedFile, err := repository.ResolvePath(ctx, &corev1.ResolvePathRequest{CommitId: ref.CommitId, Path: "/acme/payment/api/read_api.go"})
	if err != nil {
		t.Fatal(err)
	}
	if resolvedFile.Entry.Kind != corev1.EntryKind_ENTRY_KIND_FILE || resolvedFile.Entry.ContentHash == "" {
		t.Fatalf("expected read_api.go to resolve as file with content hash: %#v", resolvedFile.Entry)
	}
	read, err := repository.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: ref.CommitId, Path: "/acme/payment/api/read_api.go"})
	if err != nil {
		t.Fatal(err)
	}
	if string(read.Data) != content || read.ContentHash != resolvedFile.Entry.ContentHash {
		t.Fatalf("unexpected read response: content=%q hash=%q want hash=%q", string(read.Data), read.ContentHash, resolvedFile.Entry.ContentHash)
	}
	partial, err := repository.ReadFile(ctx, &corev1.ReadFileRequest{CommitId: ref.CommitId, Path: "/acme/payment/api/read_api.go", Offset: 8, Length: 3})
	if err != nil {
		t.Fatal(err)
	}
	if string(partial.Data) != "api" || partial.Offset != 8 {
		t.Fatalf("unexpected partial read response: %#v data=%q", partial, string(partial.Data))
	}
}

func TestSliceDefinitionUpdateConflict(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "docs/seed.txt", "docs\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "slice definition seed")
	runCLI(t, home, workspace, "cs", "submit")

	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	slices := corev1.NewSliceServiceClient(conn)

	slice, err := slices.ResolveSlice(ctx, &corev1.ResolveSliceRequest{Ref: &corev1.SliceRef{Account: "acme", Slice: "payment"}})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := slices.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                slice.Id,
		ExpectedDefinitionHash: slice.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			Version:       slice.Definition.Version,
			Visibility:    "public",
			IncludedPaths: append(append([]string{}, slice.Definition.IncludedPaths...), "/acme/payment/docs"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != slice.Definition.Version+1 {
		t.Fatalf("expected definition version %d, got %d", slice.Definition.Version+1, updated.Version)
	}
	if updated.Visibility != "public" || !containsString(updated.IncludedPaths, "/acme/payment/docs") {
		t.Fatalf("unexpected updated definition: %#v", updated)
	}
	_, err = slices.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                slice.Id,
		ExpectedDefinitionHash: slice.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			Version:       updated.Version,
			Visibility:    "private",
			IncludedPaths: []string{"/acme/payment"},
		},
	})
	if grpcstatus.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected stale definition update to fail with FailedPrecondition, got %v", err)
	}
}

func TestChangesetAbandonAndSubmitIdempotency(t *testing.T) {
	ts := startTestServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	clients := newTestCoreClients(conn)

	submittedID, patchsetID := createDirectPatchset(t, ctx, clients, "/acme/payment/idempotent_submit.go", "package payment\nconst Idempotent = true\n", "idempotent submit")
	first, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               submittedID,
		ExpectedCurrentPatchsetId: patchsetID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "pending_publish" && first.Status != "submitted" {
		t.Fatalf("unexpected first submit response: %#v", first)
	}
	published := waitForSubmittedChangeset(t, ctx, clients.changeset, submittedID)
	second, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               submittedID,
		ExpectedCurrentPatchsetId: patchsetID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "submitted" || second.CommitId != published.CommitId {
		t.Fatalf("expected idempotent submitted response with same commit, got %#v want commit %s", second, published.CommitId)
	}

	abandoned, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      postgres.DefaultTargetRef,
		Title:          "abandon draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.changeset.AbandonChangeset(ctx, &corev1.AbandonChangesetRequest{ChangesetId: abandoned.Id, Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	got, err := clients.changeset.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: abandoned.Id})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "abandoned" {
		t.Fatalf("expected abandoned status, got %#v", got)
	}
	_, err = clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{ChangesetId: abandoned.Id})
	if grpcstatus.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected abandoned submit to fail with FailedPrecondition, got %v", err)
	}
}

func TestWorkspaceServiceHelpers(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	const content = "package payment\nconst Hydrate = true\n"
	writeWorkspaceFile(t, workspace, "hydrate.go", content)
	runCLI(t, home, workspace, "cs", "create", "--title", "hydrate helper")
	runCLI(t, home, workspace, "cs", "submit")

	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	workspaceClient := corev1.NewWorkspaceServiceClient(conn)
	ref := &corev1.WorkspaceRef{Id: "acme/payment"}

	state, err := workspaceClient.GetWorkspaceState(ctx, &corev1.GetWorkspaceStateRequest{Workspace: ref})
	if err != nil {
		t.Fatal(err)
	}
	if state.BaseCommitId == "" || state.Slice == nil || state.Slice.SliceId == "" || !containsString(state.HydratedPaths, "/acme/payment") {
		t.Fatalf("unexpected workspace state: %#v", state)
	}
	hydrated, err := workspaceClient.HydratePaths(ctx, &corev1.HydratePathsRequest{
		Workspace: ref,
		Paths:     []string{"/acme/payment/hydrate.go"},
		Mode:      "file_contents",
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(hydrated.Data) != content || hydrated.Entry == nil || hydrated.Entry.Kind != corev1.EntryKind_ENTRY_KIND_FILE {
		t.Fatalf("unexpected hydration response: %#v data=%q", hydrated, string(hydrated.Data))
	}
	recorded, err := workspaceClient.RecordWorkspaceOperation(ctx, &corev1.RecordWorkspaceOperationRequest{
		Operation: &corev1.WorkspaceOperation{
			Workspace:     ref,
			OperationType: "functional_test",
			Description:   "record workspace operation",
			AffectedPaths: []string{"/acme/payment/hydrate.go"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(recorded.OperationId, "op_") {
		t.Fatalf("expected generated operation id, got %#v", recorded)
	}
	_, err = workspaceClient.GetWorkspaceState(ctx, &corev1.GetWorkspaceStateRequest{Workspace: &corev1.WorkspaceRef{Id: "bad-workspace"}})
	if grpcstatus.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected invalid workspace id to fail with InvalidArgument, got %v", err)
	}
}

func TestStaleDisjointUpdatePreservesFinalState(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	seedWorkspace := t.TempDir()
	loginTestCLI(t, ts, home, seedWorkspace)
	token := readToken(t, home)
	runCLI(t, home, seedWorkspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, seedWorkspace, "shared.go", "package payment\nconst Shared = true\n")
	runCLI(t, home, seedWorkspace, "cs", "create", "--title", "seed shared")
	runCLI(t, home, seedWorkspace, "cs", "submit")

	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	runCLI(t, home, workspaceA, "workspace", "init", "acme/payment")
	runCLI(t, home, workspaceB, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspaceA, "shared.go", "package payment\nconst Shared = false\n")
	writeWorkspaceFile(t, workspaceB, "new_disjoint.go", "package payment\nconst NewDisjoint = true\n")
	runCLI(t, home, workspaceA, "cs", "create", "--title", "update shared")
	runCLI(t, home, workspaceB, "cs", "create", "--title", "add disjoint")
	runCLI(t, home, workspaceB, "cs", "submit")
	runCLI(t, home, workspaceA, "cs", "submit")

	cloneDir := cloneSlice(t, ts, token)
	assertProjectedFile(t, cloneDir, "acme/payment/shared.go", "package payment\nconst Shared = false\n")
	assertProjectedFile(t, cloneDir, "acme/payment/new_disjoint.go", "package payment\nconst NewDisjoint = true\n")
}

func TestDeleteUpdateConflicts(t *testing.T) {
	tests := []struct {
		name        string
		firstAction string
	}{
		{name: "delete_then_update", firstAction: "delete"},
		{name: "update_then_delete", firstAction: "update"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := startTestServer(t)
			home := t.TempDir()
			seedWorkspace := t.TempDir()
			loginTestCLI(t, ts, home, seedWorkspace)
			runCLI(t, home, seedWorkspace, "workspace", "init", "acme/payment")
			writeWorkspaceFile(t, seedWorkspace, "du.go", "package payment\nconst Value = 1\n")
			runCLI(t, home, seedWorkspace, "cs", "create", "--title", "seed du")
			runCLI(t, home, seedWorkspace, "cs", "submit")

			deleteWorkspace := t.TempDir()
			updateWorkspace := t.TempDir()
			runCLI(t, home, deleteWorkspace, "workspace", "init", "acme/payment")
			runCLI(t, home, updateWorkspace, "workspace", "init", "acme/payment")
			if err := os.Remove(filepath.Join(deleteWorkspace, "acme", "payment", "du.go")); err != nil {
				t.Fatal(err)
			}
			writeWorkspaceFile(t, updateWorkspace, "acme/payment/du.go", "package payment\nconst Value = 2\n")
			runCLI(t, home, deleteWorkspace, "cs", "create", "--title", "delete du")
			runCLI(t, home, updateWorkspace, "cs", "create", "--title", "update du")

			firstWorkspace := deleteWorkspace
			secondWorkspace := updateWorkspace
			if tt.firstAction == "update" {
				firstWorkspace = updateWorkspace
				secondWorkspace = deleteWorkspace
			}
			runCLI(t, home, firstWorkspace, "cs", "submit")
			_, stderr := runCLIFails(t, home, secondWorkspace, "cs", "submit")
			assertConflictError(t, stderr)
		})
	}
}

func TestSameNewPathConcurrentOnlyOneSubmitSucceeds(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	firstWorkspace := t.TempDir()
	loginTestCLI(t, ts, home, firstWorkspace)

	const workers = 6
	workspaces := make([]string, workers)
	for i := 0; i < workers; i++ {
		workspace := t.TempDir()
		workspaces[i] = workspace
		runCLI(t, home, workspace, "workspace", "init", "acme/payment")
		writeWorkspaceFile(t, workspace, "same_new.go", fmt.Sprintf("package payment\nconst Winner = %d\n", i))
		runCLI(t, home, workspace, "cs", "create", "--title", fmt.Sprintf("same new %d", i))
	}
	var wg sync.WaitGroup
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := runCLIResult(home, workspaces[i], "cs", "submit")
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		if strings.Contains(err.Error(), "FailedPrecondition") || strings.Contains(err.Error(), "conflict") {
			conflicts++
			continue
		}
		t.Fatalf("unexpected submit error: %v", err)
	}
	if successes != 1 || conflicts != workers-1 {
		t.Fatalf("expected one success and %d conflicts, got successes=%d conflicts=%d", workers-1, successes, conflicts)
	}
}

func TestConcurrentDisjointSubmitFinalProjection(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	firstWorkspace := t.TempDir()
	loginTestCLI(t, ts, home, firstWorkspace)
	token := readToken(t, home)

	const workers = 10
	workspaces := make([]string, workers)
	for i := 0; i < workers; i++ {
		workspace := t.TempDir()
		workspaces[i] = workspace
		runCLI(t, home, workspace, "workspace", "init", "acme/payment")
		rel := fmt.Sprintf("concurrent/f_%02d.go", i)
		body := fmt.Sprintf("package concurrent\nconst File%d = %d\n", i, i)
		writeWorkspaceFile(t, workspace, rel, body)
		runCLI(t, home, workspace, "cs", "create", "--title", fmt.Sprintf("concurrent %02d", i))
	}
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := runCLIResult(home, workspaces[i], "cs", "submit"); err != nil {
				errs <- err
				return
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	cloneDir := cloneSlice(t, ts, token)
	for i := 0; i < workers; i++ {
		rel := fmt.Sprintf("acme/payment/concurrent/f_%02d.go", i)
		body := fmt.Sprintf("package concurrent\nconst File%d = %d\n", i, i)
		assertProjectedFile(t, cloneDir, rel, body)
	}
}

func TestRestartPreservesSubmittedState(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "restart.go", "package payment\n")
	runCLI(t, home, workspace, "cs", "create")
	runCLI(t, home, workspace, "cs", "submit")
	ts.restart(t)
	status := runCLI(t, home, workspace, "cs", "status")
	if !strings.Contains(status, "status: submitted") {
		t.Fatalf("expected submitted state after restart, got:\n%s", status)
	}
}

func TestGitHTTPAuthAndUnsupportedOperationMatrix(t *testing.T) {
	ts := startTestServer(t)
	token := ts.loginViaGRPC(t, "alice")
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)

	uploadInfoRefs := "/git/acme/payment.git/info/refs?service=git-upload-pack"
	statusCode, headers, body := gitHTTPRaw(t, ts.gitAddr, uploadInfoRefs, "")
	if statusCode != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated upload-pack discovery to return 401, got %d:\n%s", statusCode, string(body))
	}
	if got := headers.Get("WWW-Authenticate"); !strings.Contains(got, `Basic realm="gitslice"`) {
		t.Fatalf("expected basic auth challenge, got %q", got)
	}

	statusCode, _, body = gitHTTPRaw(t, ts.gitAddr, uploadInfoRefs, "Bearer not-a-token")
	if statusCode != http.StatusUnauthorized {
		t.Fatalf("expected invalid token upload-pack discovery to return 401, got %d:\n%s", statusCode, string(body))
	}

	outsiderToken, _, _ := ts.provisionAccount(t, "git-public-outsider", "git-public-outsider")
	statusCode, _, body = gitHTTPRaw(t, ts.gitAddr, uploadInfoRefs, "Bearer "+outsiderToken)
	if statusCode != http.StatusForbidden {
		t.Fatalf("expected private upload-pack discovery for outsider to return 403, got %d:\n%s", statusCode, string(body))
	}

	slices := corev1.NewSliceServiceClient(conn)
	payment, err := slices.ResolveSlice(ctx, &corev1.ResolveSliceRequest{Ref: &corev1.SliceRef{Account: "acme", Slice: "payment"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := slices.UpdateSliceDefinition(ctx, &corev1.UpdateSliceDefinitionRequest{
		SliceId:                payment.Id,
		ExpectedDefinitionHash: payment.DefinitionHash,
		Definition: &corev1.SliceDefinition{
			IncludedPaths: payment.Definition.IncludedPaths,
			Visibility:    "public",
		},
	}); err != nil {
		t.Fatal(err)
	}
	statusCode, headers, body = gitHTTPRaw(t, ts.gitAddr, uploadInfoRefs, "Bearer "+outsiderToken)
	if statusCode != http.StatusOK {
		t.Fatalf("expected public upload-pack discovery for outsider to return 200, got %d:\n%s", statusCode, string(body))
	}
	if got := headers.Get("Content-Type"); !strings.Contains(got, "application/x-git-upload-pack-advertisement") {
		t.Fatalf("expected public upload-pack advertisement content type, got %q", got)
	}

	basicAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:"+token))
	statusCode, headers, body = gitHTTPRaw(t, ts.gitAddr, uploadInfoRefs, basicAuth)
	if statusCode != http.StatusOK {
		t.Fatalf("expected basic-auth upload-pack discovery to return 200, got %d:\n%s", statusCode, string(body))
	}
	if got := headers.Get("Content-Type"); !strings.Contains(got, "application/x-git-upload-pack-advertisement") {
		t.Fatalf("expected upload-pack advertisement content type, got %q", got)
	}

	statusCode, _, body = gitHTTPRaw(t, ts.gitAddr, "/git/acme/missing.git/info/refs?service=git-upload-pack", "Bearer "+token)
	if statusCode != http.StatusNotFound {
		t.Fatalf("expected missing slice to return 404, got %d:\n%s", statusCode, string(body))
	}

	receiveInfoRefs := "/git/acme/payment.git/info/refs?service=git-receive-pack"
	statusCode, headers, body = gitHTTPRaw(t, ts.gitAddr, receiveInfoRefs, "")
	if statusCode != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated receive-pack discovery to return 401, got %d:\n%s", statusCode, string(body))
	}
	if got := headers.Get("WWW-Authenticate"); !strings.Contains(got, `Basic realm="gitslice"`) {
		t.Fatalf("expected receive-pack auth challenge, got %q", got)
	}

	statusCode, headers, body = gitHTTPRaw(t, ts.gitAddr, receiveInfoRefs, "Bearer "+token)
	if statusCode != http.StatusOK {
		t.Fatalf("expected authenticated receive-pack discovery to return 200, got %d:\n%s", statusCode, string(body))
	}
	if !strings.Contains(headers.Get("Content-Type"), "application/x-git-receive-pack-advertisement") {
		t.Fatalf("expected receive-pack advertisement content type, got %q", headers.Get("Content-Type"))
	}

	statusCode, _, body = gitHTTPRaw(t, ts.gitAddr, "/not-git/acme/payment.git/info/refs?service=git-upload-pack", "Bearer "+token)
	if statusCode != http.StatusNotFound {
		t.Fatalf("expected non-git route to return 404, got %d:\n%s", statusCode, string(body))
	}
}

func TestGitCloneProjection(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "git_layer.go", "package payment\nconst GitLayer = true\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "git projection")
	runCLI(t, home, workspace, "cs", "submit")

	cloneDir := filepath.Join(t.TempDir(), "payment")
	gitURL := "http://" + ts.gitAddr + "/git/acme/payment.git"
	_, stderr, err := runGitResult("", "clone", gitURL, filepath.Join(t.TempDir(), "unauthenticated-payment"))
	if err == nil {
		t.Fatal("expected unauthenticated git clone to be rejected")
	}
	if !strings.Contains(stderr, "401") &&
		!strings.Contains(stderr, "Authentication failed") &&
		!strings.Contains(stderr, "authentication") &&
		!strings.Contains(stderr, "Username") {
		t.Fatalf("expected unauthenticated clone rejection, got stderr:\n%s", stderr)
	}
	runGit(t, "", "-c", "http.extraHeader=Authorization: Bearer "+token, "clone", gitURL, cloneDir)
	projected, err := os.ReadFile(filepath.Join(cloneDir, "acme", "payment", "git_layer.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(projected) != "package payment\nconst GitLayer = true\n" {
		t.Fatalf("unexpected projected file contents:\n%s", string(projected))
	}
	writeWorkspaceFile(t, workspace, "git_fetch.go", "package payment\nconst GitFetch = true\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "git fetch projection")
	runCLI(t, home, workspace, "cs", "submit")
	runGit(t, "", "-C", cloneDir, "-c", "http.extraHeader=Authorization: Bearer "+token, "fetch", "origin", "main")
	runGit(t, "", "-C", cloneDir, "checkout", "-B", "main", "origin/main")
	projectedFetch, err := os.ReadFile(filepath.Join(cloneDir, "acme", "payment", "git_fetch.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(projectedFetch) != "package payment\nconst GitFetch = true\n" {
		t.Fatalf("unexpected fetched file contents:\n%s", string(projectedFetch))
	}
}

func TestGitPushIntoChangesets(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "git_push_base.go", "package payment\nconst GitPushBase = true\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "git push base")
	runCLI(t, home, workspace, "cs", "submit")

	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	changesets := corev1.NewChangesetServiceClient(conn)

	cloneDir := filepath.Join(t.TempDir(), "payment")
	gitURL := "http://" + ts.gitAddr + "/git/acme/payment.git"
	runGit(t, "", "-c", "http.extraHeader=Authorization: Bearer "+token, "clone", gitURL, cloneDir)
	runGit(t, "", "-C", cloneDir, "config", "user.name", "Git Pusher")
	runGit(t, "", "-C", cloneDir, "config", "user.email", "git-pusher@example.invalid")

	writeWorkspaceFile(t, cloneDir, "acme/payment/git_push_one.go", "package payment\nconst GitPushOne = true\n")
	runGit(t, "", "-C", cloneDir, "add", "acme/payment/git_push_one.go")
	runGit(t, "", "-C", cloneDir, "commit", "-m", "git push first patchset")
	_, stderr, err := runGitResult("", "-C", cloneDir, "-c", "http.extraHeader=Authorization: Bearer "+token, "push", "origin", "HEAD:refs/changes/new")
	if err != nil {
		t.Fatalf("git push refs/changes/new failed: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "Created changeset acme:payment@") {
		t.Fatalf("push output missing created changeset handle:\n%s", stderr)
	}
	draft := singleDraftChangeset(t, ctx, changesets)
	if len(draft.Patchsets) != 1 {
		t.Fatalf("draft patchset count = %d, want 1: %#v", len(draft.Patchsets), draft)
	}
	if !containsString(draft.Patchsets[0].ChangedPaths, "/acme/payment/git_push_one.go") {
		t.Fatalf("first patchset changed paths = %#v", draft.Patchsets[0].ChangedPaths)
	}

	writeWorkspaceFile(t, cloneDir, "acme/payment/git_push_two.go", "package payment\nconst GitPushTwo = true\n")
	runGit(t, "", "-C", cloneDir, "add", "acme/payment/git_push_two.go")
	runGit(t, "", "-C", cloneDir, "commit", "-m", "git push second patchset")
	_, stderr, err = runGitResult("", "-C", cloneDir, "-c", "http.extraHeader=Authorization: Bearer "+token, "push", "origin", "HEAD:refs/changes/"+strconv.FormatInt(draft.Number, 10))
	if err != nil {
		t.Fatalf("git push existing changeset failed: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "Updated changeset "+draft.Handle) {
		t.Fatalf("push output missing updated changeset handle:\n%s", stderr)
	}
	updated, err := changesets.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: draft.Id})
	if err != nil {
		t.Fatal(err)
	}
	if updated.CurrentPatchsetNumber != 2 || len(updated.Patchsets) != 2 {
		t.Fatalf("updated changeset patchsets = current %d count %d, want current 2 count 2", updated.CurrentPatchsetNumber, len(updated.Patchsets))
	}
	second := updated.Patchsets[1]
	for _, want := range []string{"/acme/payment/git_push_one.go", "/acme/payment/git_push_two.go"} {
		if !containsString(second.ChangedPaths, want) {
			t.Fatalf("second patchset changed paths missing %s: %#v", want, second.ChangedPaths)
		}
	}

	_, stderr, err = runGitResult("", "-C", cloneDir, "-c", "http.extraHeader=Authorization: Bearer "+token, "push", "origin", "HEAD:refs/heads/main")
	if err == nil {
		t.Fatal("expected protected branch push to be rejected")
	}
	if !strings.Contains(stderr, "protected") && !strings.Contains(stderr, "refs/changes/new") {
		t.Fatalf("protected branch rejection did not guide to changes refs:\n%s", stderr)
	}

	outsiderToken, _, _ := ts.provisionAccount(t, "git-push-outsider", "git-push-outsider")
	_, stderr, err = runGitResult("", "-C", cloneDir, "-c", "http.extraHeader=Authorization: Bearer "+outsiderToken, "push", "origin", "HEAD:refs/changes/new")
	if err == nil {
		t.Fatal("expected unauthorized git push to be rejected")
	}
	if !strings.Contains(stderr, "403") && !strings.Contains(strings.ToLower(stderr), "permission") {
		t.Fatalf("unauthorized push did not fail with auth guidance:\n%s", stderr)
	}

	runCLI(t, home, workspace, "cs", "submit", updated.Handle)
	submitted := waitForSubmittedChangeset(t, ctx, changesets, updated.Id)
	if !containsString(submitted.AffectedPaths, "/acme/payment/git_push_two.go") {
		t.Fatalf("submitted changeset affected paths = %#v", submitted.AffectedPaths)
	}
}

func TestGitImportShallow(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	sourceRepo := createImportGitRepo(t)
	loginTestCLI(t, ts, home, workspace)

	raw := runCLI(t, home, workspace,
		"import", sourceRepo,
		"--mount", "/acme/payment/imported/shallow",
		"--slice", "acme/payment",
		"--mode", "shallow",
		"--json",
	)
	var imported struct {
		FinalCommitID string `json:"final_commit_id"`
		Commits       []struct {
			NativeCommitID string `json:"native_commit_id"`
			Message        string `json:"message"`
		} `json:"commits"`
	}
	if err := json.Unmarshal([]byte(raw), &imported); err != nil {
		t.Fatal(err)
	}
	if len(imported.Commits) != 1 {
		t.Fatalf("shallow import commits = %d, want 1: %s", len(imported.Commits), raw)
	}
	if imported.Commits[0].Message != "third commit" {
		t.Fatalf("shallow import message = %q, want third commit", imported.Commits[0].Message)
	}
	inspect := runCLI(t, home, workspace, "show", imported.FinalCommitID)
	if !strings.Contains(inspect, "third commit") ||
		!strings.Contains(inspect, "/acme/payment/imported/shallow/README.md") {
		t.Fatalf("unexpected inspect output:\n%s", inspect)
	}
}

func TestGitImportDefaultsAuthoringSliceFromMount(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	sourceRepo := createImportGitRepo(t)
	loginTestCLI(t, ts, home, workspace)

	// No --slice and no workspace: the authoring slice defaults to the mount
	// path's account home slice (acme:home), which covers /acme.
	raw := runCLI(t, home, workspace,
		"import", sourceRepo,
		"--mount", "/acme/imported/no-slice",
		"--mode", "shallow",
		"--json",
	)
	var imported struct {
		FinalCommitID string `json:"final_commit_id"`
		Commits       []struct {
			Message string `json:"message"`
		} `json:"commits"`
	}
	if err := json.Unmarshal([]byte(raw), &imported); err != nil {
		t.Fatal(err)
	}
	if len(imported.Commits) != 1 {
		t.Fatalf("default-slice import commits = %d, want 1: %s", len(imported.Commits), raw)
	}
	inspect := runCLI(t, home, workspace, "show", imported.FinalCommitID)
	if !strings.Contains(inspect, "/acme/imported/no-slice/README.md") {
		t.Fatalf("unexpected inspect output:\n%s", inspect)
	}
}

func TestGitImportProgressText(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	sourceRepo := createImportGitRepo(t)
	loginTestCLI(t, ts, home, workspace)

	stdout, stderr := runCLIStreams(t, home, workspace,
		"import", sourceRepo,
		"--mount", "/acme/payment/imported/progress",
		"--slice", "acme/payment",
		"--mode", "shallow",
	)
	if !strings.Contains(stdout, "imported 1 commit(s)") ||
		!strings.Contains(stdout, "final commit:") {
		t.Fatalf("unexpected import stdout:\n%s", stdout)
	}
	for _, want := range []string{"cloning repository", "found 1 commit(s)", "import complete"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("import stderr missing %q:\n%s", want, stderr)
		}
	}
}

func TestGitImportDeepListAndInspectCommits(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	sourceRepo := createImportGitRepo(t)
	loginTestCLI(t, ts, home, workspace)
	token := readToken(t, home)

	raw := runCLI(t, home, workspace,
		"import", sourceRepo,
		"--mount", "/acme/payment/imported/deep",
		"--slice", "acme/payment",
		"--mode", "deep",
		"--json",
	)
	var imported struct {
		FinalCommitID string `json:"final_commit_id"`
		Commits       []struct {
			NativeCommitID string `json:"native_commit_id"`
			Message        string `json:"message"`
		} `json:"commits"`
	}
	if err := json.Unmarshal([]byte(raw), &imported); err != nil {
		t.Fatal(err)
	}
	if len(imported.Commits) != 3 {
		t.Fatalf("deep import commits = %d, want 3: %s", len(imported.Commits), raw)
	}
	for i, want := range []string{"first commit", "second commit", "third commit"} {
		if imported.Commits[i].Message != want {
			t.Fatalf("commit %d message = %q, want %q", i, imported.Commits[i].Message, want)
		}
	}
	log := runCLI(t, home, workspace, "log", "--all", "--limit", "4")
	for _, want := range []string{"third commit", "second commit", "first commit"} {
		if !strings.Contains(log, want) {
			t.Fatalf("log missing %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "sha256:") {
		t.Fatalf("log should use short ids by default:\n%s", log)
	}
	secondShort := shortCommitPrefixForTest(t, imported.Commits[1].NativeCommitID)
	if !strings.Contains(log, secondShort+"  second commit") {
		t.Fatalf("log missing short id %s for second commit:\n%s", secondShort, log)
	}
	libList := runCLI(t, home, workspace, "log", "--all", "--path", "/acme/payment/imported/deep/lib")
	if !strings.Contains(libList, "second commit") || strings.Contains(libList, "first commit") || strings.Contains(libList, "third commit") {
		t.Fatalf("path-filtered lib history is wrong:\n%s", libList)
	}
	readmeList := runCLI(t, home, workspace, "log", "--all", "--", "/acme/payment/imported/deep/README.md")
	if !strings.Contains(readmeList, "first commit") || !strings.Contains(readmeList, "third commit") || strings.Contains(readmeList, "second commit") {
		t.Fatalf("path-filtered README history is wrong:\n%s", readmeList)
	}
	firstPageRaw := runCLI(t, home, workspace, "log", "--all", "--limit", "1", "--json", "--", "/acme/payment/imported/deep/README.md")
	var firstPage struct {
		Commits []struct {
			Message string `json:"message"`
		} `json:"commits"`
		NextPageToken string `json:"next_page_token"`
	}
	if err := json.Unmarshal([]byte(firstPageRaw), &firstPage); err != nil {
		t.Fatal(err)
	}
	if len(firstPage.Commits) != 1 || firstPage.Commits[0].Message != "third commit" || firstPage.NextPageToken == "" {
		t.Fatalf("unexpected first commit page: %#v raw=%s", firstPage, firstPageRaw)
	}
	secondPageRaw := runCLI(t, home, workspace, "log", "--all", "--limit", "1", "--page-token", firstPage.NextPageToken, "--json", "--", "/acme/payment/imported/deep/README.md")
	var secondPage struct {
		Commits []struct {
			Message string `json:"message"`
		} `json:"commits"`
		NextPageToken string `json:"next_page_token"`
	}
	if err := json.Unmarshal([]byte(secondPageRaw), &secondPage); err != nil {
		t.Fatal(err)
	}
	if len(secondPage.Commits) != 1 || secondPage.Commits[0].Message != "first commit" {
		t.Fatalf("unexpected second commit page: %#v raw=%s", secondPage, secondPageRaw)
	}
	if secondPage.NextPageToken != "" {
		thirdPageRaw := runCLI(t, home, workspace, "log", "--all", "--limit", "1", "--page-token", secondPage.NextPageToken, "--json", "--", "/acme/payment/imported/deep/README.md")
		var thirdPage struct {
			Commits []struct {
				Message string `json:"message"`
			} `json:"commits"`
			NextPageToken string `json:"next_page_token"`
		}
		if err := json.Unmarshal([]byte(thirdPageRaw), &thirdPage); err != nil {
			t.Fatal(err)
		}
		if len(thirdPage.Commits) != 1 || thirdPage.Commits[0].Message != "bootstrap acme test slices" || thirdPage.NextPageToken != "" {
			t.Fatalf("unexpected third commit page: %#v raw=%s", thirdPage, thirdPageRaw)
		}
	}
	show := runCLI(t, home, workspace, "show", secondShort)
	if !strings.Contains(show, "commit "+secondShort) ||
		!strings.Contains(show, "second commit") ||
		!strings.Contains(show, "/acme/payment/imported/deep/lib/code.go") {
		t.Fatalf("unexpected show output for short id %s:\n%s", secondShort, show)
	}
	showRaw := runCLI(t, home, workspace, "show", secondShort, "--json")
	var shown struct {
		ID      string `json:"id"`
		ShortID string `json:"short_id"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(showRaw), &shown); err != nil {
		t.Fatal(err)
	}
	if shown.ID != imported.Commits[1].NativeCommitID || shown.ShortID != secondShort || shown.Message != "second commit" {
		t.Fatalf("unexpected show json: %#v raw=%s", shown, showRaw)
	}
	diffNameOnly := runCLI(t, home, workspace, "diff", secondShort, "--name-only")
	if !strings.Contains(diffNameOnly, "/acme/payment/imported/deep/lib/code.go") {
		t.Fatalf("commit diff --name-only missing changed path:\n%s", diffNameOnly)
	}
	cloneDir := cloneSlice(t, ts, token)
	assertProjectedFile(t, cloneDir, "acme/payment/imported/deep/README.md", "hello v2\n")
	assertProjectedFile(t, cloneDir, "acme/payment/imported/deep/lib/code.go", "package lib\nconst Value = 1\n")
}

func TestGitImportDeepMaxCommitsAndResume(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	sourceRepo := createImportGitRepo(t)
	loginTestCLI(t, ts, home, workspace)

	raw := runCLI(t, home, workspace,
		"import", sourceRepo,
		"--mount", "/acme/payment/imported/bounded",
		"--slice", "acme/payment",
		"--mode", "deep",
		"--max-commits", "2",
		"--json",
	)
	var imported struct {
		Commits []struct {
			Message string `json:"message"`
		} `json:"commits"`
	}
	if err := json.Unmarshal([]byte(raw), &imported); err != nil {
		t.Fatal(err)
	}
	if len(imported.Commits) != 2 {
		t.Fatalf("bounded deep import commits = %d, want 2: %s", len(imported.Commits), raw)
	}
	for i, want := range []string{"second commit", "third commit"} {
		if imported.Commits[i].Message != want {
			t.Fatalf("commit %d message = %q, want %q", i, imported.Commits[i].Message, want)
		}
	}

	stdout, stderr := runCLIStreams(t, home, workspace,
		"import", sourceRepo,
		"--mount", "/acme/payment/imported/bounded",
		"--slice", "acme/payment",
		"--mode", "deep",
		"--max-commits", "2",
	)
	if !strings.Contains(stdout, "imported 2 commit(s)") {
		t.Fatalf("unexpected resume stdout:\n%s", stdout)
	}
	if !strings.Contains(stderr, "skipped") {
		t.Fatalf("resume stderr missing skipped progress:\n%s", stderr)
	}
}

type testServer struct {
	addr        string
	httpAddr    string
	gitAddr     string
	ctx         context.Context
	cancel      context.CancelFunc
	errCh       chan error
	databaseURL string
	schema      string
	objectRoot  string
	servicePriv string
	servicePub  string

	mu               sync.Mutex
	defaultReady     bool
	defaultToken     string
	defaultSubjectID string
	memberTokens     map[string]string
}

func startTestServer(t *testing.T) *testServer {
	t.Helper()
	databaseURL := os.Getenv("GITSLICE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set GITSLICE_TEST_DATABASE_URL to run real Postgres CLI e2e tests")
	}
	schema := uniqueSchema("gitslice_test_", t)
	createSchema(t, databaseURL, schema)
	servicePriv, servicePub, err := servicetoken.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	ts := &testServer{
		addr:         freeAddr(t),
		httpAddr:     freeAddr(t),
		gitAddr:      freeAddr(t),
		databaseURL:  databaseURL,
		schema:       schema,
		objectRoot:   t.TempDir(),
		servicePriv:  servicePriv,
		servicePub:   servicePub,
		memberTokens: map[string]string{},
	}
	ts.start(t, true)
	t.Cleanup(func() {
		ts.stop(t)
		dropSchema(t, databaseURL, schema)
	})
	return ts
}

func (ts *testServer) start(t *testing.T, migrate bool) {
	t.Helper()
	ts.ctx, ts.cancel = context.WithCancel(context.Background())
	ts.errCh = make(chan error, 1)
	go func() {
		ts.errCh <- server.Run(ts.ctx, server.Config{
			GRPCAddr:          ts.addr,
			HTTPAddr:          ts.httpAddr,
			HTTPAllowedOrigin: "http://web.test",
			GitHTTPAddr:       ts.gitAddr,
			GitCacheRoot:      filepath.Join(ts.objectRoot, "git-cache"),
			DatabaseURL:       databaseURLWithSearchPath(t, ts.databaseURL, ts.schema),
			ObjectStoreRoot:   ts.objectRoot,
			ServiceToken: servicetoken.Config{
				PublicKeyPEM: ts.servicePub,
				Issuer:       servicetoken.DefaultIssuer,
			},
			RunMigrations: migrate,
		})
	}()
	waitForHealth(t, ts.addr)
	waitForHTTPGateway(t, ts.httpAddr)
	waitForGitHTTP(t, ts.gitAddr)
}

func (ts *testServer) stop(t *testing.T) {
	t.Helper()
	if ts.cancel == nil {
		return
	}
	ts.cancel()
	err := <-ts.errCh
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("server exited with error: %v", err)
	}
	ts.cancel = nil
}

func (ts *testServer) restart(t *testing.T) {
	t.Helper()
	ts.stop(t)
	ts.start(t, false)
}

func shortCommitPrefixForTest(t *testing.T, id string) string {
	t.Helper()
	hexPart := strings.TrimPrefix(id, "sha256:")
	if len(hexPart) < 12 {
		t.Fatalf("commit id too short for short prefix: %s", id)
	}
	return hexPart[:12]
}

func runCLI(t *testing.T, home, workspace string, args ...string) string {
	t.Helper()
	stdout, _ := runCLIStreams(t, home, workspace, args...)
	return stdout
}

func runCLIStreams(t *testing.T, home, workspace string, args ...string) (string, string) {
	t.Helper()
	return runCLIStreamsWithInput(t, home, workspace, "", args...)
}

func runCLIStreamsWithInput(t *testing.T, home, workspace, stdin string, args ...string) (string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	r := cli.Runner{Home: home, Dir: workspace, Stdin: strings.NewReader(stdin), Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), args); err != nil {
		t.Fatalf("gs %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String()
}

func runCLIFails(t *testing.T, home, workspace string, args ...string) (string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	r := cli.Runner{Home: home, Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), args); err == nil {
		t.Fatalf("gs %s unexpectedly succeeded\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), stdout.String(), stderr.String())
	} else {
		fmt.Fprintln(&stderr, err)
	}
	return stdout.String(), stderr.String()
}

func runCLIResult(home, workspace string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	r := cli.Runner{Home: home, Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), args); err != nil {
		return stdout.String(), fmt.Errorf("gs %s failed: %w\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String(), nil
}

// runCLISignupThroughWeb provisions a brand-new account for username and writes
// the CLI auth config so subsequent gs commands in `home` run as that user. It
// mints a service token and calls ChooseUsername (the dev signup flow is gone);
// the username is normalized to a slug exactly as the old signup path did.
func runCLISignupThroughWeb(t *testing.T, home, dir string, ts *testServer, username string) {
	t.Helper()
	_ = dir
	token, _, subjectID := ts.provisionAccount(t, username, username)
	writeCLIAuthConfig(t, home, ts.addr, token, subjectID)
}

func readSignupApprovalURL(t *testing.T, r io.Reader) string {
	t.Helper()
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			return line
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("approval URL was not printed")
	return ""
}

type testCoreClients struct {
	repository corev1.RepositoryServiceClient
	blob       corev1.BlobServiceClient
	changeset  corev1.ChangesetServiceClient
}

func newTestCoreClients(conn *grpc.ClientConn) testCoreClients {
	return testCoreClients{
		repository: corev1.NewRepositoryServiceClient(conn),
		blob:       corev1.NewBlobServiceClient(conn),
		changeset:  corev1.NewChangesetServiceClient(conn),
	}
}

func dialTestGRPC(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func loginTestCLI(t *testing.T, ts *testServer, home, workspace string) (string, string) {
	t.Helper()
	_ = workspace
	token, subjectID := ts.defaultAcmeCredentials(t)
	writeCLIAuthConfig(t, home, ts.addr, token, subjectID)
	return token, subjectID
}

func writeCLIAuthConfig(t *testing.T, home, serverAddr, token, subjectID string) {
	t.Helper()
	configDir := filepath.Join(home, ".gitslice")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(cli.UserConfig{
		ServerAddr: serverAddr,
		Token:      token,
		SubjectID:  subjectID,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (ts *testServer) loginViaGRPC(t *testing.T, user string) string {
	t.Helper()
	switch user {
	case "alice", "acme":
		token, _ := ts.defaultAcmeCredentials(t)
		return token
	case "bob":
		return ts.acmeMemberToken(t, "bob", "writer")
	default:
		token, _, _ := ts.provisionAccount(t, user, user)
		return token
	}
}

func (ts *testServer) defaultAcmeCredentials(t *testing.T) (string, string) {
	t.Helper()
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.defaultReady {
		return ts.defaultToken, ts.defaultSubjectID
	}
	ts.clearSeedAcme(t)
	token, account, subjectID := ts.provisionAccount(t, "acme", "acme-owner")
	if account != "acme" {
		t.Fatalf("provisioned account = %q, want acme", account)
	}
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	ts.createDefaultAcmeSlices(t, ctx, conn)
	ts.defaultToken = token
	ts.defaultSubjectID = subjectID
	ts.defaultReady = true
	return token, subjectID
}

func (ts *testServer) acmeMemberToken(t *testing.T, username, role string) string {
	t.Helper()
	ts.defaultAcmeCredentials(t)
	ts.mu.Lock()
	if token := ts.memberTokens[username]; token != "" {
		ts.mu.Unlock()
		return token
	}
	ts.mu.Unlock()

	token, _, subjectID := ts.provisionAccount(t, username, username)
	ts.grantAccountRole(t, subjectID, "acme", role)

	ts.mu.Lock()
	ts.memberTokens[username] = token
	ts.mu.Unlock()
	return token
}

func (ts *testServer) mintToken(t *testing.T, subject, email string) string {
	t.Helper()
	token, err := servicetoken.Mint(ts.servicePriv, subject, email, servicetoken.DefaultIssuer, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func (ts *testServer) provisionAccount(t *testing.T, username, label string) (string, string, string) {
	t.Helper()
	subject := "svc_" + testTokenLabel(t, label)
	email := testTokenLabel(t, label) + "@test.local"
	token := ts.mintToken(t, subject, email)
	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	ctx := grpcAuthContext(token)
	chosen, err := corev1.NewAuthServiceClient(conn).ChooseUsername(ctx, &corev1.ChooseUsernameRequest{Username: username})
	if err != nil {
		t.Fatal(err)
	}
	if chosen.Account == "" || chosen.SubjectId == "" {
		t.Fatalf("incomplete ChooseUsername response: %#v", chosen)
	}
	return token, chosen.Account, chosen.SubjectId
}

func (ts *testServer) clearSeedAcme(t *testing.T) {
	t.Helper()
	db, err := sql.Open("pgx", databaseURLWithSearchPath(t, ts.databaseURL, ts.schema))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`delete from slice_included_paths where slice_id in (select id from slices where account_id = 'acct_acme')`,
		`delete from slice_definition_versions where slice_id in (select id from slices where account_id = 'acct_acme')`,
		`delete from slices where account_id = 'acct_acme'`,
		`delete from account_memberships where account_id = 'acct_acme'`,
		`delete from current_path_entities where account_id = 'acct_acme'`,
		`delete from commit_entity_changes where account_id = 'acct_acme'`,
		`delete from fs_entities where account_id = 'acct_acme'`,
		`delete from accounts where id = 'acct_acme' and slug = 'acme'`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			t.Fatalf("clear seeded acme: %v\nsql: %s", err, stmt)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func (ts *testServer) createDefaultAcmeSlices(t *testing.T, ctx context.Context, conn *grpc.ClientConn) {
	t.Helper()
	clients := newTestCoreClients(conn)
	slices := corev1.NewSliceServiceClient(conn)
	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{RefName: postgres.DefaultTargetRef})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "home"},
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          "bootstrap acme test slices",
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{
			{Op: "mkdir", Path: "/acme/payment"},
			{Op: "mkdir", Path: "/acme/payment/shared"},
			{Op: "mkdir", Path: "/acme/backend"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.changeset.SubmitChangeset(ctx, &corev1.SubmitChangesetRequest{
		ChangesetId:               cs.Id,
		ExpectedCurrentPatchsetId: patchset.Id,
	}); err != nil {
		t.Fatal(err)
	}
	waitForSubmittedChangeset(t, ctx, clients.changeset, cs.Id)
	if _, err := slices.CreateSlice(ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "acme", Slice: "payment"},
		IncludedPaths: []string{"/acme/payment"},
		Visibility:    "private",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := slices.CreateSlice(ctx, &corev1.CreateSliceRequest{
		Ref:           &corev1.SliceRef{Account: "acme", Slice: "backend"},
		IncludedPaths: []string{"/acme/backend", "/acme/payment/shared"},
		Visibility:    "private",
	}); err != nil {
		t.Fatal(err)
	}
}

func (ts *testServer) grantAccountRole(t *testing.T, subjectID, account, role string) {
	t.Helper()
	db, err := sql.Open("pgx", databaseURLWithSearchPath(t, ts.databaseURL, ts.schema))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	res, err := db.Exec(`
		insert into account_memberships(account_id, subject_id, role, created_at)
		select id, $1, $2, now()
		from accounts
		where slug = $3
		on conflict do nothing
	`, subjectID, role, account)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		t.Fatalf("grant %s on %s to %s affected %d rows, err=%v", role, account, subjectID, n, err)
	}
}

func testTokenLabel(t *testing.T, label string) string {
	t.Helper()
	raw := strings.ToLower(t.Name() + "_" + label)
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func grpcAuthContext(token string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)
}

func createDirectPatchset(t *testing.T, ctx context.Context, clients testCoreClients, path, content, title string) (string, string) {
	t.Helper()
	ref, err := clients.repository.GetRef(ctx, &corev1.GetRefRequest{})
	if err != nil {
		t.Fatal(err)
	}
	upload, err := clients.blob.UploadBlob(ctx, &corev1.UploadBlobRequest{Data: []byte(content), Slice: &corev1.SliceRef{Account: "acme", Slice: "payment"}})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := clients.changeset.CreateChangeset(ctx, &corev1.CreateChangesetRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		TargetRef:      postgres.DefaultTargetRef,
		BaseCommitId:   ref.CommitId,
		Title:          title,
	})
	if err != nil {
		t.Fatal(err)
	}
	patchset, err := clients.changeset.UpdateChangeset(ctx, &corev1.UpdateChangesetRequest{
		ChangesetId:  cs.Id,
		BaseCommitId: ref.CommitId,
		FileEdits: []*corev1.FileEdit{{
			Op:          "add",
			Path:        path,
			BlobId:      upload.BlobId,
			ContentHash: upload.ContentHash,
			Mode:        0o644,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return cs.Id, patchset.Id
}

func waitForSubmittedChangeset(t *testing.T, ctx context.Context, client corev1.ChangesetServiceClient, changesetID string) *corev1.Changeset {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last *corev1.Changeset
	for time.Now().Before(deadline) {
		cs, err := client.GetChangeset(ctx, &corev1.GetChangesetRequest{ChangesetId: changesetID})
		if err != nil {
			t.Fatal(err)
		}
		last = cs
		if cs.Status == "submitted" && cs.CommitId != "" {
			return cs
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("changeset %s did not reach submitted status, last=%#v", changesetID, last)
	return nil
}

func singleDraftChangeset(t *testing.T, ctx context.Context, client corev1.ChangesetServiceClient) *corev1.Changeset {
	t.Helper()
	res, err := client.ListChangesets(ctx, &corev1.ListChangesetsRequest{
		AuthoringSlice: &corev1.SliceRef{Account: "acme", Slice: "payment"},
		Status:         "draft",
		Limit:          10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changesets) != 1 {
		t.Fatalf("draft changeset count = %d, want 1: %#v", len(res.Changesets), res.Changesets)
	}
	return res.Changesets[0]
}

func writeWorkspaceFile(t *testing.T, workspace, rel, content string) {
	t.Helper()
	writeWorkspaceFileBytes(t, workspace, rel, []byte(content))
}

func writeWorkspaceFileBytes(t *testing.T, workspace, rel string, content []byte) {
	t.Helper()
	path := filepath.Join(workspace, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertWorkspaceFile(t *testing.T, workspace, rel, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("unexpected workspace file %s:\nwant:\n%s\ngot:\n%s", rel, want, string(got))
	}
}

func assertWorkspaceDir(t *testing.T, workspace, rel string) {
	t.Helper()
	info, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("workspace path %s is not a directory", rel)
	}
}

func submittedRefCommitID(t *testing.T, raw string) string {
	t.Helper()
	var res struct {
		NewRefCommitID string `json:"new_ref_commit_id"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("submit output is not JSON: %v\n%s", err, raw)
	}
	if res.NewRefCommitID == "" {
		t.Fatalf("submit output missing new_ref_commit_id:\n%s", raw)
	}
	return res.NewRefCommitID
}

func parseChangesetOutput(t *testing.T, raw string) (string, string, string, int64) {
	t.Helper()
	var res struct {
		ChangesetID     string `json:"changeset_id"`
		PatchsetID      string `json:"patchset_id"`
		ChangesetHandle string `json:"changeset_handle"`
		PatchsetNumber  int64  `json:"patchset_number"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("changeset output is not JSON: %v\n%s", err, raw)
	}
	if res.ChangesetID == "" || res.PatchsetID == "" || res.ChangesetHandle == "" || res.PatchsetNumber == 0 {
		t.Fatalf("changeset output missing ids or handle:\n%s", raw)
	}
	if strings.Contains(res.ChangesetHandle, "!") || !strings.Contains(res.ChangesetHandle, "@") {
		t.Fatalf("changeset handle is not shell-safe: %q", res.ChangesetHandle)
	}
	return res.ChangesetID, res.PatchsetID, res.ChangesetHandle, res.PatchsetNumber
}

func createImportGitRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.name", "Import Tester")
	runGit(t, repo, "config", "user.email", "importer@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "first commit")
	if err := os.MkdirAll(filepath.Join(repo, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "lib", "code.go"), []byte("package lib\nconst Value = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "lib/code.go")
	runGit(t, repo, "commit", "-m", "second commit")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "third commit")
	return repo
}

func cloneSlice(t *testing.T, ts *testServer, token string) string {
	t.Helper()
	cloneDir := filepath.Join(t.TempDir(), "payment")
	gitURL := "http://" + ts.gitAddr + "/git/acme/payment.git"
	runGit(t, "", "-c", "http.extraHeader=Authorization: Bearer "+token, "clone", gitURL, cloneDir)
	return cloneDir
}

func assertProjectedFile(t *testing.T, cloneDir, rel, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(cloneDir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("unexpected contents for %s:\nwant:\n%s\ngot:\n%s", rel, want, string(got))
	}
}

func assertConflictError(t *testing.T, stderr string) {
	t.Helper()
	if !strings.Contains(stderr, "FailedPrecondition") && !strings.Contains(stderr, "conflict") {
		t.Fatalf("expected conflict error, got stderr:\n%s", stderr)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasEntry(entries []*corev1.TreeEntry, name string, kind corev1.EntryKind) bool {
	for _, entry := range entries {
		if entry.Name == name && entry.Kind == kind {
			return true
		}
	}
	return false
}

func uploadTestFileCount(t *testing.T) int {
	t.Helper()
	const defaultFileCount = 256
	value := os.Getenv("GITSLICE_UPLOAD_TEST_FILES")
	if value == "" {
		return defaultFileCount
	}
	count, err := strconv.Atoi(value)
	if err != nil || count <= 0 {
		t.Fatalf("GITSLICE_UPLOAD_TEST_FILES must be a positive integer, got %q", value)
	}
	return count
}

func countRemoteTree(t *testing.T, ctx context.Context, repo corev1.RepositoryServiceClient, commitID, p string) (int, int) {
	t.Helper()
	files := 0
	dirs := 0
	cursor := ""
	for {
		list, err := repo.ListDirectory(ctx, &corev1.ListDirectoryRequest{CommitId: commitID, Path: p, PageSize: 1000, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range list.Entries {
			switch entry.Kind {
			case corev1.EntryKind_ENTRY_KIND_FILE:
				files++
			case corev1.EntryKind_ENTRY_KIND_DIRECTORY:
				dirs++
				childFiles, childDirs := countRemoteTree(t, ctx, repo, commitID, entry.Path)
				files += childFiles
				dirs += childDirs
			}
		}
		if list.NextCursor == "" {
			return files, dirs
		}
		if list.NextCursor == cursor {
			t.Fatalf("directory cursor did not advance for %s", p)
		}
		cursor = list.NextCursor
	}
}

func readToken(t *testing.T, home string) string {
	t.Helper()
	var cfg struct {
		Token string `json:"token"`
	}
	if err := readJSONFile(filepath.Join(home, ".gitslice", "config.json"), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Token == "" {
		t.Fatal("empty token in CLI config")
	}
	return cfg.Token
}

func assertWorkspaceMetadataDoesNotContainToken(t *testing.T, workspace, token string) {
	t.Helper()
	if token == "" {
		t.Fatal("test setup produced empty token")
	}
	for _, rel := range []string{".gs/slice.json", ".gs/state.json", ".gs/base_snapshot.json"} {
		if _, err := os.Stat(filepath.Join(workspace, rel)); err != nil {
			t.Fatalf("expected workspace metadata %s: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(workspace, ".gs", "config.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace must not store auth config in .gs/config.json; stat err=%v", err)
	}
	entries, err := os.ReadDir(filepath.Join(workspace, ".gs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(workspace, ".gs", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(token)) {
			t.Fatalf("workspace metadata %s contains bearer token", entry.Name())
		}
	}
}

func readJSONFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	stdout, stderr, err := runGitResult(dir, args...)
	if err != nil {
		t.Fatalf("git %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

func runGitResult(dir string, args ...string) (string, string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func httpGatewayPost(t *testing.T, addr, path, token string, body any) map[string]any {
	t.Helper()
	statusCode, _, data := httpGatewayPostRaw(t, addr, path, token, body)
	if statusCode >= 300 {
		t.Fatalf("gateway %s returned %d:\n%s", path, statusCode, string(data))
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode gateway response: %v\n%s", err, string(data))
	}
	return out
}

func httpGatewayPostRaw(t *testing.T, addr, path, token string, body any) (int, http.Header, []byte) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header.Clone(), data
}

func httpGatewayOptions(t *testing.T, addr, path, origin string) (int, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodOptions, "http://"+addr+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", "POST")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, resp.Header.Clone()
}

func httpGet(t *testing.T, addr, path string) string {
	t.Helper()
	resp, err := http.Get("http://" + addr + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode >= 300 {
		t.Fatalf("GET %s returned %d:\n%s", path, resp.StatusCode, string(data))
	}
	return string(data)
}

func assertMetricPositive(t *testing.T, text, series string) {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != series {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			t.Fatalf("metric %s has unparsable value %q", series, fields[1])
		}
		if value > 0 {
			return
		}
		t.Fatalf("metric %s = %s, want > 0", series, fields[1])
	}
	t.Fatalf("metric %s not found in /metrics output:\n%s", series, text)
}

func gitHTTPRaw(t *testing.T, addr, path, authorization string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header.Clone(), data
}

func waitForGatewayChangesetStatus(t *testing.T, addr, token, changesetID, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last map[string]any
	for time.Now().Before(deadline) {
		last = httpGatewayPost(t, addr, "/gitslice.core.v1.ChangesetService/GetChangeset", token, map[string]any{
			"changesetId": changesetID,
		})
		if status, _ := last["status"].(string); status == want {
			return last
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("changeset %s did not reach %s status, last=%#v", changesetID, want, last)
	return nil
}

// uniqueSchema builds a Postgres-safe, unique schema name. Postgres truncates
// identifiers to 63 bytes, so the unique token must survive truncation: we trim
// the test-name hint up front to keep the whole name within the limit, leaving
// the token (and its uniqueness) fully intact. The old time-of-day suffix had
// only second resolution and was silently truncated away for long test names,
// causing reruns to collide on a stale schema.
func uniqueSchema(prefix string, t *testing.T) string {
	const maxLen = 63 // Postgres NAMEDATALEN - 1
	token := strconv.FormatInt(time.Now().UnixNano(), 36)
	hint := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_"))
	budget := maxLen - len(prefix) - len(token) - 1 // room for the "_" separator
	if budget < 0 {
		name := prefix + token
		if len(name) > maxLen {
			name = name[:maxLen]
		}
		return name
	}
	if len(hint) > budget {
		hint = hint[:budget]
	}
	return prefix + hint + "_" + token
}

func createSchema(t *testing.T, databaseURL, schema string) {
	t.Helper()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Drop any leftover schema from an interrupted run so create is idempotent.
	if _, err := db.Exec(`drop schema if exists ` + pqIdentifier(schema) + ` cascade`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create schema ` + pqIdentifier(schema)); err != nil {
		t.Fatal(err)
	}
}

func dropSchema(t *testing.T, databaseURL, schema string) {
	t.Helper()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Logf("open db for cleanup: %v", err)
		return
	}
	defer db.Close()
	if _, err := db.Exec(`drop schema if exists ` + pqIdentifier(schema) + ` cascade`); err != nil {
		t.Logf("drop schema %s: %v", schema, err)
	}
}

func pqIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func databaseURLWithSearchPath(t *testing.T, databaseURL, schema string) string {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed.Scheme == "" {
		t.Fatalf("GITSLICE_TEST_DATABASE_URL must be a URL connection string for CLI e2e tests")
	}
	q := parsed.Query()
	q.Set("search_path", schema)
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

func freeAddr(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	return lis.Addr().String()
}

func waitForHealth(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
		if err == nil {
			client := healthv1.NewHealthClient(conn)
			_, err = client.Check(ctx, &healthv1.HealthCheckRequest{})
			_ = conn.Close()
		}
		cancel()
		if err == nil {
			return
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal(fmt.Errorf("server health check did not pass: %w", lastErr))
}

func waitForHTTPGateway(t *testing.T, addr string) {
	t.Helper()
	waitForHTTPServer(t, addr, "server HTTP gateway")
}

func waitForGitHTTP(t *testing.T, addr string) {
	t.Helper()
	waitForHTTPServer(t, addr, "server Git HTTP")
}

func waitForHTTPServer(t *testing.T, addr, name string) {
	t.Helper()
	client := http.Client{Timeout: time.Second}
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://" + addr + "/")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal(fmt.Errorf("%s did not start: %w", name, lastErr))
}
