package cli_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/auth/servicetoken"
	"gitslice.io/gitslice/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
)

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
	operatorSubjects []string
	agentSignup      bool
}

func startTestServer(t *testing.T) *testServer {
	t.Helper()
	databaseURL := os.Getenv("GITSLICE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set GITSLICE_TEST_DATABASE_URL to run real Postgres CLI e2e tests")
	}
	// Import e2e tests clone from local temp repos, which the production import
	// allowlist rejects; opt in for the in-process test server only.
	t.Setenv("GITSLICE_IMPORT_ALLOW_LOCAL", "1")
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
	for attempt := 1; ; attempt++ {
		err := ts.launch(t, migrate)
		if err == nil {
			break
		}
		ts.cancel()
		ts.cancel = nil
		if attempt < 3 && retryableStartupError(err) {
			t.Logf("test server lost a port, retrying on fresh ones: %v", err)
			ts.addr, ts.httpAddr, ts.gitAddr = freeAddr(t), freeAddr(t), freeAddr(t)
			continue
		}
		t.Fatal(err)
	}
	waitForHTTPGateway(t, ts.httpAddr)
	waitForGitHTTP(t, ts.gitAddr)
}

func (ts *testServer) launch(t *testing.T, migrate bool) error {
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
			RunMigrations:      migrate,
			OperatorSubjects:   ts.operatorSubjects,
			AgentSignupEnabled: ts.agentSignup,
			AgentSignupPerHour: 100,
			// Webhook tests deliver to a local receiver.
			WebhookAllowPrivateTargets: true,
		})
	}()
	return waitForHealth(ts.addr, ts.errCh)
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

// setOperators restarts the server with the given operator subjects (allowed
// to create organizations), keeping the database.
func (ts *testServer) setOperators(t *testing.T, subjects ...string) {
	t.Helper()
	ts.operatorSubjects = subjects
	ts.restart(t)
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

func freeAddr(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	return lis.Addr().String()
}

// serverExitError reports that the in-process server returned before its
// health check passed, carrying the server's own error.
type serverExitError struct{ err error }

func (e *serverExitError) Error() string {
	return fmt.Sprintf("server exited during startup: %v", e.err)
}

func (e *serverExitError) Unwrap() error { return e.err }

// waitForHealth waits for the gRPC health check to pass. It also watches the
// server goroutine: if server.Run returns first (e.g. its port was taken
// between freeAddr and Listen), the real error is returned at once instead of
// a generic health-check timeout 10s later.
func waitForHealth(addr string, errCh <-chan error) error {
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			return &serverExitError{err: err}
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
		if err == nil {
			client := healthv1.NewHealthClient(conn)
			_, err = client.Check(ctx, &healthv1.HealthCheckRequest{})
			_ = conn.Close()
		}
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("server health check did not pass: %w", lastErr)
}

// retryableStartupError reports whether a startup failure was a lost port,
// which a retry on fresh ports fixes.
func retryableStartupError(err error) bool {
	var exitErr *serverExitError
	return errors.As(err, &exitErr) && errors.Is(exitErr.err, syscall.EADDRINUSE)
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
