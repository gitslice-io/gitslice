package mcpserver

import (
	"context"

	corev1 "gitslice.io/gitslice/proto/core/v1"
	"google.golang.org/grpc"
)

// SliceClient, RepositoryClient, ChangesetClient, and CheckClient are minimal
// unary service interfaces satisfied directly by both in-process service
// handlers (*service.SliceService, etc.) and gRPC client wrappers.
type SliceClient interface {
	ResolveSlice(ctx context.Context, req *corev1.ResolveSliceRequest) (*corev1.Slice, error)
}

type RepositoryClient interface {
	GetRef(ctx context.Context, req *corev1.GetRefRequest) (*corev1.Ref, error)
	ListDirectory(ctx context.Context, req *corev1.ListDirectoryRequest) (*corev1.ListDirectoryResponse, error)
	ListCommits(ctx context.Context, req *corev1.ListCommitsRequest) (*corev1.ListCommitsResponse, error)
	BlameFile(ctx context.Context, req *corev1.BlameFileRequest) (*corev1.BlameFileResponse, error)
}

type ChangesetClient interface {
	CreateChangeset(ctx context.Context, req *corev1.CreateChangesetRequest) (*corev1.Changeset, error)
	UpdateChangeset(ctx context.Context, req *corev1.UpdateChangesetRequest) (*corev1.Patchset, error)
	AbandonChangeset(ctx context.Context, req *corev1.AbandonChangesetRequest) (*corev1.Empty, error)
	GetChangeset(ctx context.Context, req *corev1.GetChangesetRequest) (*corev1.Changeset, error)
	SubmitChangeset(ctx context.Context, req *corev1.SubmitChangesetRequest) (*corev1.SubmitChangesetResponse, error)
}

type CheckClient interface {
	ListCheckRuns(ctx context.Context, req *corev1.ListCheckRunsRequest) (*corev1.ListCheckRunsResponse, error)
}

type grpcSliceClient struct{ c corev1.SliceServiceClient }

func (g grpcSliceClient) ResolveSlice(ctx context.Context, req *corev1.ResolveSliceRequest) (*corev1.Slice, error) {
	return g.c.ResolveSlice(ctx, req)
}

type grpcRepoClient struct {
	c corev1.RepositoryServiceClient
}

func (g grpcRepoClient) GetRef(ctx context.Context, req *corev1.GetRefRequest) (*corev1.Ref, error) {
	return g.c.GetRef(ctx, req)
}
func (g grpcRepoClient) ListDirectory(ctx context.Context, req *corev1.ListDirectoryRequest) (*corev1.ListDirectoryResponse, error) {
	return g.c.ListDirectory(ctx, req)
}
func (g grpcRepoClient) ListCommits(ctx context.Context, req *corev1.ListCommitsRequest) (*corev1.ListCommitsResponse, error) {
	return g.c.ListCommits(ctx, req)
}
func (g grpcRepoClient) BlameFile(ctx context.Context, req *corev1.BlameFileRequest) (*corev1.BlameFileResponse, error) {
	return g.c.BlameFile(ctx, req)
}

type grpcChangesetClient struct{ c corev1.ChangesetServiceClient }

func (g grpcChangesetClient) CreateChangeset(ctx context.Context, req *corev1.CreateChangesetRequest) (*corev1.Changeset, error) {
	return g.c.CreateChangeset(ctx, req)
}
func (g grpcChangesetClient) UpdateChangeset(ctx context.Context, req *corev1.UpdateChangesetRequest) (*corev1.Patchset, error) {
	return g.c.UpdateChangeset(ctx, req)
}
func (g grpcChangesetClient) AbandonChangeset(ctx context.Context, req *corev1.AbandonChangesetRequest) (*corev1.Empty, error) {
	return g.c.AbandonChangeset(ctx, req)
}
func (g grpcChangesetClient) GetChangeset(ctx context.Context, req *corev1.GetChangesetRequest) (*corev1.Changeset, error) {
	return g.c.GetChangeset(ctx, req)
}
func (g grpcChangesetClient) SubmitChangeset(ctx context.Context, req *corev1.SubmitChangesetRequest) (*corev1.SubmitChangesetResponse, error) {
	return g.c.SubmitChangeset(ctx, req)
}

type grpcCheckClient struct{ c corev1.CheckServiceClient }

func (g grpcCheckClient) ListCheckRuns(ctx context.Context, req *corev1.ListCheckRunsRequest) (*corev1.ListCheckRunsResponse, error) {
	return g.c.ListCheckRuns(ctx, req)
}

func (b *Backend) slices() SliceClient {
	if b.deps.Slices != nil {
		return b.deps.Slices
	}
	return grpcSliceClient{c: corev1.NewSliceServiceClient(b.deps.Conn)}
}

func (b *Backend) repo() RepositoryClient {
	if b.deps.Repo != nil {
		return b.deps.Repo
	}
	return grpcRepoClient{c: corev1.NewRepositoryServiceClient(b.deps.Conn)}
}

func (b *Backend) changesets() ChangesetClient {
	if b.deps.Changesets != nil {
		return b.deps.Changesets
	}
	return grpcChangesetClient{c: corev1.NewChangesetServiceClient(b.deps.Conn)}
}

func (b *Backend) checks() CheckClient {
	if b.deps.Checks != nil {
		return b.deps.Checks
	}
	return grpcCheckClient{c: corev1.NewCheckServiceClient(b.deps.Conn)}
}

// ensure grpc import used when only Conn field is referenced
var _ *grpc.ClientConn
