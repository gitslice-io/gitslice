package service

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/gitslice-io/gitslice/internal/authctx"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRegisterAgentDisabledByDefault(t *testing.T) {
	_, handlers := newMemoryHandlers()
	_, err := handlers.Auth.RegisterAgent(context.Background(), &corev1.RegisterAgentRequest{
		Username:   "release-bot",
		OwnerEmail: "owner@example.com",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("RegisterAgent code = %v; want FailedPrecondition", status.Code(err))
	}
}

func TestRegisterAgentIssuesUsableKey(t *testing.T) {
	mem, handlers := newMemoryHandlers()
	handlers.Auth.AgentSignupEnabled = true
	ctx := context.Background()

	resp, err := handlers.Auth.RegisterAgent(ctx, &corev1.RegisterAgentRequest{
		Username:   "release-bot",
		OwnerEmail: "owner@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Account != "release-bot" || !strings.HasPrefix(resp.ApiKey, "gsk_") {
		t.Fatalf("RegisterAgent = %+v", resp)
	}

	// Resolve the key the way the server's auth interceptor does.
	subject, err := mem.Auth.SubjectForToken(ctx, resp.ApiKey)
	if err != nil {
		t.Fatal(err)
	}
	authStatus, err := handlers.Auth.GetAuthStatus(authctx.WithSubjectID(ctx, subject.ID), &corev1.GetAuthStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if authStatus.SubjectId != resp.SubjectId || !reflect.DeepEqual(authStatus.Accounts, []string{"release-bot"}) || authStatus.NeedsUsername {
		t.Fatalf("GetAuthStatus = %+v", authStatus)
	}

	if _, err := handlers.Auth.RegisterAgent(ctx, &corev1.RegisterAgentRequest{Username: "release-bot", OwnerEmail: "owner@example.com"}); err == nil {
		t.Fatal("duplicate username registered")
	}
	if _, err := handlers.Auth.RegisterAgent(ctx, &corev1.RegisterAgentRequest{Username: "other-bot", OwnerEmail: "nope"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad email code = %v; want InvalidArgument", status.Code(err))
	}
}
