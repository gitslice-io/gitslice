package functional_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/gitslice-io/gitslice/internal/cli"
	"github.com/gitslice-io/gitslice/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
)

func TestMinimalCLIJourney(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	runCLI(t, home, workspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	status := runCLI(t, home, workspace, "status")
	if !strings.Contains(status, "status: clean") {
		t.Fatalf("expected clean status, got:\n%s", status)
	}
	writeWorkspaceFile(t, workspace, "app.go", "package payment\n")
	status = runCLI(t, home, workspace, "status")
	if !strings.Contains(status, "/acme/payment/app.go") {
		t.Fatalf("expected app.go to be dirty, got:\n%s", status)
	}
	runCLI(t, home, workspace, "cs", "create")
	runCLI(t, home, workspace, "cs", "submit")
	csStatus := runCLI(t, home, workspace, "cs", "status")
	if !strings.Contains(csStatus, "status: submitted") {
		t.Fatalf("expected submitted changeset, got:\n%s", csStatus)
	}
	status = runCLI(t, home, workspace, "status")
	if !strings.Contains(status, "status: clean") {
		t.Fatalf("expected clean status after submit, got:\n%s", status)
	}
}

func TestChangesetUpdateAndDelete(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	runCLI(t, home, workspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
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

func TestOutsideSliceEditRejected(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	runCLI(t, home, workspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "acme/backend/app.go", "package backend\n")
	_, stderr := runCLIFails(t, home, workspace, "status")
	if !strings.Contains(stderr, "outside slice") {
		t.Fatalf("expected outside-slice rejection, got stderr:\n%s", stderr)
	}
}

func TestDisjointStaleChangesetsCanSubmit(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	runCLI(t, home, workspaceA, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
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
	runCLI(t, home, workspaceA, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
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

func TestStaleDisjointUpdatePreservesFinalState(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	seedWorkspace := t.TempDir()
	runCLI(t, home, seedWorkspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
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
			runCLI(t, home, seedWorkspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
			runCLI(t, home, seedWorkspace, "workspace", "init", "acme/payment")
			writeWorkspaceFile(t, seedWorkspace, "du.go", "package payment\nconst Value = 1\n")
			runCLI(t, home, seedWorkspace, "cs", "create", "--title", "seed du")
			runCLI(t, home, seedWorkspace, "cs", "submit")

			deleteWorkspace := t.TempDir()
			updateWorkspace := t.TempDir()
			runCLI(t, home, deleteWorkspace, "workspace", "init", "acme/payment")
			runCLI(t, home, updateWorkspace, "workspace", "init", "acme/payment")
			copyWorkspaceFile(t, seedWorkspace, deleteWorkspace, "du.go")
			copyWorkspaceFile(t, seedWorkspace, updateWorkspace, "du.go")
			copyWorkspaceFile(t, seedWorkspace, deleteWorkspace, ".gs/base_snapshot.json")
			copyWorkspaceFile(t, seedWorkspace, updateWorkspace, ".gs/base_snapshot.json")
			if err := os.Remove(filepath.Join(deleteWorkspace, "du.go")); err != nil {
				t.Fatal(err)
			}
			writeWorkspaceFile(t, updateWorkspace, "du.go", "package payment\nconst Value = 2\n")
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
	runCLI(t, home, firstWorkspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")

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
	runCLI(t, home, firstWorkspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
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
	runCLI(t, home, workspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
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

func TestGitCloneProjection(t *testing.T) {
	ts := startTestServer(t)
	home := t.TempDir()
	workspace := t.TempDir()
	runCLI(t, home, workspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
	token := readToken(t, home)
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	writeWorkspaceFile(t, workspace, "git_layer.go", "package payment\nconst GitLayer = true\n")
	runCLI(t, home, workspace, "cs", "create", "--title", "git projection")
	runCLI(t, home, workspace, "cs", "submit")

	cloneDir := filepath.Join(t.TempDir(), "payment")
	gitURL := "http://" + ts.gitAddr + "/git/acme/payment.git"
	runGit(t, "", "-c", "http.extraHeader=Authorization: Bearer "+token, "clone", gitURL, cloneDir)
	projected, err := os.ReadFile(filepath.Join(cloneDir, "acme", "payment", "git_layer.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(projected) != "package payment\nconst GitLayer = true\n" {
		t.Fatalf("unexpected projected file contents:\n%s", string(projected))
	}
	_, stderr, err := runGitResult("", "-C", cloneDir, "-c", "http.extraHeader=Authorization: Bearer "+token, "push", "origin", "HEAD:refs/changes/new")
	if err == nil {
		t.Fatal("expected git push to be rejected")
	}
	if !strings.Contains(stderr, "403") && !strings.Contains(stderr, "not supported") {
		t.Fatalf("expected push rejection, got stderr:\n%s", stderr)
	}
}

type testServer struct {
	addr        string
	gitAddr     string
	ctx         context.Context
	cancel      context.CancelFunc
	errCh       chan error
	databaseURL string
	schema      string
	objectRoot  string
}

func startTestServer(t *testing.T) *testServer {
	t.Helper()
	databaseURL := os.Getenv("GITSLICE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set GITSLICE_TEST_DATABASE_URL to run the real Postgres functional smoke test")
	}
	schema := "gitslice_test_" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_")) + "_" + time.Now().Format("150405000000")
	createSchema(t, databaseURL, schema)
	ts := &testServer{
		addr:        freeAddr(t),
		gitAddr:     freeAddr(t),
		databaseURL: databaseURL,
		schema:      schema,
		objectRoot:  t.TempDir(),
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
			GRPCAddr:        ts.addr,
			GitHTTPAddr:     ts.gitAddr,
			GitCacheRoot:    filepath.Join(ts.objectRoot, "git-cache"),
			DatabaseURL:     databaseURLWithSearchPath(t, ts.databaseURL, ts.schema),
			ObjectStoreRoot: ts.objectRoot,
			RunMigrations:   migrate,
		})
	}()
	waitForHealth(t, ts.addr)
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

func runCLI(t *testing.T, home, workspace string, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	r := cli.Runner{Home: home, Dir: workspace, Stdout: &stdout, Stderr: &stderr}
	if err := r.Run(context.Background(), args); err != nil {
		t.Fatalf("gs %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
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

func writeWorkspaceFile(t *testing.T, workspace, rel, content string) {
	t.Helper()
	path := filepath.Join(workspace, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func copyWorkspaceFile(t *testing.T, fromWorkspace, toWorkspace, rel string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fromWorkspace, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(toWorkspace, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		t.Fatal(err)
	}
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

func createSchema(t *testing.T, databaseURL, schema string) {
	t.Helper()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
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
		t.Fatalf("GITSLICE_TEST_DATABASE_URL must be a URL connection string for functional tests")
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
