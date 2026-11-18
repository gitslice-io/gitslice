package server

import (
	"context"
	"errors"
	"io"

	"connectrpc.com/connect"
	"gitslice.io/gitslice/internal/metrics"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"gitslice.io/gitslice/service"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type connectAgentAdapter struct {
	svc *service.AgentService
}

func (a connectAgentAdapter) StandbyPoll(ctx context.Context, req *connect.Request[corev1.StandbyPollRequest]) (*connect.Response[corev1.StandbyPollResponse], error) {
	return connectResponse(a.svc.StandbyPoll(ctx, req.Msg))
}

func (a connectAgentAdapter) Connect(ctx context.Context, stream *connect.BidiStream[corev1.DaemonMessage, corev1.ServerMessage]) error {
	return connectError(a.svc.Connect(connectAgentConnectStream{ctx: ctx, stream: stream}))
}

func (a connectAgentAdapter) ListDaemons(ctx context.Context, req *connect.Request[corev1.ListDaemonsRequest]) (*connect.Response[corev1.ListDaemonsResponse], error) {
	return connectResponse(a.svc.ListDaemons(ctx, req.Msg))
}

func (a connectAgentAdapter) RenameDaemon(ctx context.Context, req *connect.Request[corev1.RenameDaemonRequest]) (*connect.Response[corev1.AgentDaemon], error) {
	return connectResponse(a.svc.RenameDaemon(ctx, req.Msg))
}

func (a connectAgentAdapter) ExposeDaemon(ctx context.Context, req *connect.Request[corev1.ExposeDaemonRequest]) (*connect.Response[corev1.AgentDaemon], error) {
	return connectResponse(a.svc.ExposeDaemon(ctx, req.Msg))
}

func (a connectAgentAdapter) UnexposeDaemon(ctx context.Context, req *connect.Request[corev1.UnexposeDaemonRequest]) (*connect.Response[corev1.AgentDaemon], error) {
	return connectResponse(a.svc.UnexposeDaemon(ctx, req.Msg))
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

func (a connectCheckAdapter) RerunCheck(ctx context.Context, req *connect.Request[corev1.RerunCheckRequest]) (*connect.Response[corev1.CheckRun], error) {
	return connectResponse(a.svc.RerunCheck(ctx, req.Msg))
}

func (a connectCheckAdapter) StreamCheckRun(ctx context.Context, req *connect.Request[corev1.StreamCheckRunRequest], stream *connect.ServerStream[corev1.CheckRunLog]) error {
	return connectError(a.svc.StreamCheckRun(req.Msg, connectCheckRunLogStream{ctx: ctx, stream: stream}))
}

func (a connectCheckAdapter) AllowPatchsetSecrets(ctx context.Context, req *connect.Request[corev1.AllowPatchsetSecretsRequest]) (*connect.Response[corev1.AllowPatchsetSecretsResponse], error) {
	return connectResponse(a.svc.AllowPatchsetSecrets(ctx, req.Msg))
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

type connectRepositoryCheckoutStream struct {
	connectGRPCServerStream
	ctx    context.Context
	stream *connect.ServerStream[corev1.CheckoutResponse]
}

func (s connectRepositoryCheckoutStream) Context() context.Context {
	return s.ctx
}

func (s connectRepositoryCheckoutStream) Send(msg *corev1.CheckoutResponse) error {
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

type connectFeedbackAdapter struct {
	svc *service.FeedbackService
}

func (a connectFeedbackAdapter) SubmitFeedback(ctx context.Context, req *connect.Request[corev1.SubmitFeedbackRequest]) (*connect.Response[corev1.Feedback], error) {
	return connectResponse(a.svc.SubmitFeedback(ctx, req.Msg))
}

func (a connectFeedbackAdapter) ListFeedback(ctx context.Context, req *connect.Request[corev1.ListFeedbackRequest]) (*connect.Response[corev1.ListFeedbackResponse], error) {
	return connectResponse(a.svc.ListFeedback(ctx, req.Msg))
}

func (a connectFeedbackAdapter) ResolveFeedback(ctx context.Context, req *connect.Request[corev1.ResolveFeedbackRequest]) (*connect.Response[corev1.Feedback], error) {
	return connectResponse(a.svc.ResolveFeedback(ctx, req.Msg))
}

type connectSecretAdapter struct {
	svc *service.SecretService
}

func (a connectSecretAdapter) ListSecrets(ctx context.Context, req *connect.Request[corev1.ListSecretsRequest]) (*connect.Response[corev1.ListSecretsResponse], error) {
	return connectResponse(a.svc.ListSecrets(ctx, req.Msg))
}

func (a connectSecretAdapter) GetSecret(ctx context.Context, req *connect.Request[corev1.GetSecretRequest]) (*connect.Response[corev1.Secret], error) {
	return connectResponse(a.svc.GetSecret(ctx, req.Msg))
}

func (a connectSecretAdapter) CreateSecret(ctx context.Context, req *connect.Request[corev1.CreateSecretRequest]) (*connect.Response[corev1.Secret], error) {
	return connectResponse(a.svc.CreateSecret(ctx, req.Msg))
}

func (a connectSecretAdapter) UpdateSecret(ctx context.Context, req *connect.Request[corev1.UpdateSecretRequest]) (*connect.Response[corev1.Secret], error) {
	return connectResponse(a.svc.UpdateSecret(ctx, req.Msg))
}

func (a connectSecretAdapter) DeleteSecret(ctx context.Context, req *connect.Request[corev1.DeleteSecretRequest]) (*connect.Response[corev1.Empty], error) {
	return connectResponse(a.svc.DeleteSecret(ctx, req.Msg))
}

func (a connectSecretAdapter) RevealSecrets(ctx context.Context, req *connect.Request[corev1.RevealSecretsRequest]) (*connect.Response[corev1.RevealSecretsResponse], error) {
	return connectResponse(a.svc.RevealSecrets(ctx, req.Msg))
}

func (a connectSecretAdapter) RevealSliceSecrets(ctx context.Context, req *connect.Request[corev1.RevealSliceSecretsRequest]) (*connect.Response[corev1.RevealSliceSecretsResponse], error) {
	return connectResponse(a.svc.RevealSliceSecrets(ctx, req.Msg))
}

type connectTriggerServiceAdapter struct {
	svc *service.TriggerService
}

func (a connectTriggerServiceAdapter) ListTriggers(ctx context.Context, req *connect.Request[corev1.ListTriggersRequest]) (*connect.Response[corev1.ListTriggersResponse], error) {
	return connectResponse(a.svc.ListTriggers(ctx, req.Msg))
}

func (a connectTriggerServiceAdapter) ListTriggerRuns(ctx context.Context, req *connect.Request[corev1.ListTriggerRunsRequest]) (*connect.Response[corev1.ListTriggerRunsResponse], error) {
	return connectResponse(a.svc.ListTriggerRuns(ctx, req.Msg))
}

func (a connectTriggerServiceAdapter) GetTriggerRun(ctx context.Context, req *connect.Request[corev1.GetTriggerRunRequest]) (*connect.Response[corev1.TriggerRun], error) {
	return connectResponse(a.svc.GetTriggerRun(ctx, req.Msg))
}

func (a connectTriggerServiceAdapter) ListTriggerRunLogs(ctx context.Context, req *connect.Request[corev1.ListTriggerRunLogsRequest]) (*connect.Response[corev1.ListTriggerRunLogsResponse], error) {
	return connectResponse(a.svc.ListTriggerRunLogs(ctx, req.Msg))
}

func (a connectTriggerServiceAdapter) RunTrigger(ctx context.Context, req *connect.Request[corev1.RunTriggerRequest]) (*connect.Response[corev1.TriggerRun], error) {
	return connectResponse(a.svc.RunTrigger(ctx, req.Msg))
}

func (a connectTriggerServiceAdapter) RerunTriggerRun(ctx context.Context, req *connect.Request[corev1.RerunTriggerRunRequest]) (*connect.Response[corev1.TriggerRun], error) {
	return connectResponse(a.svc.RerunTriggerRun(ctx, req.Msg))
}

func (a connectTriggerServiceAdapter) CancelTriggerRun(ctx context.Context, req *connect.Request[corev1.CancelTriggerRunRequest]) (*connect.Response[corev1.TriggerRun], error) {
	return connectResponse(a.svc.CancelTriggerRun(ctx, req.Msg))
}

type connectTriggerRunServiceAdapter struct {
	svc      *service.TriggerRunService
	limiters *serverLimiters
}

func connectTriggerRunResponse[T any](ctx context.Context, limiters *serverLimiters, msg *T, err error) (*connect.Response[T], error) {
	if isAuthFailureError(err) && limiters != nil && limiters.triggerFail != nil {
		if ipKey, ok := clientIPKeyFromContext(ctx); ok && !limiters.triggerFail.Allow(ipKey) {
			ratelimitRejectedTotal.Inc(metrics.Labels{"transport": "http"})
			return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("trigger run authentication failure rate limit exceeded"))
		}
	}
	return connectResponse(msg, err)
}

func (a connectTriggerRunServiceAdapter) StartRun(ctx context.Context, req *connect.Request[corev1.StartTriggerRunRequest]) (*connect.Response[corev1.StartTriggerRunResponse], error) {
	msg, err := a.svc.StartRun(ctx, req.Msg)
	return connectTriggerRunResponse(ctx, a.limiters, msg, err)
}

func (a connectTriggerRunServiceAdapter) StartCallback(ctx context.Context, req *connect.Request[corev1.StartTriggerRunRequest]) (*connect.Response[corev1.StartTriggerRunResponse], error) {
	msg, err := a.svc.StartCallback(ctx, req.Msg)
	return connectTriggerRunResponse(ctx, a.limiters, msg, err)
}

func (a connectTriggerRunServiceAdapter) FinishRun(ctx context.Context, req *connect.Request[corev1.FinishTriggerRunRequest]) (*connect.Response[corev1.TriggerRun], error) {
	msg, err := a.svc.FinishRun(ctx, req.Msg)
	return connectTriggerRunResponse(ctx, a.limiters, msg, err)
}

func (a connectTriggerRunServiceAdapter) AppendRunLog(ctx context.Context, req *connect.Request[corev1.AppendTriggerRunLogRequest]) (*connect.Response[corev1.Empty], error) {
	msg, err := a.svc.AppendRunLog(ctx, req.Msg)
	return connectTriggerRunResponse(ctx, a.limiters, msg, err)
}

func (a connectTriggerRunServiceAdapter) GetRunState(ctx context.Context, req *connect.Request[corev1.TriggerRunTokenRequest]) (*connect.Response[corev1.TriggerRunState], error) {
	msg, err := a.svc.GetRunState(ctx, req.Msg)
	return connectTriggerRunResponse(ctx, a.limiters, msg, err)
}

func (a connectTriggerRunServiceAdapter) SetRunState(ctx context.Context, req *connect.Request[corev1.SetTriggerRunStateRequest]) (*connect.Response[corev1.Empty], error) {
	msg, err := a.svc.SetRunState(ctx, req.Msg)
	return connectTriggerRunResponse(ctx, a.limiters, msg, err)
}

func (a connectTriggerRunServiceAdapter) CreateCheck(ctx context.Context, req *connect.Request[corev1.CreateTriggerCheckRequest]) (*connect.Response[corev1.CheckRun], error) {
	msg, err := a.svc.CreateCheck(ctx, req.Msg)
	return connectTriggerRunResponse(ctx, a.limiters, msg, err)
}

func (a connectTriggerRunServiceAdapter) UpdateCheck(ctx context.Context, req *connect.Request[corev1.UpdateTriggerCheckRequest]) (*connect.Response[corev1.CheckRun], error) {
	msg, err := a.svc.UpdateCheck(ctx, req.Msg)
	return connectTriggerRunResponse(ctx, a.limiters, msg, err)
}

func (a connectTriggerRunServiceAdapter) ReadFile(ctx context.Context, req *connect.Request[corev1.TriggerReadFileRequest]) (*connect.Response[corev1.TriggerReadFileResponse], error) {
	msg, err := a.svc.ReadFile(ctx, req.Msg)
	return connectTriggerRunResponse(ctx, a.limiters, msg, err)
}

func (a connectTriggerRunServiceAdapter) ListDirectory(ctx context.Context, req *connect.Request[corev1.TriggerListDirectoryRequest]) (*connect.Response[corev1.TriggerListDirectoryResponse], error) {
	msg, err := a.svc.ListDirectory(ctx, req.Msg)
	return connectTriggerRunResponse(ctx, a.limiters, msg, err)
}

func (a connectTriggerRunServiceAdapter) GetChangeset(ctx context.Context, req *connect.Request[corev1.TriggerRunTokenRequest]) (*connect.Response[corev1.Changeset], error) {
	msg, err := a.svc.GetChangeset(ctx, req.Msg)
	return connectTriggerRunResponse(ctx, a.limiters, msg, err)
}
