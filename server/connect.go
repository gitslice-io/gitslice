package server

import (
	"context"
	"errors"
	"io"
	"net/http"

	"connectrpc.com/connect"
	"github.com/gitslice-io/gitslice/internal/authctx"
	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
	corev1connect "github.com/gitslice-io/gitslice/proto/core/v1/corev1connect"
	"github.com/gitslice-io/gitslice/service"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func NewConnectHandler(resolve subjectResolver, handlers *service.Handlers) http.Handler {
	mux := http.NewServeMux()
	mount := func(pattern string, handler http.Handler) {
		mux.Handle(pattern, connectAuthMiddleware(resolve)(handler))
	}
	mount(corev1connect.NewAuthServiceHandler(connectAuthAdapter{svc: handlers.Auth}))
	mount(corev1connect.NewRepositoryServiceHandler(connectRepositoryAdapter{svc: handlers.Repository}))
	mount(corev1connect.NewBlobServiceHandler(connectBlobAdapter{svc: handlers.Blob}))
	mount(corev1connect.NewSliceServiceHandler(connectSliceAdapter{svc: handlers.Slice}))
	mount(corev1connect.NewWorkspaceServiceHandler(connectWorkspaceAdapter{svc: handlers.Workspace}))
	mount(corev1connect.NewChangesetServiceHandler(connectChangesetAdapter{svc: handlers.Changeset}))
	mount(corev1connect.NewChangesetStackServiceHandler(connectStackAdapter{svc: handlers.Stack}))
	mount(corev1connect.NewAgentServiceHandler(connectAgentAdapter{svc: handlers.Agent}))
	mount(corev1connect.NewCheckServiceHandler(connectCheckAdapter{svc: handlers.Check}))
	return mux
}

func connectAuthMiddleware(resolve subjectResolver) func(http.Handler) http.Handler {
	errorWriter := connect.NewErrorWriter()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isPublicMethod(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			token, ok, err := optionalBearerTokenValue(r.Header.Get("Authorization"))
			if err != nil {
				_ = errorWriter.Write(w, r, connectError(err))
				return
			}
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			subjectID, err := resolve(r.Context(), token)
			if err != nil {
				_ = errorWriter.Write(w, r, connectAuthError(err))
				return
			}
			next.ServeHTTP(w, r.WithContext(authctx.WithSubjectID(r.Context(), subjectID)))
		})
	}
}

func connectResponse[T any](msg *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(msg), nil
}

func connectError(err error) error {
	if err == nil {
		return nil
	}
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		return err
	}
	if st, ok := status.FromError(err); ok {
		return connect.NewError(connect.Code(st.Code()), errors.New(st.Message()))
	}
	return err
}

func connectAuthError(err error) error {
	return connectError(grpcAuthError(err))
}

type connectAuthAdapter struct {
	svc *service.AuthService
}

func (a connectAuthAdapter) StartCliLogin(ctx context.Context, req *connect.Request[corev1.StartCliLoginRequest]) (*connect.Response[corev1.StartCliLoginResponse], error) {
	return connectResponse(a.svc.StartCliLogin(ctx, req.Msg))
}

func (a connectAuthAdapter) PollCliLogin(ctx context.Context, req *connect.Request[corev1.PollCliLoginRequest]) (*connect.Response[corev1.PollCliLoginResponse], error) {
	return connectResponse(a.svc.PollCliLogin(ctx, req.Msg))
}

func (a connectAuthAdapter) CompleteCliLogin(ctx context.Context, req *connect.Request[corev1.CompleteCliLoginRequest]) (*connect.Response[corev1.CompleteCliLoginResponse], error) {
	return connectResponse(a.svc.CompleteCliLogin(ctx, req.Msg))
}

func (a connectAuthAdapter) GetAuthStatus(ctx context.Context, req *connect.Request[corev1.GetAuthStatusRequest]) (*connect.Response[corev1.GetAuthStatusResponse], error) {
	return connectResponse(a.svc.GetAuthStatus(ctx, req.Msg))
}

func (a connectAuthAdapter) CheckUsernameAvailable(ctx context.Context, req *connect.Request[corev1.CheckUsernameAvailableRequest]) (*connect.Response[corev1.CheckUsernameAvailableResponse], error) {
	return connectResponse(a.svc.CheckUsernameAvailable(ctx, req.Msg))
}

func (a connectAuthAdapter) ChooseUsername(ctx context.Context, req *connect.Request[corev1.ChooseUsernameRequest]) (*connect.Response[corev1.ChooseUsernameResponse], error) {
	return connectResponse(a.svc.ChooseUsername(ctx, req.Msg))
}

type connectRepositoryAdapter struct {
	svc *service.RepositoryService
}

func (a connectRepositoryAdapter) ResolvePath(ctx context.Context, req *connect.Request[corev1.ResolvePathRequest]) (*connect.Response[corev1.ResolvePathResponse], error) {
	return connectResponse(a.svc.ResolvePath(ctx, req.Msg))
}

func (a connectRepositoryAdapter) ListDirectory(ctx context.Context, req *connect.Request[corev1.ListDirectoryRequest]) (*connect.Response[corev1.ListDirectoryResponse], error) {
	return connectResponse(a.svc.ListDirectory(ctx, req.Msg))
}

func (a connectRepositoryAdapter) ReadFile(ctx context.Context, req *connect.Request[corev1.ReadFileRequest]) (*connect.Response[corev1.ReadFileResponse], error) {
	return connectResponse(a.svc.ReadFile(ctx, req.Msg))
}

func (a connectRepositoryAdapter) GetCommit(ctx context.Context, req *connect.Request[corev1.GetCommitRequest]) (*connect.Response[corev1.Commit], error) {
	return connectResponse(a.svc.GetCommit(ctx, req.Msg))
}

func (a connectRepositoryAdapter) ResolveCommit(ctx context.Context, req *connect.Request[corev1.ResolveCommitRequest]) (*connect.Response[corev1.ResolveCommitResponse], error) {
	return connectResponse(a.svc.ResolveCommit(ctx, req.Msg))
}

func (a connectRepositoryAdapter) ListCommits(ctx context.Context, req *connect.Request[corev1.ListCommitsRequest]) (*connect.Response[corev1.ListCommitsResponse], error) {
	return connectResponse(a.svc.ListCommits(ctx, req.Msg))
}

func (a connectRepositoryAdapter) GetRef(ctx context.Context, req *connect.Request[corev1.GetRefRequest]) (*connect.Response[corev1.Ref], error) {
	return connectResponse(a.svc.GetRef(ctx, req.Msg))
}

func (a connectRepositoryAdapter) ImportGitRepository(ctx context.Context, req *connect.Request[corev1.ImportGitRepositoryRequest]) (*connect.Response[corev1.ImportGitRepositoryResponse], error) {
	return connectResponse(a.svc.ImportGitRepository(ctx, req.Msg))
}

func (a connectRepositoryAdapter) ImportGitRepositoryStream(ctx context.Context, req *connect.Request[corev1.ImportGitRepositoryRequest], stream *connect.ServerStream[corev1.ImportGitRepositoryProgress]) error {
	return connectError(a.svc.ImportGitRepositoryStream(req.Msg, connectRepositoryImportStream{ctx: ctx, stream: stream}))
}

type connectBlobAdapter struct {
	svc *service.BlobService
}

func (a connectBlobAdapter) GetBlobStatus(ctx context.Context, req *connect.Request[corev1.GetBlobStatusRequest]) (*connect.Response[corev1.GetBlobStatusResponse], error) {
	return connectResponse(a.svc.GetBlobStatus(ctx, req.Msg))
}

func (a connectBlobAdapter) UploadBlob(ctx context.Context, req *connect.Request[corev1.UploadBlobRequest]) (*connect.Response[corev1.UploadBlobResponse], error) {
	return connectResponse(a.svc.UploadBlob(ctx, req.Msg))
}

func (a connectBlobAdapter) UploadBlobStream(ctx context.Context, stream *connect.ClientStream[corev1.UploadBlobChunk]) (*connect.Response[corev1.UploadBlobResponse], error) {
	adapter := &connectUploadBlobStream{ctx: ctx, stream: stream}
	if err := a.svc.UploadBlobStream(adapter); err != nil {
		return nil, connectError(err)
	}
	if adapter.response == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("upload completed without response"))
	}
	return connect.NewResponse(adapter.response), nil
}

func (a connectBlobAdapter) ReadBlobStream(ctx context.Context, req *connect.Request[corev1.ReadBlobStreamRequest], stream *connect.ServerStream[corev1.ReadBlobChunk]) error {
	return connectError(a.svc.ReadBlobStream(req.Msg, connectReadBlobStream{ctx: ctx, stream: stream}))
}

type connectSliceAdapter struct {
	svc *service.SliceService
}

func (a connectSliceAdapter) CreateSlice(ctx context.Context, req *connect.Request[corev1.CreateSliceRequest]) (*connect.Response[corev1.Slice], error) {
	return connectResponse(a.svc.CreateSlice(ctx, req.Msg))
}

func (a connectSliceAdapter) ResolveSlice(ctx context.Context, req *connect.Request[corev1.ResolveSliceRequest]) (*connect.Response[corev1.Slice], error) {
	return connectResponse(a.svc.ResolveSlice(ctx, req.Msg))
}

func (a connectSliceAdapter) GetSlice(ctx context.Context, req *connect.Request[corev1.GetSliceRequest]) (*connect.Response[corev1.Slice], error) {
	return connectResponse(a.svc.GetSlice(ctx, req.Msg))
}

func (a connectSliceAdapter) ListSlices(ctx context.Context, req *connect.Request[corev1.ListSlicesRequest]) (*connect.Response[corev1.ListSlicesResponse], error) {
	return connectResponse(a.svc.ListSlices(ctx, req.Msg))
}

func (a connectSliceAdapter) ListSliceDefinitionVersions(ctx context.Context, req *connect.Request[corev1.ListSliceDefinitionVersionsRequest]) (*connect.Response[corev1.ListSliceDefinitionVersionsResponse], error) {
	return connectResponse(a.svc.ListSliceDefinitionVersions(ctx, req.Msg))
}

func (a connectSliceAdapter) UpdateSliceDefinition(ctx context.Context, req *connect.Request[corev1.UpdateSliceDefinitionRequest]) (*connect.Response[corev1.SliceDefinition], error) {
	return connectResponse(a.svc.UpdateSliceDefinition(ctx, req.Msg))
}

func (a connectSliceAdapter) DeleteSlice(ctx context.Context, req *connect.Request[corev1.DeleteSliceRequest]) (*connect.Response[corev1.DeleteSliceResponse], error) {
	return connectResponse(a.svc.DeleteSlice(ctx, req.Msg))
}

type connectWorkspaceAdapter struct {
	svc *service.WorkspaceService
}

func (a connectWorkspaceAdapter) GetWorkspaceState(ctx context.Context, req *connect.Request[corev1.GetWorkspaceStateRequest]) (*connect.Response[corev1.WorkspaceState], error) {
	return connectResponse(a.svc.GetWorkspaceState(ctx, req.Msg))
}

func (a connectWorkspaceAdapter) HydratePaths(ctx context.Context, req *connect.Request[corev1.HydratePathsRequest]) (*connect.Response[corev1.HydratePathsResponse], error) {
	return connectResponse(a.svc.HydratePaths(ctx, req.Msg))
}

func (a connectWorkspaceAdapter) ValidateWorkspaceDiff(ctx context.Context, req *connect.Request[corev1.ValidateWorkspaceDiffRequest]) (*connect.Response[corev1.ValidateWorkspaceDiffResponse], error) {
	return connectResponse(a.svc.ValidateWorkspaceDiff(ctx, req.Msg))
}

func (a connectWorkspaceAdapter) RecordWorkspaceOperation(ctx context.Context, req *connect.Request[corev1.RecordWorkspaceOperationRequest]) (*connect.Response[corev1.RecordWorkspaceOperationResponse], error) {
	return connectResponse(a.svc.RecordWorkspaceOperation(ctx, req.Msg))
}

type connectChangesetAdapter struct {
	svc *service.ChangesetService
}

func (a connectChangesetAdapter) CreateChangeset(ctx context.Context, req *connect.Request[corev1.CreateChangesetRequest]) (*connect.Response[corev1.Changeset], error) {
	return connectResponse(a.svc.CreateChangeset(ctx, req.Msg))
}

func (a connectChangesetAdapter) GetChangeset(ctx context.Context, req *connect.Request[corev1.GetChangesetRequest]) (*connect.Response[corev1.Changeset], error) {
	return connectResponse(a.svc.GetChangeset(ctx, req.Msg))
}

func (a connectChangesetAdapter) ListChangesets(ctx context.Context, req *connect.Request[corev1.ListChangesetsRequest]) (*connect.Response[corev1.ListChangesetsResponse], error) {
	return connectResponse(a.svc.ListChangesets(ctx, req.Msg))
}

func (a connectChangesetAdapter) DiffChangeset(ctx context.Context, req *connect.Request[corev1.DiffChangesetRequest]) (*connect.Response[corev1.DiffChangesetResponse], error) {
	return connectResponse(a.svc.DiffChangeset(ctx, req.Msg))
}

func (a connectChangesetAdapter) UpdateChangeset(ctx context.Context, req *connect.Request[corev1.UpdateChangesetRequest]) (*connect.Response[corev1.Patchset], error) {
	return connectResponse(a.svc.UpdateChangeset(ctx, req.Msg))
}

func (a connectChangesetAdapter) ApproveChangeset(ctx context.Context, req *connect.Request[corev1.ApproveChangesetRequest]) (*connect.Response[corev1.ApproveChangesetResponse], error) {
	return connectResponse(a.svc.ApproveChangeset(ctx, req.Msg))
}

func (a connectChangesetAdapter) ReportCheckResult(ctx context.Context, req *connect.Request[corev1.ReportCheckResultRequest]) (*connect.Response[corev1.ReportCheckResultResponse], error) {
	return connectResponse(a.svc.ReportCheckResult(ctx, req.Msg))
}

func (a connectChangesetAdapter) SubmitChangeset(ctx context.Context, req *connect.Request[corev1.SubmitChangesetRequest]) (*connect.Response[corev1.SubmitChangesetResponse], error) {
	return connectResponse(a.svc.SubmitChangeset(ctx, req.Msg))
}

func (a connectChangesetAdapter) AbandonChangeset(ctx context.Context, req *connect.Request[corev1.AbandonChangesetRequest]) (*connect.Response[corev1.Empty], error) {
	return connectResponse(a.svc.AbandonChangeset(ctx, req.Msg))
}

type connectStackAdapter struct {
	svc *service.ChangesetStackService
}

func (a connectStackAdapter) CreateStack(ctx context.Context, req *connect.Request[corev1.CreateStackRequest]) (*connect.Response[corev1.ChangesetStack], error) {
	return connectResponse(a.svc.CreateStack(ctx, req.Msg))
}

func (a connectStackAdapter) GetStack(ctx context.Context, req *connect.Request[corev1.GetStackRequest]) (*connect.Response[corev1.ChangesetStack], error) {
	return connectResponse(a.svc.GetStack(ctx, req.Msg))
}

func (a connectStackAdapter) ListStacks(ctx context.Context, req *connect.Request[corev1.ListStacksRequest]) (*connect.Response[corev1.ListStacksResponse], error) {
	return connectResponse(a.svc.ListStacks(ctx, req.Msg))
}

func (a connectStackAdapter) AddStackEntry(ctx context.Context, req *connect.Request[corev1.AddStackEntryRequest]) (*connect.Response[corev1.Changeset], error) {
	return connectResponse(a.svc.AddStackEntry(ctx, req.Msg))
}

func (a connectStackAdapter) MoveStackEntry(ctx context.Context, req *connect.Request[corev1.MoveStackEntryRequest]) (*connect.Response[corev1.ChangesetStack], error) {
	return connectResponse(a.svc.MoveStackEntry(ctx, req.Msg))
}

func (a connectStackAdapter) ReparentStackEntry(ctx context.Context, req *connect.Request[corev1.ReparentStackEntryRequest]) (*connect.Response[corev1.ChangesetStack], error) {
	return connectResponse(a.svc.ReparentStackEntry(ctx, req.Msg))
}

func (a connectStackAdapter) DetachStackEntry(ctx context.Context, req *connect.Request[corev1.DetachStackEntryRequest]) (*connect.Response[corev1.DetachStackEntryResponse], error) {
	return connectResponse(a.svc.DetachStackEntry(ctx, req.Msg))
}

func (a connectStackAdapter) Restack(ctx context.Context, req *connect.Request[corev1.RestackRequest]) (*connect.Response[corev1.RestackResponse], error) {
	return connectResponse(a.svc.Restack(ctx, req.Msg))
}

func (a connectStackAdapter) SubmitStack(ctx context.Context, req *connect.Request[corev1.SubmitStackRequest]) (*connect.Response[corev1.SubmitStackResponse], error) {
	return connectResponse(a.svc.SubmitStack(ctx, req.Msg))
}

type connectAgentAdapter struct {
	svc *service.AgentService
}

func (a connectAgentAdapter) Connect(ctx context.Context, stream *connect.BidiStream[corev1.DaemonMessage, corev1.ServerMessage]) error {
	return connectError(a.svc.Connect(connectAgentConnectStream{ctx: ctx, stream: stream}))
}

func (a connectAgentAdapter) ListDaemons(ctx context.Context, req *connect.Request[corev1.ListDaemonsRequest]) (*connect.Response[corev1.ListDaemonsResponse], error) {
	return connectResponse(a.svc.ListDaemons(ctx, req.Msg))
}

func (a connectAgentAdapter) CreateConversation(ctx context.Context, req *connect.Request[corev1.CreateConversationRequest]) (*connect.Response[corev1.Conversation], error) {
	return connectResponse(a.svc.CreateConversation(ctx, req.Msg))
}

func (a connectAgentAdapter) ListConversations(ctx context.Context, req *connect.Request[corev1.ListConversationsRequest]) (*connect.Response[corev1.ListConversationsResponse], error) {
	return connectResponse(a.svc.ListConversations(ctx, req.Msg))
}

func (a connectAgentAdapter) GetConversation(ctx context.Context, req *connect.Request[corev1.GetConversationRequest]) (*connect.Response[corev1.Conversation], error) {
	return connectResponse(a.svc.GetConversation(ctx, req.Msg))
}

func (a connectAgentAdapter) SendAgentMessage(ctx context.Context, req *connect.Request[corev1.SendAgentMessageRequest]) (*connect.Response[corev1.SendAgentMessageResponse], error) {
	return connectResponse(a.svc.SendAgentMessage(ctx, req.Msg))
}

func (a connectAgentAdapter) StreamConversation(ctx context.Context, req *connect.Request[corev1.StreamConversationRequest], stream *connect.ServerStream[corev1.ConversationEvent]) error {
	return connectError(a.svc.StreamConversation(req.Msg, connectAgentConversationStream{ctx: ctx, stream: stream}))
}

func (a connectAgentAdapter) GetConversationEvents(ctx context.Context, req *connect.Request[corev1.GetConversationEventsRequest]) (*connect.Response[corev1.GetConversationEventsResponse], error) {
	return connectResponse(a.svc.GetConversationEvents(ctx, req.Msg))
}

func (a connectAgentAdapter) CloseConversation(ctx context.Context, req *connect.Request[corev1.CloseConversationRequest]) (*connect.Response[corev1.Conversation], error) {
	return connectResponse(a.svc.CloseConversation(ctx, req.Msg))
}

type connectCheckAdapter struct {
	svc *service.CheckService
}

func (a connectCheckAdapter) ListCheckRuns(ctx context.Context, req *connect.Request[corev1.ListCheckRunsRequest]) (*connect.Response[corev1.ListCheckRunsResponse], error) {
	return connectResponse(a.svc.ListCheckRuns(ctx, req.Msg))
}

func (a connectCheckAdapter) GetCheckRun(ctx context.Context, req *connect.Request[corev1.GetCheckRunRequest]) (*connect.Response[corev1.CheckRun], error) {
	return connectResponse(a.svc.GetCheckRun(ctx, req.Msg))
}

func (a connectCheckAdapter) StreamCheckRun(ctx context.Context, req *connect.Request[corev1.StreamCheckRunRequest], stream *connect.ServerStream[corev1.CheckRunLog]) error {
	return connectError(a.svc.StreamCheckRun(req.Msg, connectCheckRunLogStream{ctx: ctx, stream: stream}))
}

type connectGRPCServerStream struct {
	ctx context.Context
}

func (s connectGRPCServerStream) SetHeader(metadata.MD) error {
	return nil
}

func (s connectGRPCServerStream) SendHeader(metadata.MD) error {
	return nil
}

func (s connectGRPCServerStream) SetTrailer(metadata.MD) {}

func (s connectGRPCServerStream) Context() context.Context {
	return s.ctx
}

func (s connectGRPCServerStream) SendMsg(any) error {
	return status.Error(codes.Unimplemented, "SendMsg is not implemented by connect stream adapter")
}

func (s connectGRPCServerStream) RecvMsg(any) error {
	return status.Error(codes.Unimplemented, "RecvMsg is not implemented by connect stream adapter")
}

type connectRepositoryImportStream struct {
	connectGRPCServerStream
	ctx    context.Context
	stream *connect.ServerStream[corev1.ImportGitRepositoryProgress]
}

func (s connectRepositoryImportStream) Context() context.Context {
	return s.ctx
}

func (s connectRepositoryImportStream) Send(msg *corev1.ImportGitRepositoryProgress) error {
	return s.stream.Send(msg)
}

type connectReadBlobStream struct {
	connectGRPCServerStream
	ctx    context.Context
	stream *connect.ServerStream[corev1.ReadBlobChunk]
}

func (s connectReadBlobStream) Context() context.Context {
	return s.ctx
}

func (s connectReadBlobStream) Send(msg *corev1.ReadBlobChunk) error {
	return s.stream.Send(msg)
}

type connectUploadBlobStream struct {
	connectGRPCServerStream
	ctx      context.Context
	stream   *connect.ClientStream[corev1.UploadBlobChunk]
	response *corev1.UploadBlobResponse
}

func (s *connectUploadBlobStream) Context() context.Context {
	return s.ctx
}

func (s *connectUploadBlobStream) Recv() (*corev1.UploadBlobChunk, error) {
	if !s.stream.Receive() {
		if err := s.stream.Err(); err != nil {
			return nil, connectError(err)
		}
		return nil, io.EOF
	}
	return s.stream.Msg(), nil
}

func (s *connectUploadBlobStream) SendAndClose(msg *corev1.UploadBlobResponse) error {
	s.response = msg
	return nil
}

type connectAgentConnectStream struct {
	connectGRPCServerStream
	ctx    context.Context
	stream *connect.BidiStream[corev1.DaemonMessage, corev1.ServerMessage]
}

func (s connectAgentConnectStream) Context() context.Context {
	return s.ctx
}

func (s connectAgentConnectStream) Send(msg *corev1.ServerMessage) error {
	return s.stream.Send(msg)
}

func (s connectAgentConnectStream) Recv() (*corev1.DaemonMessage, error) {
	msg, err := s.stream.Receive()
	if err != nil {
		return nil, connectError(err)
	}
	return msg, nil
}

type connectAgentConversationStream struct {
	connectGRPCServerStream
	ctx    context.Context
	stream *connect.ServerStream[corev1.ConversationEvent]
}

func (s connectAgentConversationStream) Context() context.Context {
	return s.ctx
}

func (s connectAgentConversationStream) Send(msg *corev1.ConversationEvent) error {
	return s.stream.Send(msg)
}

type connectCheckRunLogStream struct {
	connectGRPCServerStream
	ctx    context.Context
	stream *connect.ServerStream[corev1.CheckRunLog]
}

func (s connectCheckRunLogStream) Context() context.Context {
	return s.ctx
}

func (s connectCheckRunLogStream) Send(msg *corev1.CheckRunLog) error {
	return s.stream.Send(msg)
}
