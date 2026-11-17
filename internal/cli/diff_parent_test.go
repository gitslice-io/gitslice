package cli

import (
	"context"
	"testing"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type parentCommitRepo struct {
	corev1.RepositoryServiceClient
	err error
}

func (r parentCommitRepo) GetCommit(ctx context.Context, in *corev1.GetCommitRequest, _ ...grpc.CallOption) (*corev1.Commit, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &corev1.Commit{Id: in.GetCommitId(), Message: "visible"}, nil
}

// History is shared by every account: a commit's parent can be another
// account's private commit, which the server hides. The diff still runs from
// the parent's id.
func TestParentCommitForDiffToleratesAHiddenParent(t *testing.T) {
	for _, code := range []codes.Code{codes.NotFound, codes.PermissionDenied} {
		got, err := parentCommitForDiff(context.Background(), parentCommitRepo{err: grpcstatus.Error(code, "hidden")}, "sha256:parent")
		if err != nil || got.GetId() != "sha256:parent" {
			t.Fatalf("%v: got %v, %v; want a commit with the parent id", code, got, err)
		}
	}
	got, err := parentCommitForDiff(context.Background(), parentCommitRepo{}, "sha256:parent")
	if err != nil || got.GetMessage() != "visible" {
		t.Fatalf("visible parent: got %v, %v", got, err)
	}
	if _, err := parentCommitForDiff(context.Background(), parentCommitRepo{err: grpcstatus.Error(codes.Unavailable, "down")}, "sha256:parent"); grpcstatus.Code(err) != codes.Unavailable {
		t.Fatalf("unavailable: err = %v, want it passed through", err)
	}
}
