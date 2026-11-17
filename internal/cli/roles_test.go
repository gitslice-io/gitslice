package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

type rolesFakeServer struct {
	corev1.UnimplementedAuthServiceServer
	corev1.UnimplementedSliceServiceServer

	authStatus *corev1.GetAuthStatusResponse
	slice      *corev1.Slice
	shared     []*corev1.SharedSlice

	lastSliceMemberReq  *corev1.SetSliceMemberRequest
	lastAccountRoleReq  *corev1.SetAccountMemberRequest
	lastAccountInviteRq *corev1.InviteAccountMemberRequest
}

func (s *rolesFakeServer) GetAuthStatus(ctx context.Context, req *corev1.GetAuthStatusRequest) (*corev1.GetAuthStatusResponse, error) {
	if s.authStatus != nil {
		return s.authStatus, nil
	}
	return &corev1.GetAuthStatusResponse{
		SubjectId: "user_alice",
		Accounts:  []string{"alice"},
		Memberships: []*corev1.AccountMembership{
			{Account: "alice", Kind: "personal", Role: "owner"},
		},
	}, nil
}

func (s *rolesFakeServer) ResolveSlice(ctx context.Context, req *corev1.ResolveSliceRequest) (*corev1.Slice, error) {
	if s.slice != nil {
		return s.slice, nil
	}
	return nil, status.Error(codes.NotFound, "not found")
}

func (s *rolesFakeServer) ListSharedSlices(ctx context.Context, req *corev1.ListSharedSlicesRequest) (*corev1.ListSharedSlicesResponse, error) {
	return &corev1.ListSharedSlicesResponse{Slices: s.shared}, nil
}

func (s *rolesFakeServer) SetSliceMember(ctx context.Context, req *corev1.SetSliceMemberRequest) (*corev1.SliceMember, error) {
	s.lastSliceMemberReq = req
	return &corev1.SliceMember{
		Username: req.GetUsername(),
		Role:     req.GetRole(),
	}, nil
}

func (s *rolesFakeServer) SetAccountMember(ctx context.Context, req *corev1.SetAccountMemberRequest) (*corev1.SetAccountMemberResponse, error) {
	s.lastAccountRoleReq = req
	return &corev1.SetAccountMemberResponse{
		Member: &corev1.AccountMember{
			Username: req.GetUsername(),
			Role:     req.GetRole(),
		},
	}, nil
}

func (s *rolesFakeServer) InviteAccountMember(ctx context.Context, req *corev1.InviteAccountMemberRequest) (*corev1.InviteAccountMemberResponse, error) {
	s.lastAccountInviteRq = req
	return &corev1.InviteAccountMemberResponse{
		Invitation: &corev1.AccountInvitation{
			Account:  req.GetAccount(),
			Username: req.GetUsername(),
			Role:     req.GetRole(),
		},
	}, nil
}

func startRolesFakeServer(t *testing.T, f *rolesFakeServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	hs := health.NewServer()
	hs.SetServingStatus("", healthv1.HealthCheckResponse_SERVING)
	healthv1.RegisterHealthServer(srv, hs)
	corev1.RegisterAuthServiceServer(srv, f)
	corev1.RegisterSliceServiceServer(srv, f)
	go func() {
		_ = srv.Serve(lis)
	}()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

func newRolesTestRunner(t *testing.T, addr string) (Runner, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	r := Runner{
		Home:   t.TempDir(),
		Dir:    t.TempDir(),
		Stdout: &stdout,
		Stderr: &stderr,
	}
	if addr != "" {
		if err := r.writeUserConfig(UserConfig{
			ServerAddr: addr,
			Token:      "clitok_test_token",
			SubjectID:  "user_alice",
		}); err != nil {
			t.Fatalf("writeUserConfig: %v", err)
		}
	}
	return r, &stdout, &stderr
}

func assertInvalidRoleError(t *testing.T, err error, context string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected invalid_role error, got nil", context)
	}
	var cmdErr commandError
	if !errors.As(err, &cmdErr) || cmdErr.Code != "invalid_role" {
		t.Fatalf("%s: err = %v (%T), want commandError{Code: \"invalid_role\"}", context, err, err)
	}
}

func TestSliceShareRoleValidationAndContributor(t *testing.T) {
	srv := &rolesFakeServer{}
	addr := startRolesFakeServer(t, srv)

	r, stdout, _ := newRolesTestRunner(t, addr)
	if err := r.Run(context.Background(), []string{"slice", "share", "acme/project", "bob", "--role", "contributor"}); err != nil {
		t.Fatalf("gs slice share --role contributor: %v", err)
	}
	if srv.lastSliceMemberReq == nil || srv.lastSliceMemberReq.GetRole() != "contributor" || srv.lastSliceMemberReq.GetUsername() != "bob" {
		t.Fatalf("unexpected SetSliceMemberRequest: %#v", srv.lastSliceMemberReq)
	}
	if !strings.Contains(stdout.String(), "as contributor") {
		t.Fatalf("stdout = %q, want \"as contributor\"", stdout.String())
	}

	// Maintainer role was removed and must be rejected client-side before dialing RPC.
	srv.lastSliceMemberReq = nil
	err := r.Run(context.Background(), []string{"slice", "share", "acme/project", "bob", "--role", "maintainer"})
	assertInvalidRoleError(t, err, "gs slice share --role maintainer")
	if srv.lastSliceMemberReq != nil {
		t.Fatalf("maintainer should fail client-side validation without calling RPC, got %#v", srv.lastSliceMemberReq)
	}
}

func TestAccountRoleValidationAndContributor(t *testing.T) {
	srv := &rolesFakeServer{}
	addr := startRolesFakeServer(t, srv)

	r, stdout, _ := newRolesTestRunner(t, addr)
	if err := r.Run(context.Background(), []string{"account", "set-member", "acme", "bob", "--role", "contributor"}); err != nil {
		t.Fatalf("gs account set-member --role contributor: %v", err)
	}
	if srv.lastAccountRoleReq == nil || srv.lastAccountRoleReq.GetRole() != "contributor" {
		t.Fatalf("unexpected SetAccountMemberRequest: %#v", srv.lastAccountRoleReq)
	}
	if !strings.Contains(stdout.String(), "contributor") {
		t.Fatalf("stdout = %q, want contributor", stdout.String())
	}

	srv.lastAccountRoleReq = nil
	err := r.Run(context.Background(), []string{"account", "set-member", "acme", "bob", "--role", "maintainer"})
	assertInvalidRoleError(t, err, "gs account set-member --role maintainer")
	if srv.lastAccountRoleReq != nil {
		t.Fatalf("set-member maintainer should fail validation before RPC")
	}

	stdout.Reset()
	if err := r.Run(context.Background(), []string{"account", "invite", "acme", "carol", "--role", "contributor"}); err != nil {
		t.Fatalf("gs account invite --role contributor: %v", err)
	}
	if srv.lastAccountInviteRq == nil || srv.lastAccountInviteRq.GetRole() != "contributor" {
		t.Fatalf("unexpected InviteAccountMemberRequest: %#v", srv.lastAccountInviteRq)
	}
	if !strings.Contains(stdout.String(), "contributor") {
		t.Fatalf("stdout = %q, want contributor", stdout.String())
	}

	srv.lastAccountInviteRq = nil
	err = r.Run(context.Background(), []string{"account", "invite", "acme", "carol", "--role", "maintainer"})
	assertInvalidRoleError(t, err, "gs account invite --role maintainer")
	if srv.lastAccountInviteRq != nil {
		t.Fatalf("invite maintainer should fail validation before RPC")
	}
}

func TestSchemaAndHelpListContributor(t *testing.T) {
	r, stdout, _ := newRolesTestRunner(t, "")
	if err := r.Run(context.Background(), []string{"schema", "--json"}); err != nil {
		t.Fatalf("gs schema --json: %v", err)
	}
	if !strings.Contains(stdout.String(), "contributor") {
		t.Fatalf("gs schema --json did not mention contributor: %s", stdout.String())
	}

	for _, args := range [][]string{
		{"slice", "share", "--help"},
		{"account", "set-member", "--help"},
		{"account", "invite", "--help"},
	} {
		stdout.Reset()
		if err := r.Run(context.Background(), args); err != nil {
			t.Fatalf("gs %v: %v", args, err)
		}
		if !strings.Contains(stdout.String(), "contributor") {
			t.Fatalf("gs %v help did not mention contributor:\n%s", args, stdout.String())
		}
	}
}

func TestDoctorContributorRoleAccountAndSharedSlice(t *testing.T) {
	projectSlice := &corev1.Slice{
		Id:  "slice_acme_project",
		Ref: &corev1.SliceRef{Account: "acme", Slice: "project"},
	}

	t.Run("account contributor", func(t *testing.T) {
		srv := &rolesFakeServer{
			authStatus: &corev1.GetAuthStatusResponse{
				SubjectId: "user_bob",
				Accounts:  []string{"bob", "acme"},
				Memberships: []*corev1.AccountMembership{
					{Account: "bob", Kind: "personal", Role: "owner"},
					{Account: "acme", Kind: "organization", Role: "contributor"},
				},
			},
			slice: projectSlice,
		}
		addr := startRolesFakeServer(t, srv)
		r, stdout, _ := newRolesTestRunner(t, addr)
		if err := r.Run(context.Background(), []string{"doctor", "acme/project", "--json"}); err != nil {
			t.Fatalf("gs doctor acme/project: %v", err)
		}
		var out doctorOutput
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			t.Fatalf("unmarshal doctor output: %v\n%s", err, stdout.String())
		}
		var sliceCheck *doctorCheck
		for i := range out.Checks {
			if out.Checks[i].Name == "slice" {
				sliceCheck = &out.Checks[i]
				break
			}
		}
		if sliceCheck == nil {
			t.Fatalf("missing slice check in doctor output: %+v", out.Checks)
		}
		if sliceCheck.Status != "ok" {
			t.Fatalf("slice check status = %q, want ok (summary=%q)", sliceCheck.Status, sliceCheck.Summary)
		}
		if !strings.Contains(sliceCheck.Summary, "(can propose changesets; writer needed to submit)") {
			t.Fatalf("slice check summary = %q, want contributor capability note", sliceCheck.Summary)
		}
	})

	t.Run("shared slice contributor", func(t *testing.T) {
		srv := &rolesFakeServer{
			authStatus: &corev1.GetAuthStatusResponse{
				SubjectId: "user_bob",
				Accounts:  []string{"bob"},
				Memberships: []*corev1.AccountMembership{
					{Account: "bob", Kind: "personal", Role: "owner"},
				},
			},
			slice: projectSlice,
			shared: []*corev1.SharedSlice{
				{Slice: projectSlice, Role: "contributor"},
			},
		}
		addr := startRolesFakeServer(t, srv)
		r, stdout, _ := newRolesTestRunner(t, addr)
		if err := r.Run(context.Background(), []string{"doctor", "acme/project", "--json"}); err != nil {
			t.Fatalf("gs doctor acme/project: %v", err)
		}
		var out doctorOutput
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			t.Fatalf("unmarshal doctor output: %v\n%s", err, stdout.String())
		}
		var sliceCheck *doctorCheck
		for i := range out.Checks {
			if out.Checks[i].Name == "slice" {
				sliceCheck = &out.Checks[i]
				break
			}
		}
		if sliceCheck == nil || sliceCheck.Status != "ok" || !strings.Contains(sliceCheck.Summary, "(can propose changesets; writer needed to submit)") {
			t.Fatalf("unexpected shared contributor slice check: %+v", sliceCheck)
		}
	})

	t.Run("shared slice reader remains warn", func(t *testing.T) {
		srv := &rolesFakeServer{
			authStatus: &corev1.GetAuthStatusResponse{
				SubjectId: "user_carol",
				Accounts:  []string{"carol"},
				Memberships: []*corev1.AccountMembership{
					{Account: "carol", Kind: "personal", Role: "owner"},
				},
			},
			slice: projectSlice,
			shared: []*corev1.SharedSlice{
				{Slice: projectSlice, Role: "reader"},
			},
		}
		addr := startRolesFakeServer(t, srv)
		r, stdout, _ := newRolesTestRunner(t, addr)
		if err := r.Run(context.Background(), []string{"doctor", "acme/project", "--json"}); err != nil {
			t.Fatalf("gs doctor acme/project: %v", err)
		}
		var out doctorOutput
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			t.Fatalf("unmarshal doctor output: %v\n%s", err, stdout.String())
		}
		var sliceCheck *doctorCheck
		for i := range out.Checks {
			if out.Checks[i].Name == "slice" {
				sliceCheck = &out.Checks[i]
				break
			}
		}
		if sliceCheck == nil || sliceCheck.Status != "warn" || !strings.Contains(sliceCheck.Summary, "read it but not change it") {
			t.Fatalf("unexpected reader slice check: %+v", sliceCheck)
		}
	})
}
