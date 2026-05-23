//go:build load

package load_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

func TestLoadConcurrentDisjointSubmit(t *testing.T) {
	ts := startLoadServer(t)
	workers := envInt("GITSLICE_LOAD_WORKERS", 16)
	home := t.TempDir()
	firstWorkspace := t.TempDir()
	runCLI(t, home, firstWorkspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")

	var wg sync.WaitGroup
	results := make(chan time.Duration, workers)
	errs := make(chan error, workers)
	start := time.Now()
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			workspace := t.TempDir()
			begin := time.Now()
			if _, err := runCLIResult(home, workspace, "workspace", "init", "acme/payment"); err != nil {
				errs <- err
				return
			}
			writeWorkspaceFile(t, workspace, fmt.Sprintf("load_%03d.go", i), fmt.Sprintf("package payment\nconst Load%d = %d\n", i, i))
			if _, err := runCLIResult(home, workspace, "cs", "create", "--title", fmt.Sprintf("load %03d", i)); err != nil {
				errs <- err
				return
			}
			if _, err := runCLIResult(home, workspace, "cs", "submit"); err != nil {
				errs <- err
				return
			}
			results <- time.Since(begin)
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	durations := drainDurations(results)
	reportDurations(t, "concurrent_disjoint_submit", workers, time.Since(start), durations)
}

func TestLoadSamePathSubmitContention(t *testing.T) {
	ts := startLoadServer(t)
	workers := envInt("GITSLICE_LOAD_WORKERS", 16)
	home := t.TempDir()
	firstWorkspace := t.TempDir()
	runCLI(t, home, firstWorkspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")

	workspaces := make([]string, workers)
	for i := range workers {
		workspace := t.TempDir()
		workspaces[i] = workspace
		runCLI(t, home, workspace, "workspace", "init", "acme/payment")
		writeWorkspaceFile(t, workspace, "hotspot.go", fmt.Sprintf("package payment\nconst Hotspot = %d\n", i))
		runCLI(t, home, workspace, "cs", "create", "--title", fmt.Sprintf("hotspot %03d", i))
	}

	var wg sync.WaitGroup
	results := make(chan time.Duration, workers)
	errs := make(chan error, workers)
	start := time.Now()
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			begin := time.Now()
			_, err := runCLIResult(home, workspaces[i], "cs", "submit")
			results <- time.Since(begin)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)

	successes := 0
	conflicts := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		if strings.Contains(err.Error(), "FailedPrecondition") || strings.Contains(err.Error(), "conflict") {
			conflicts++
			continue
		}
		t.Fatalf("unexpected contention error: %v", err)
	}
	if successes != 1 || conflicts != workers-1 {
		t.Fatalf("expected one success and %d conflicts, got successes=%d conflicts=%d", workers-1, successes, conflicts)
	}
	durations := drainDurations(results)
	reportDurations(t, "same_path_submit_contention", workers, time.Since(start), durations)
}

func TestLoadRepeatedStatus(t *testing.T) {
	ts := startLoadServer(t)
	workers := envInt("GITSLICE_LOAD_WORKERS", 16)
	iterations := envInt("GITSLICE_LOAD_STATUS_ITERATIONS", 8)
	home := t.TempDir()
	workspace := t.TempDir()
	runCLI(t, home, workspace, "auth", "login", "--server", ts.addr, "--dev-user", "alice")
	runCLI(t, home, workspace, "workspace", "init", "acme/payment")
	for i := range 20 {
		writeWorkspaceFile(t, workspace, fmt.Sprintf("status_%03d.go", i), fmt.Sprintf("package payment\nconst Status%d = %d\n", i, i))
	}

	total := workers * iterations
	var wg sync.WaitGroup
	results := make(chan time.Duration, total)
	errs := make(chan error, total)
	start := time.Now()
	for i := range workers {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := range iterations {
				begin := time.Now()
				if _, err := runCLIResult(home, workspace, "status", "--format", "json"); err != nil {
					errs <- fmt.Errorf("worker %d iteration %d: %w", worker, j, err)
					return
				}
				results <- time.Since(begin)
			}
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	durations := drainDurations(results)
	reportDurations(t, "repeated_status", total, time.Since(start), durations)
}

type loadServer struct {
	addr        string
	cancel      context.CancelFunc
	errCh       chan error
	databaseURL string
	schema      string
}

func startLoadServer(t *testing.T) *loadServer {
	t.Helper()
	databaseURL := os.Getenv("GITSLICE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set GITSLICE_TEST_DATABASE_URL to run load tests")
	}
	schema := "gitslice_load_" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_")) + "_" + time.Now().Format("150405000000")
	createSchema(t, databaseURL, schema)
	ctx, cancel := context.WithCancel(context.Background())
	ts := &loadServer{
		addr:        freeAddr(t),
		cancel:      cancel,
		errCh:       make(chan error, 1),
		databaseURL: databaseURL,
		schema:      schema,
	}
	go func() {
		ts.errCh <- server.Run(ctx, server.Config{
			GRPCAddr:        ts.addr,
			DatabaseURL:     databaseURLWithSearchPath(t, databaseURL, schema),
			ObjectStoreRoot: t.TempDir(),
			RunMigrations:   true,
		})
	}()
	waitForHealth(t, ts.addr)
	t.Cleanup(func() {
		ts.cancel()
		err := <-ts.errCh
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("server exited with error: %v", err)
		}
		dropSchema(t, databaseURL, schema)
	})
	return ts
}

func runCLI(t *testing.T, home, workspace string, args ...string) string {
	t.Helper()
	out, err := runCLIResult(home, workspace, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
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

func drainDurations(ch <-chan time.Duration) []time.Duration {
	var out []time.Duration
	for d := range ch {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func reportDurations(t *testing.T, name string, operations int, wall time.Duration, durations []time.Duration) {
	t.Helper()
	if len(durations) == 0 {
		t.Fatalf("%s recorded no durations", name)
	}
	t.Logf("%s operations=%d wall=%s throughput=%.2f/s p50=%s p95=%s p99=%s",
		name,
		operations,
		wall,
		float64(operations)/wall.Seconds(),
		percentile(durations, 0.50),
		percentile(durations, 0.95),
		percentile(durations, 0.99),
	)
}

func percentile(durations []time.Duration, p float64) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	idx := int(float64(len(durations)-1) * p)
	return durations[idx]
}

func envInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
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
		t.Fatalf("GITSLICE_TEST_DATABASE_URL must be a URL connection string")
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
