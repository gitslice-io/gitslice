package corev1

import (
	"context"

	"google.golang.org/grpc"
)

type FakeAccountServiceServer interface {
	Login(context.Context, *LoginRequest) (*LoginResponse, error)
}

type FakeAccountServiceClient interface {
	Login(context.Context, *LoginRequest, ...grpc.CallOption) (*LoginResponse, error)
}

type fakeAccountServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewFakeAccountServiceClient(cc grpc.ClientConnInterface) FakeAccountServiceClient {
	return &fakeAccountServiceClient{cc: cc}
}

func (c *fakeAccountServiceClient) Login(ctx context.Context, in *LoginRequest, opts ...grpc.CallOption) (*LoginResponse, error) {
	out := new(LoginResponse)
	err := c.cc.Invoke(ctx, "/gitslice.core.v1.FakeAccountService/Login", in, out, opts...)
	return out, err
}

func RegisterFakeAccountServiceServer(s grpc.ServiceRegistrar, srv FakeAccountServiceServer) {
	s.RegisterService(&grpc.ServiceDesc{
		ServiceName: "gitslice.core.v1.FakeAccountService",
		HandlerType: (*FakeAccountServiceServer)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "Login",
			Handler:    unaryHandler("/gitslice.core.v1.FakeAccountService/Login", srv.Login),
		}},
	}, srv)
}

type RepositoryServiceServer interface {
	ResolvePath(context.Context, *ResolvePathRequest) (*ResolvePathResponse, error)
	ListDirectory(context.Context, *ListDirectoryRequest) (*ListDirectoryResponse, error)
	ReadFile(context.Context, *ReadFileRequest) (*ReadFileResponse, error)
	GetCommit(context.Context, *GetCommitRequest) (*Commit, error)
	GetRef(context.Context, *GetRefRequest) (*Ref, error)
}

type RepositoryServiceClient interface {
	ResolvePath(context.Context, *ResolvePathRequest, ...grpc.CallOption) (*ResolvePathResponse, error)
	ListDirectory(context.Context, *ListDirectoryRequest, ...grpc.CallOption) (*ListDirectoryResponse, error)
	ReadFile(context.Context, *ReadFileRequest, ...grpc.CallOption) (*ReadFileResponse, error)
	GetCommit(context.Context, *GetCommitRequest, ...grpc.CallOption) (*Commit, error)
	GetRef(context.Context, *GetRefRequest, ...grpc.CallOption) (*Ref, error)
}

type repositoryServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewRepositoryServiceClient(cc grpc.ClientConnInterface) RepositoryServiceClient {
	return &repositoryServiceClient{cc: cc}
}

func (c *repositoryServiceClient) ResolvePath(ctx context.Context, in *ResolvePathRequest, opts ...grpc.CallOption) (*ResolvePathResponse, error) {
	out := new(ResolvePathResponse)
	err := c.cc.Invoke(ctx, "/gitslice.core.v1.RepositoryService/ResolvePath", in, out, opts...)
	return out, err
}

func (c *repositoryServiceClient) ListDirectory(ctx context.Context, in *ListDirectoryRequest, opts ...grpc.CallOption) (*ListDirectoryResponse, error) {
	out := new(ListDirectoryResponse)
	err := c.cc.Invoke(ctx, "/gitslice.core.v1.RepositoryService/ListDirectory", in, out, opts...)
	return out, err
}

func (c *repositoryServiceClient) ReadFile(ctx context.Context, in *ReadFileRequest, opts ...grpc.CallOption) (*ReadFileResponse, error) {
	out := new(ReadFileResponse)
	err := c.cc.Invoke(ctx, "/gitslice.core.v1.RepositoryService/ReadFile", in, out, opts...)
	return out, err
}

func (c *repositoryServiceClient) GetCommit(ctx context.Context, in *GetCommitRequest, opts ...grpc.CallOption) (*Commit, error) {
	out := new(Commit)
	err := c.cc.Invoke(ctx, "/gitslice.core.v1.RepositoryService/GetCommit", in, out, opts...)
	return out, err
}

func (c *repositoryServiceClient) GetRef(ctx context.Context, in *GetRefRequest, opts ...grpc.CallOption) (*Ref, error) {
	out := new(Ref)
	err := c.cc.Invoke(ctx, "/gitslice.core.v1.RepositoryService/GetRef", in, out, opts...)
	return out, err
}

func RegisterRepositoryServiceServer(s grpc.ServiceRegistrar, srv RepositoryServiceServer) {
	s.RegisterService(&grpc.ServiceDesc{
		ServiceName: "gitslice.core.v1.RepositoryService",
		HandlerType: (*RepositoryServiceServer)(nil),
		Methods: []grpc.MethodDesc{
			{MethodName: "ResolvePath", Handler: unaryHandler("/gitslice.core.v1.RepositoryService/ResolvePath", srv.ResolvePath)},
			{MethodName: "ListDirectory", Handler: unaryHandler("/gitslice.core.v1.RepositoryService/ListDirectory", srv.ListDirectory)},
			{MethodName: "ReadFile", Handler: unaryHandler("/gitslice.core.v1.RepositoryService/ReadFile", srv.ReadFile)},
			{MethodName: "GetCommit", Handler: unaryHandler("/gitslice.core.v1.RepositoryService/GetCommit", srv.GetCommit)},
			{MethodName: "GetRef", Handler: unaryHandler("/gitslice.core.v1.RepositoryService/GetRef", srv.GetRef)},
		},
	}, srv)
}

type BlobServiceServer interface {
	GetBlobStatus(context.Context, *GetBlobStatusRequest) (*GetBlobStatusResponse, error)
	UploadBlob(context.Context, *UploadBlobRequest) (*UploadBlobResponse, error)
}

type BlobServiceClient interface {
	GetBlobStatus(context.Context, *GetBlobStatusRequest, ...grpc.CallOption) (*GetBlobStatusResponse, error)
	UploadBlob(context.Context, *UploadBlobRequest, ...grpc.CallOption) (*UploadBlobResponse, error)
}

type blobServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewBlobServiceClient(cc grpc.ClientConnInterface) BlobServiceClient {
	return &blobServiceClient{cc: cc}
}

func (c *blobServiceClient) GetBlobStatus(ctx context.Context, in *GetBlobStatusRequest, opts ...grpc.CallOption) (*GetBlobStatusResponse, error) {
	out := new(GetBlobStatusResponse)
	err := c.cc.Invoke(ctx, "/gitslice.core.v1.BlobService/GetBlobStatus", in, out, opts...)
	return out, err
}

func (c *blobServiceClient) UploadBlob(ctx context.Context, in *UploadBlobRequest, opts ...grpc.CallOption) (*UploadBlobResponse, error) {
	out := new(UploadBlobResponse)
	err := c.cc.Invoke(ctx, "/gitslice.core.v1.BlobService/UploadBlob", in, out, opts...)
	return out, err
}

func RegisterBlobServiceServer(s grpc.ServiceRegistrar, srv BlobServiceServer) {
	s.RegisterService(&grpc.ServiceDesc{
		ServiceName: "gitslice.core.v1.BlobService",
		HandlerType: (*BlobServiceServer)(nil),
		Methods: []grpc.MethodDesc{
			{MethodName: "GetBlobStatus", Handler: unaryHandler("/gitslice.core.v1.BlobService/GetBlobStatus", srv.GetBlobStatus)},
			{MethodName: "UploadBlob", Handler: unaryHandler("/gitslice.core.v1.BlobService/UploadBlob", srv.UploadBlob)},
		},
	}, srv)
}

type SliceServiceServer interface {
	ResolveSlice(context.Context, *ResolveSliceRequest) (*Slice, error)
	GetSlice(context.Context, *GetSliceRequest) (*Slice, error)
	ListSlices(context.Context, *ListSlicesRequest) (*ListSlicesResponse, error)
	UpdateSliceDefinition(context.Context, *UpdateSliceDefinitionRequest) (*SliceDefinition, error)
}

type SliceServiceClient interface {
	ResolveSlice(context.Context, *ResolveSliceRequest, ...grpc.CallOption) (*Slice, error)
	GetSlice(context.Context, *GetSliceRequest, ...grpc.CallOption) (*Slice, error)
	ListSlices(context.Context, *ListSlicesRequest, ...grpc.CallOption) (*ListSlicesResponse, error)
	UpdateSliceDefinition(context.Context, *UpdateSliceDefinitionRequest, ...grpc.CallOption) (*SliceDefinition, error)
}

type sliceServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewSliceServiceClient(cc grpc.ClientConnInterface) SliceServiceClient {
	return &sliceServiceClient{cc: cc}
}

func (c *sliceServiceClient) ResolveSlice(ctx context.Context, in *ResolveSliceRequest, opts ...grpc.CallOption) (*Slice, error) {
	out := new(Slice)
	err := c.cc.Invoke(ctx, "/gitslice.core.v1.SliceService/ResolveSlice", in, out, opts...)
	return out, err
}

func (c *sliceServiceClient) GetSlice(ctx context.Context, in *GetSliceRequest, opts ...grpc.CallOption) (*Slice, error) {
	out := new(Slice)
	err := c.cc.Invoke(ctx, "/gitslice.core.v1.SliceService/GetSlice", in, out, opts...)
	return out, err
}

func (c *sliceServiceClient) ListSlices(ctx context.Context, in *ListSlicesRequest, opts ...grpc.CallOption) (*ListSlicesResponse, error) {
	out := new(ListSlicesResponse)
	err := c.cc.Invoke(ctx, "/gitslice.core.v1.SliceService/ListSlices", in, out, opts...)
	return out, err
}

func (c *sliceServiceClient) UpdateSliceDefinition(ctx context.Context, in *UpdateSliceDefinitionRequest, opts ...grpc.CallOption) (*SliceDefinition, error) {
	out := new(SliceDefinition)
	err := c.cc.Invoke(ctx, "/gitslice.core.v1.SliceService/UpdateSliceDefinition", in, out, opts...)
	return out, err
}

func RegisterSliceServiceServer(s grpc.ServiceRegistrar, srv SliceServiceServer) {
	s.RegisterService(&grpc.ServiceDesc{
		ServiceName: "gitslice.core.v1.SliceService",
		HandlerType: (*SliceServiceServer)(nil),
		Methods: []grpc.MethodDesc{
			{MethodName: "ResolveSlice", Handler: unaryHandler("/gitslice.core.v1.SliceService/ResolveSlice", srv.ResolveSlice)},
			{MethodName: "GetSlice", Handler: unaryHandler("/gitslice.core.v1.SliceService/GetSlice", srv.GetSlice)},
			{MethodName: "ListSlices", Handler: unaryHandler("/gitslice.core.v1.SliceService/ListSlices", srv.ListSlices)},
			{MethodName: "UpdateSliceDefinition", Handler: unaryHandler("/gitslice.core.v1.SliceService/UpdateSliceDefinition", srv.UpdateSliceDefinition)},
		},
	}, srv)
}

type WorkspaceServiceServer interface {
	GetWorkspaceState(context.Context, *GetWorkspaceStateRequest) (*WorkspaceState, error)
	HydratePaths(context.Context, *HydratePathsRequest) (*HydratePathsResponse, error)
	ValidateWorkspaceDiff(context.Context, *ValidateWorkspaceDiffRequest) (*ValidateWorkspaceDiffResponse, error)
	RecordWorkspaceOperation(context.Context, *RecordWorkspaceOperationRequest) (*RecordWorkspaceOperationResponse, error)
}

type WorkspaceServiceClient interface {
	GetWorkspaceState(context.Context, *GetWorkspaceStateRequest, ...grpc.CallOption) (*WorkspaceState, error)
	HydratePaths(context.Context, *HydratePathsRequest, ...grpc.CallOption) (*HydratePathsResponse, error)
	ValidateWorkspaceDiff(context.Context, *ValidateWorkspaceDiffRequest, ...grpc.CallOption) (*ValidateWorkspaceDiffResponse, error)
	RecordWorkspaceOperation(context.Context, *RecordWorkspaceOperationRequest, ...grpc.CallOption) (*RecordWorkspaceOperationResponse, error)
}

type workspaceServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewWorkspaceServiceClient(cc grpc.ClientConnInterface) WorkspaceServiceClient {
	return &workspaceServiceClient{cc: cc}
}

func (c *workspaceServiceClient) GetWorkspaceState(ctx context.Context, in *GetWorkspaceStateRequest, opts ...grpc.CallOption) (*WorkspaceState, error) {
	out := new(WorkspaceState)
	err := c.cc.Invoke(ctx, "/gitslice.core.v1.WorkspaceService/GetWorkspaceState", in, out, opts...)
	return out, err
}

func (c *workspaceServiceClient) HydratePaths(ctx context.Context, in *HydratePathsRequest, opts ...grpc.CallOption) (*HydratePathsResponse, error) {
	out := new(HydratePathsResponse)
	err := c.cc.Invoke(ctx, "/gitslice.core.v1.WorkspaceService/HydratePaths", in, out, opts...)
	return out, err
}

func (c *workspaceServiceClient) ValidateWorkspaceDiff(ctx context.Context, in *ValidateWorkspaceDiffRequest, opts ...grpc.CallOption) (*ValidateWorkspaceDiffResponse, error) {
	out := new(ValidateWorkspaceDiffResponse)
	err := c.cc.Invoke(ctx, "/gitslice.core.v1.WorkspaceService/ValidateWorkspaceDiff", in, out, opts...)
	return out, err
}

func (c *workspaceServiceClient) RecordWorkspaceOperation(ctx context.Context, in *RecordWorkspaceOperationRequest, opts ...grpc.CallOption) (*RecordWorkspaceOperationResponse, error) {
	out := new(RecordWorkspaceOperationResponse)
	err := c.cc.Invoke(ctx, "/gitslice.core.v1.WorkspaceService/RecordWorkspaceOperation", in, out, opts...)
	return out, err
}

func RegisterWorkspaceServiceServer(s grpc.ServiceRegistrar, srv WorkspaceServiceServer) {
	s.RegisterService(&grpc.ServiceDesc{
		ServiceName: "gitslice.core.v1.WorkspaceService",
		HandlerType: (*WorkspaceServiceServer)(nil),
		Methods: []grpc.MethodDesc{
			{MethodName: "GetWorkspaceState", Handler: unaryHandler("/gitslice.core.v1.WorkspaceService/GetWorkspaceState", srv.GetWorkspaceState)},
			{MethodName: "HydratePaths", Handler: unaryHandler("/gitslice.core.v1.WorkspaceService/HydratePaths", srv.HydratePaths)},
			{MethodName: "ValidateWorkspaceDiff", Handler: unaryHandler("/gitslice.core.v1.WorkspaceService/ValidateWorkspaceDiff", srv.ValidateWorkspaceDiff)},
			{MethodName: "RecordWorkspaceOperation", Handler: unaryHandler("/gitslice.core.v1.WorkspaceService/RecordWorkspaceOperation", srv.RecordWorkspaceOperation)},
		},
	}, srv)
}

type ChangesetServiceServer interface {
	CreateChangeset(context.Context, *CreateChangesetRequest) (*Changeset, error)
	GetChangeset(context.Context, *GetChangesetRequest) (*Changeset, error)
	UpdateChangeset(context.Context, *UpdateChangesetRequest) (*Patchset, error)
	SubmitChangeset(context.Context, *SubmitChangesetRequest) (*SubmitChangesetResponse, error)
	AbandonChangeset(context.Context, *AbandonChangesetRequest) (*Empty, error)
}

type ChangesetServiceClient interface {
	CreateChangeset(context.Context, *CreateChangesetRequest, ...grpc.CallOption) (*Changeset, error)
	GetChangeset(context.Context, *GetChangesetRequest, ...grpc.CallOption) (*Changeset, error)
	UpdateChangeset(context.Context, *UpdateChangesetRequest, ...grpc.CallOption) (*Patchset, error)
	SubmitChangeset(context.Context, *SubmitChangesetRequest, ...grpc.CallOption) (*SubmitChangesetResponse, error)
	AbandonChangeset(context.Context, *AbandonChangesetRequest, ...grpc.CallOption) (*Empty, error)
}

type changesetServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewChangesetServiceClient(cc grpc.ClientConnInterface) ChangesetServiceClient {
	return &changesetServiceClient{cc: cc}
}

func (c *changesetServiceClient) CreateChangeset(ctx context.Context, in *CreateChangesetRequest, opts ...grpc.CallOption) (*Changeset, error) {
	out := new(Changeset)
	err := c.cc.Invoke(ctx, "/gitslice.core.v1.ChangesetService/CreateChangeset", in, out, opts...)
	return out, err
}

func (c *changesetServiceClient) GetChangeset(ctx context.Context, in *GetChangesetRequest, opts ...grpc.CallOption) (*Changeset, error) {
	out := new(Changeset)
	err := c.cc.Invoke(ctx, "/gitslice.core.v1.ChangesetService/GetChangeset", in, out, opts...)
	return out, err
}

func (c *changesetServiceClient) UpdateChangeset(ctx context.Context, in *UpdateChangesetRequest, opts ...grpc.CallOption) (*Patchset, error) {
	out := new(Patchset)
	err := c.cc.Invoke(ctx, "/gitslice.core.v1.ChangesetService/UpdateChangeset", in, out, opts...)
	return out, err
}

func (c *changesetServiceClient) SubmitChangeset(ctx context.Context, in *SubmitChangesetRequest, opts ...grpc.CallOption) (*SubmitChangesetResponse, error) {
	out := new(SubmitChangesetResponse)
	err := c.cc.Invoke(ctx, "/gitslice.core.v1.ChangesetService/SubmitChangeset", in, out, opts...)
	return out, err
}

func (c *changesetServiceClient) AbandonChangeset(ctx context.Context, in *AbandonChangesetRequest, opts ...grpc.CallOption) (*Empty, error) {
	out := new(Empty)
	err := c.cc.Invoke(ctx, "/gitslice.core.v1.ChangesetService/AbandonChangeset", in, out, opts...)
	return out, err
}

func RegisterChangesetServiceServer(s grpc.ServiceRegistrar, srv ChangesetServiceServer) {
	s.RegisterService(&grpc.ServiceDesc{
		ServiceName: "gitslice.core.v1.ChangesetService",
		HandlerType: (*ChangesetServiceServer)(nil),
		Methods: []grpc.MethodDesc{
			{MethodName: "CreateChangeset", Handler: unaryHandler("/gitslice.core.v1.ChangesetService/CreateChangeset", srv.CreateChangeset)},
			{MethodName: "GetChangeset", Handler: unaryHandler("/gitslice.core.v1.ChangesetService/GetChangeset", srv.GetChangeset)},
			{MethodName: "UpdateChangeset", Handler: unaryHandler("/gitslice.core.v1.ChangesetService/UpdateChangeset", srv.UpdateChangeset)},
			{MethodName: "SubmitChangeset", Handler: unaryHandler("/gitslice.core.v1.ChangesetService/SubmitChangeset", srv.SubmitChangeset)},
			{MethodName: "AbandonChangeset", Handler: unaryHandler("/gitslice.core.v1.ChangesetService/AbandonChangeset", srv.AbandonChangeset)},
		},
	}, srv)
}

func unaryHandler[Req any, Resp any](fullMethod string, fn func(context.Context, *Req) (*Resp, error)) func(any, context.Context, func(any) error, grpc.UnaryServerInterceptor) (any, error) {
	return func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		in := new(Req)
		if err := dec(in); err != nil {
			return nil, err
		}
		if interceptor == nil {
			return fn(ctx, in)
		}
		info := &grpc.UnaryServerInfo{Server: srv, FullMethod: fullMethod}
		handler := func(ctx context.Context, req any) (any, error) {
			return fn(ctx, req.(*Req))
		}
		return interceptor(ctx, in, info, handler)
	}
}
