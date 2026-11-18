package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"gitslice.io/gitslice/internal/storage"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGRPCErrorMapping(t *testing.T) {
	t.Parallel()

	if got := grpcError(nil); got != nil {
		t.Fatalf("grpcError(nil) = %v, want nil", got)
	}

	detailedStatus, err := status.New(codes.FailedPrecondition, "submit blocked").
		WithDetails(&corev1.SliceRef{Account: "acme", Slice: "payment"})
	if err != nil {
		t.Fatalf("status.WithDetails: %v", err)
	}
	existingStatusErr := detailedStatus.Err()

	cases := []struct {
		name        string
		in          error
		wantCode    codes.Code
		wantMessage string
		wantSameErr bool
		wantDetails int
	}{
		{
			name:        "preserves existing grpc status error and details",
			in:          existingStatusErr,
			wantCode:    codes.FailedPrecondition,
			wantMessage: "submit blocked",
			wantSameErr: true,
			wantDetails: 1,
		},
		{
			name:        "preserves wrapped grpc status error",
			in:          fmt.Errorf("read slice: %w", status.Error(codes.Unavailable, "upstream timeout")),
			wantCode:    codes.Unavailable,
			wantMessage: "read slice: rpc error: code = Unavailable desc = upstream timeout",
			wantSameErr: true,
		},
		{
			name:        "context canceled direct",
			in:          context.Canceled,
			wantCode:    codes.Canceled,
			wantMessage: context.Canceled.Error(),
		},
		{
			name:        "context canceled wrapped",
			in:          fmt.Errorf("query commits: %w", context.Canceled),
			wantCode:    codes.Canceled,
			wantMessage: "query commits: context canceled",
		},
		{
			name:        "context deadline exceeded direct",
			in:          context.DeadlineExceeded,
			wantCode:    codes.DeadlineExceeded,
			wantMessage: context.DeadlineExceeded.Error(),
		},
		{
			name:        "context deadline exceeded wrapped",
			in:          fmt.Errorf("fetch blob: %w", context.DeadlineExceeded),
			wantCode:    codes.DeadlineExceeded,
			wantMessage: "fetch blob: context deadline exceeded",
		},
		{
			name:        "storage not found",
			in:          fmt.Errorf("slice acme/app: %w", storage.ErrNotFound),
			wantCode:    codes.NotFound,
			wantMessage: "slice acme/app: not found",
		},
		{
			name:        "storage already exists",
			in:          fmt.Errorf("tag v1.0.0: %w", storage.ErrAlreadyExists),
			wantCode:    codes.AlreadyExists,
			wantMessage: "tag v1.0.0: already exists",
		},
		{
			name:        "storage conflict maps to failed precondition",
			in:          fmt.Errorf("stale base: %w", storage.ErrConflict),
			wantCode:    codes.FailedPrecondition,
			wantMessage: "stale base: conflict",
		},
		{
			name:        "storage invalid",
			in:          fmt.Errorf("bad handle: %w", storage.ErrInvalid),
			wantCode:    codes.InvalidArgument,
			wantMessage: "bad handle: invalid",
		},
		{
			name:        "storage resource exhausted",
			in:          fmt.Errorf("quota exceeded: %w", storage.ErrResourceExhausted),
			wantCode:    codes.ResourceExhausted,
			wantMessage: "quota exceeded: resource exhausted",
		},
		{
			name:        "storage unauthenticated",
			in:          fmt.Errorf("revoked token: %w", storage.ErrUnauthenticated),
			wantCode:    codes.Unauthenticated,
			wantMessage: "revoked token: unauthenticated",
		},
		{
			name:        "storage unauthorized",
			in:          fmt.Errorf("missing writer role: %w", storage.ErrUnauthorized),
			wantCode:    codes.PermissionDenied,
			wantMessage: "missing writer role: unauthorized",
		},
		{
			name:        "unclassified error maps to internal",
			in:          errors.New("unexpected disk failure"),
			wantCode:    codes.Internal,
			wantMessage: "unexpected disk failure",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := grpcError(tc.in)
			if got == nil {
				t.Fatalf("grpcError(%v) = nil, want %v", tc.in, tc.wantCode)
			}
			if tc.wantSameErr && got != tc.in {
				t.Fatalf("grpcError(%v) returned new error %v, want exact original error preserved", tc.in, got)
			}
			st, ok := status.FromError(got)
			if !ok {
				t.Fatalf("status.FromError(%v) = !ok", got)
			}
			if st.Code() != tc.wantCode {
				t.Fatalf("code = %v, want %v (err=%v)", st.Code(), tc.wantCode, got)
			}
			if st.Message() != tc.wantMessage {
				t.Fatalf("message = %q, want %q", st.Message(), tc.wantMessage)
			}
			if len(st.Details()) != tc.wantDetails {
				t.Fatalf("details len = %d, want %d", len(st.Details()), tc.wantDetails)
			}
		})
	}
}

func TestRequireDefaultTargetRef(t *testing.T) {
	t.Parallel()

	if err := requireDefaultTargetRef(""); err != nil {
		t.Fatalf("requireDefaultTargetRef(\"\") = %v, want nil", err)
	}
	if err := requireDefaultTargetRef(storage.DefaultTargetRef); err != nil {
		t.Fatalf("requireDefaultTargetRef(%q) = %v, want nil", storage.DefaultTargetRef, err)
	}
	err := requireDefaultTargetRef("refs/heads/feature")
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("requireDefaultTargetRef(invalid) code = %v, want InvalidArgument", status.Code(err))
	}
}
