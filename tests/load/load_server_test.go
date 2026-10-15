//go:build load

package load_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/auth/servicetoken"
	"gitslice.io/gitslice/internal/cli"
	"gitslice.io/gitslice/internal/rpclimits"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"gitslice.io/gitslice/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
)

type loadServer struct {
	addr        string
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
}

func startLoadServer(t *testing.T) *loadServer {
	t.Helper()
	databaseURL := os.Getenv("GITSLICE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set GITSLICE_TEST_DATABASE_URL to run load tests")
	}
	schema := uniqueSchema("gitslice_load_", t)
	createSchema(t, databaseURL, schema)
	servicePriv, servicePub, err := servicetoken.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ts := &loadServer{
		addr:        freeAddr(t),
		cancel:      cancel,
		errCh:       make(chan error, 1),
		databaseURL: databaseURL,
		schema:      schema,
		objectRoot:  t.TempDir(),
		servicePriv: servicePriv,
		servicePub:  servicePub,
	}
	go func() {
		ts.errCh <- server.Run(ctx, server.Config{
			GRPCAddr:           ts.addr,
			DatabaseURL:        databaseURLWithSearchPath(t, databaseURL, schema),
			ObjectStoreRoot:    ts.objectRoot,
			ObjectCacheBytes:   int64(envInt("GITSLICE_LOAD_OBJECT_CACHE_BYTES", 256<<20)),
			ObjectStoreLatency: time.Duration(envInt("GITSLICE_LOAD_OBJECT_STORE_LATENCY_MS", 0)) * time.Millisecond,
			ServiceToken: servicetoken.Config{
				PublicKeyPEM: ts.servicePub,
				Issuer:       servicetoken.DefaultIssuer,
			},
			RunMigrations: true,
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

type loadCoreClients struct {
	ctx       context.Context
	subjectID string
	repo      corev1.RepositoryServiceClient
	blob      corev1.BlobServiceClient
	changeset corev1.ChangesetServiceClient
}

func dialLoadGRPC(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(rpclimits.MaxUnaryMessageBytes),
			grpc.MaxCallSendMsgSize(rpclimits.MaxUnaryMessageBytes),
		),
		grpc.WithBlock(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func loginLoadCLI(t *testing.T, ts *loadServer, home, workspace string) (string, string) {
	t.Helper()
	_ = workspace
	token, subjectID := ts.defaultAcmeCredentials(t)
	writeLoadCLIAuthConfig(t, home, ts.addr, token, subjectID)
	return token, subjectID
}

func writeLoadCLIAuthConfig(t *testing.T, home, serverAddr, token, subjectID string) {
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

func newLoadCoreClients(t *testing.T, ctx context.Context, ts *loadServer, conn *grpc.ClientConn) loadCoreClients {
	t.Helper()
	token, subjectID := ts.defaultAcmeCredentials(t)
	return loadCoreClients{
		ctx:       metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token),
		subjectID: subjectID,
		repo:      corev1.NewRepositoryServiceClient(conn),
		blob:      corev1.NewBlobServiceClient(conn),
		changeset: corev1.NewChangesetServiceClient(conn),
	}
}

func (ts *loadServer) defaultAcmeCredentials(t *testing.T) (string, string) {
	t.Helper()
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.defaultReady {
		return ts.defaultToken, ts.defaultSubjectID
	}
	ts.clearSeedAcme(t)
	conn := dialLoadGRPC(t, ts.addr)
	defer conn.Close()
	token, account, subjectID := ts.provisionAccount(t, context.Background(), conn, "acme", "acme-owner")
	if account != "acme" {
		t.Fatalf("provisioned account = %q, want acme", account)
	}
	authCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)
	ts.createDefaultAcmeSlices(t, authCtx, conn)
	ts.defaultToken = token
	ts.defaultSubjectID = subjectID
	ts.defaultReady = true
	return token, subjectID
}

func (ts *loadServer) mintToken(t *testing.T, subject, email string) string {
	t.Helper()
	token, err := servicetoken.Mint(ts.servicePriv, subject, email, servicetoken.DefaultIssuer, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func (ts *loadServer) provisionAccount(t *testing.T, ctx context.Context, conn *grpc.ClientConn, username, label string) (string, string, string) {
	t.Helper()
	subject := "svc_" + loadTokenLabel(t, label)
	email := loadTokenLabel(t, label) + "@test.local"
	token := ts.mintToken(t, subject, email)
	authCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	chosen, err := corev1.NewAuthServiceClient(conn).ChooseUsername(authCtx, &corev1.ChooseUsernameRequest{Username: username})
	if err != nil {
		t.Fatal(err)
	}
	if chosen.Account == "" || chosen.SubjectId == "" {
		t.Fatalf("incomplete ChooseUsername response: %#v", chosen)
	}
	return token, chosen.Account, chosen.SubjectId
}

func (ts *loadServer) clearSeedAcme(t *testing.T) {
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

func (ts *loadServer) createDefaultAcmeSlices(t *testing.T, ctx context.Context, conn *grpc.ClientConn) {
	t.Helper()
	clients := loadCoreClients{
		ctx:       ctx,
		repo:      corev1.NewRepositoryServiceClient(conn),
		blob:      corev1.NewBlobServiceClient(conn),
		changeset: corev1.NewChangesetServiceClient(conn),
	}
	if _, _, err := submitLoadEdits(clients, &corev1.SliceRef{Account: "acme", Slice: "home"}, "bootstrap acme test slices", []*corev1.FileEdit{
		{Op: "mkdir", Path: "/acme/payment"},
		{Op: "mkdir", Path: "/acme/payment/shared"},
		{Op: "mkdir", Path: "/acme/backend"},
	}); err != nil {
		t.Fatal(err)
	}
	slices := corev1.NewSliceServiceClient(conn)
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

func loadTokenLabel(t *testing.T, label string) string {
	t.Helper()
	return loadTokenLabelString(t.Name() + "_" + label)
}

func loadTokenLabelString(raw string) string {
	raw = strings.ToLower(raw)
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
