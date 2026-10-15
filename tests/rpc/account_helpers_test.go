package rpc_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"gitslice.io/gitslice/internal/auth/servicetoken"
	"gitslice.io/gitslice/internal/postgres"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

type testCoreClients struct {
	repository corev1.RepositoryServiceClient
	blob       corev1.BlobServiceClient
	changeset  corev1.ChangesetServiceClient
	stack      corev1.ChangesetStackServiceClient
}

func newTestCoreClients(conn *grpc.ClientConn) testCoreClients {
	return testCoreClients{
		repository: corev1.NewRepositoryServiceClient(conn),
		blob:       corev1.NewBlobServiceClient(conn),
		changeset:  corev1.NewChangesetServiceClient(conn),
		stack:      corev1.NewChangesetStackServiceClient(conn),
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

func (ts *testRPCServer) loginViaGRPC(t *testing.T, user string) string {
	t.Helper()
	switch user {
	case "alice", "acme":
		token, _ := ts.defaultAcmeCredentials(t)
		return token
	case "bobby":
		return ts.acmeMemberToken(t, "bobby", "writer")
	default:
		token, _, _ := ts.provisionAccount(t, user, user)
		return token
	}
}

func (ts *testRPCServer) defaultAcmeCredentials(t *testing.T) (string, string) {
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

func (ts *testRPCServer) acmeMemberToken(t *testing.T, username, role string) string {
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

func (ts *testRPCServer) mintToken(t *testing.T, subject, email string) string {
	t.Helper()
	token, err := servicetoken.Mint(ts.servicePriv, subject, email, servicetoken.DefaultIssuer, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func (ts *testRPCServer) provisionAccount(t *testing.T, username, label string) (string, string, string) {
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

func (ts *testRPCServer) clearSeedAcme(t *testing.T) {
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

func (ts *testRPCServer) createDefaultAcmeSlices(t *testing.T, ctx context.Context, conn *grpc.ClientConn) {
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

func (ts *testRPCServer) grantAccountRole(t *testing.T, subjectID, account, role string) {
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
