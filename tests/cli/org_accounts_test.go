package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func wantCode(t *testing.T, err error, want codes.Code, what string) {
	t.Helper()
	if status.Code(err) != want {
		t.Fatalf("%s: got %v, want %s", what, err, want)
	}
}

// TestOrganizationAccounts covers operator-created organizations (including
// reserved names) and owner/admin membership management, and checks that
// organization roles drive slice authorization.
func TestOrganizationAccounts(t *testing.T) {
	ts := startTestServer(t)
	operatorToken, _, operatorSubject := ts.provisionAccount(t, "org-operator", "org-operator")
	ts.setOperators(t, operatorSubject)
	writerToken, _, writerSubject := ts.provisionAccount(t, "org-writer", "org-writer")
	readerToken, _, readerSubject := ts.provisionAccount(t, "org-reader", "org-reader")
	outsiderToken, _, _ := ts.provisionAccount(t, "org-outsider", "org-outsider")

	conn := dialTestGRPC(t, ts.addr)
	defer conn.Close()
	auth := corev1.NewAuthServiceClient(conn)

	_, err := auth.CreateOrganization(grpcAuthContext(writerToken), &corev1.CreateOrganizationRequest{Slug: "gitslice"})
	// Anyone may create an organization, but names reserved for sign-up are
	// for operators.
	wantCode(t, err, codes.InvalidArgument, "non-operator CreateOrganization with a reserved name")

	// Operators may claim names reserved for self-service sign-up.
	operatorHome := t.TempDir()
	writeCLIAuthConfig(t, operatorHome, ts.addr, operatorToken, operatorSubject)
	created := runCLI(t, operatorHome, t.TempDir(), "account", "create-org", "gitslice", "--json")
	var org struct {
		Account string `json:"account"`
		Members []struct {
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"members"`
	}
	if err := json.Unmarshal([]byte(created), &org); err != nil {
		t.Fatalf("create-org output %q: %v", created, err)
	}
	if org.Account != "gitslice" || len(org.Members) != 1 || org.Members[0].Username != "org-operator" || org.Members[0].Role != "owner" {
		t.Fatalf("unexpected create-org result: %+v", org)
	}
	_, err = auth.CreateOrganization(grpcAuthContext(operatorToken), &corev1.CreateOrganizationRequest{Slug: "gitslice"})
	wantCode(t, err, codes.AlreadyExists, "duplicate CreateOrganization")
	_, err = auth.CreateOrganization(grpcAuthContext(operatorToken), &corev1.CreateOrganizationRequest{Slug: "org-writer"})
	wantCode(t, err, codes.AlreadyExists, "CreateOrganization over a personal account")

	runCLI(t, operatorHome, t.TempDir(), "account", "set-member", "gitslice", "org-writer", "--role", "writer")
	if _, err := auth.SetAccountMember(grpcAuthContext(operatorToken), &corev1.SetAccountMemberRequest{Account: "gitslice", Username: "org-reader", Role: "reader"}); err != nil {
		t.Fatal(err)
	}
	members, err := auth.ListAccountMembers(grpcAuthContext(readerToken), &corev1.ListAccountMembersRequest{Account: "gitslice"})
	if err != nil {
		t.Fatal(err)
	}
	roles := map[string]string{}
	for _, member := range members.Members {
		roles[member.Username] = member.Role
	}
	if members.Kind != "organization" || roles["org-operator"] != "owner" || roles["org-writer"] != "writer" || roles["org-reader"] != "reader" {
		t.Fatalf("unexpected members: kind=%s %v", members.Kind, roles)
	}
	// A member's auth status names each account with its kind and their role,
	// which is what the web app's account switcher lists.
	status, err := auth.GetAuthStatus(grpcAuthContext(writerToken), &corev1.GetAuthStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, m := range status.Memberships {
		got[m.Account] = m.Kind + "/" + m.Role
	}
	if len(status.Memberships) != len(status.Accounts) || status.Memberships[0].Account != "org-writer" ||
		!strings.HasPrefix(got["org-writer"], "personal/") || got["gitslice"] != "organization/writer" {
		t.Fatalf("writer memberships = %v (accounts %v)", got, status.Accounts)
	}
	_, err = auth.ListAccountMembers(grpcAuthContext(outsiderToken), &corev1.ListAccountMembersRequest{Account: "gitslice"})
	wantCode(t, err, codes.NotFound, "outsider ListAccountMembers")
	_, err = auth.SetAccountMember(grpcAuthContext(writerToken), &corev1.SetAccountMemberRequest{Account: "gitslice", Username: "org-outsider", Role: "reader"})
	wantCode(t, err, codes.PermissionDenied, "writer SetAccountMember")
	_, err = auth.SetAccountMember(grpcAuthContext(operatorToken), &corev1.SetAccountMemberRequest{Account: "org-writer", Username: "org-reader", Role: "reader"})
	wantCode(t, err, codes.FailedPrecondition, "SetAccountMember on a personal account")

	// Organization roles drive slice authorization. The owner seeds a folder
	// and a slice, then a writer submits while a reader cannot.
	ownerWorkspace := t.TempDir()
	runCLI(t, operatorHome, ownerWorkspace, "workspace", "init", "gitslice/home")
	writeWorkspaceFile(t, ownerWorkspace, "gitslice/gitslice/README.md", "# gitslice\n")
	runCLI(t, operatorHome, ownerWorkspace, "cs", "create", "--title", "seed gitslice")
	runCLI(t, operatorHome, ownerWorkspace, "cs", "submit")
	runCLI(t, operatorHome, t.TempDir(), "slice", "create", "gitslice/gitslice", "--include", "/gitslice/gitslice", "--visibility", "public")

	writerHome := t.TempDir()
	writerWorkspace := t.TempDir()
	writeCLIAuthConfig(t, writerHome, ts.addr, writerToken, writerSubject)
	runCLI(t, writerHome, writerWorkspace, "workspace", "init", "gitslice/gitslice")
	writeWorkspaceFile(t, writerWorkspace, "CONTRIBUTING.md", "Use gs.\n")
	runCLI(t, writerHome, writerWorkspace, "cs", "create", "--title", "writer change")
	runCLI(t, writerHome, writerWorkspace, "cs", "submit")

	readerHome := t.TempDir()
	readerWorkspace := t.TempDir()
	writeCLIAuthConfig(t, readerHome, ts.addr, readerToken, readerSubject)
	runCLI(t, readerHome, readerWorkspace, "workspace", "init", "gitslice/gitslice")
	writeWorkspaceFile(t, readerWorkspace, "NOPE.md", "readers cannot write\n")
	if _, err := runCLIResult(readerHome, readerWorkspace, "cs", "create", "--title", "reader change"); err == nil {
		t.Fatal("expected a reader to be unable to create a changeset")
	}

	// Admins manage members but not owners; the last owner stays.
	if _, err := auth.SetAccountMember(grpcAuthContext(operatorToken), &corev1.SetAccountMemberRequest{Account: "gitslice", Username: "org-writer", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	_, err = auth.SetAccountMember(grpcAuthContext(writerToken), &corev1.SetAccountMemberRequest{Account: "gitslice", Username: "org-reader", Role: "owner"})
	wantCode(t, err, codes.PermissionDenied, "admin granting owner")
	if _, err := auth.SetAccountMember(grpcAuthContext(writerToken), &corev1.SetAccountMemberRequest{Account: "gitslice", Username: "org-reader", Role: "member"}); err != nil {
		t.Fatalf("admin changing a non-owner role: %v", err)
	}
	_, err = auth.RemoveAccountMember(grpcAuthContext(writerToken), &corev1.RemoveAccountMemberRequest{Account: "gitslice", Username: "org-operator"})
	wantCode(t, err, codes.PermissionDenied, "admin removing an owner")
	_, err = auth.RemoveAccountMember(grpcAuthContext(operatorToken), &corev1.RemoveAccountMemberRequest{Account: "gitslice", Username: "org-operator"})
	wantCode(t, err, codes.FailedPrecondition, "removing the last owner")
	_, err = auth.SetAccountMember(grpcAuthContext(operatorToken), &corev1.SetAccountMemberRequest{Account: "gitslice", Username: "org-operator", Role: "admin"})
	wantCode(t, err, codes.FailedPrecondition, "demoting the last owner")
	if _, err := auth.RemoveAccountMember(grpcAuthContext(writerToken), &corev1.RemoveAccountMemberRequest{Account: "gitslice", Username: "org-reader"}); err != nil {
		t.Fatalf("admin removing a member: %v", err)
	}
	out := runCLI(t, operatorHome, t.TempDir(), "account", "members", "gitslice")
	if strings.Contains(out, "org-reader") || !strings.Contains(out, "org-writer\tadmin") {
		t.Fatalf("unexpected member list after changes:\n%s", out)
	}
}
