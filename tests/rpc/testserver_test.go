package rpc_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/auth/servicetoken"
	"gitslice.io/gitslice/internal/objectstore/filesystem"
	"gitslice.io/gitslice/internal/postgres"
	"gitslice.io/gitslice/internal/treestore"
	"gitslice.io/gitslice/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
)

type testRPCServer struct {
	addr        string
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

func startRPCServer(t *testing.T) *testRPCServer {
	t.Helper()
	databaseURL := os.Getenv("GITSLICE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set GITSLICE_TEST_DATABASE_URL to run real Postgres RPC e2e tests")
	}
	// Import e2e tests clone from local temp repos, which the production import
	// allowlist rejects; opt in for the in-process test server only.
	t.Setenv("GITSLICE_IMPORT_ALLOW_LOCAL", "1")
	schema := uniqueSchema("gitslice_rpc_", t)
	createSchema(t, databaseURL, schema)
	servicePriv, servicePub, err := servicetoken.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	ts := &testRPCServer{
		addr:         freeAddr(t),
		databaseURL:  databaseURL,
		schema:       schema,
		objectRoot:   t.TempDir(),
		servicePriv:  servicePriv,
		servicePub:   servicePub,
		memberTokens: map[string]string{},
	}
	ts.start(t)
	t.Cleanup(func() {
		ts.stop(t)
		dropSchema(t, databaseURL, schema)
	})
	return ts
}

func (ts *testRPCServer) start(t *testing.T) {
	t.Helper()
	for attempt := 1; ; attempt++ {
		err := ts.launch(t)
		if err == nil {
			return
		}
		ts.cancel()
		ts.cancel = nil
		if attempt < 3 && retryableStartupError(err) {
			t.Logf("test server lost its port, retrying on a fresh one: %v", err)
			ts.addr = freeAddr(t)
			continue
		}
		t.Fatal(err)
	}
}

func (ts *testRPCServer) launch(t *testing.T) error {
	t.Helper()
	ts.ctx, ts.cancel = context.WithCancel(context.Background())
	ts.errCh = make(chan error, 1)
	go func() {
		ts.errCh <- server.Run(ts.ctx, server.Config{
			GRPCAddr:        ts.addr,
			DatabaseURL:     databaseURLWithSearchPath(t, ts.databaseURL, ts.schema),
			ObjectStoreRoot: ts.objectRoot,
			ServiceToken: servicetoken.Config{
				PublicKeyPEM: ts.servicePub,
				Issuer:       servicetoken.DefaultIssuer,
			},
			RunMigrations:      true,
			AgentSignupEnabled: true,
		})
	}()
	return waitForHealth(ts.addr, ts.errCh)
}

func (ts *testRPCServer) stop(t *testing.T) {
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

func (ts *testRPCServer) waitForOutboxDrain(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, databaseURLWithSearchPath(t, ts.databaseURL, ts.schema))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	objectStore, err := filesystem.New(ts.objectRoot)
	if err != nil {
		t.Fatal(err)
	}
	db.SetTreeStore(treestore.New(objectStore))
	if err := db.Changesets().WaitForOutboxDrain(ctx); err != nil {
		t.Fatal(err)
	}
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
