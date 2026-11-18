package server

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	corev1 "gitslice.io/gitslice/proto/core/v1"
	"gitslice.io/gitslice/service"
)

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

func (a connectAuthAdapter) ListPendingClaims(ctx context.Context, req *connect.Request[corev1.ListPendingClaimsRequest]) (*connect.Response[corev1.ListPendingClaimsResponse], error) {
	return connectResponse(a.svc.ListPendingClaims(ctx, req.Msg))
}

func (a connectAuthAdapter) AcceptClaim(ctx context.Context, req *connect.Request[corev1.AcceptClaimRequest]) (*connect.Response[corev1.AcceptClaimResponse], error) {
	return connectResponse(a.svc.AcceptClaim(ctx, req.Msg))
}

func (a connectAuthAdapter) ListApiKeys(ctx context.Context, req *connect.Request[corev1.ListApiKeysRequest]) (*connect.Response[corev1.ListApiKeysResponse], error) {
	return connectResponse(a.svc.ListApiKeys(ctx, req.Msg))
}

func (a connectAuthAdapter) CreateApiKey(ctx context.Context, req *connect.Request[corev1.CreateApiKeyRequest]) (*connect.Response[corev1.CreateApiKeyResponse], error) {
	return connectResponse(a.svc.CreateApiKey(ctx, req.Msg))
}

func (a connectAuthAdapter) RevokeApiKey(ctx context.Context, req *connect.Request[corev1.RevokeApiKeyRequest]) (*connect.Response[corev1.RevokeApiKeyResponse], error) {
	return connectResponse(a.svc.RevokeApiKey(ctx, req.Msg))
}

func (a connectAuthAdapter) ListOwnedAgents(ctx context.Context, req *connect.Request[corev1.ListOwnedAgentsRequest]) (*connect.Response[corev1.ListOwnedAgentsResponse], error) {
	return connectResponse(a.svc.ListOwnedAgents(ctx, req.Msg))
}

func (a connectAuthAdapter) CreateDelegatedAgent(ctx context.Context, req *connect.Request[corev1.CreateDelegatedAgentRequest]) (*connect.Response[corev1.CreateDelegatedAgentResponse], error) {
	return connectResponse(a.svc.CreateDelegatedAgent(ctx, req.Msg))
}

func (a connectAuthAdapter) CreateGroup(ctx context.Context, req *connect.Request[corev1.CreateGroupRequest]) (*connect.Response[corev1.AccountGroup], error) {
	return connectResponse(a.svc.CreateGroup(ctx, req.Msg))
}

func (a connectAuthAdapter) GetGroup(ctx context.Context, req *connect.Request[corev1.GetGroupRequest]) (*connect.Response[corev1.AccountGroup], error) {
	return connectResponse(a.svc.GetGroup(ctx, req.Msg))
}

func (a connectAuthAdapter) ListGroups(ctx context.Context, req *connect.Request[corev1.ListGroupsRequest]) (*connect.Response[corev1.ListGroupsResponse], error) {
	return connectResponse(a.svc.ListGroups(ctx, req.Msg))
}

func (a connectAuthAdapter) DeleteGroup(ctx context.Context, req *connect.Request[corev1.DeleteGroupRequest]) (*connect.Response[corev1.Empty], error) {
	return connectResponse(a.svc.DeleteGroup(ctx, req.Msg))
}

func (a connectAuthAdapter) AddGroupMember(ctx context.Context, req *connect.Request[corev1.AddGroupMemberRequest]) (*connect.Response[corev1.AccountGroup], error) {
	return connectResponse(a.svc.AddGroupMember(ctx, req.Msg))
}

func (a connectAuthAdapter) RemoveGroupMember(ctx context.Context, req *connect.Request[corev1.RemoveGroupMemberRequest]) (*connect.Response[corev1.AccountGroup], error) {
	return connectResponse(a.svc.RemoveGroupMember(ctx, req.Msg))
}

func (a connectAuthAdapter) CreateOrganization(ctx context.Context, req *connect.Request[corev1.CreateOrganizationRequest]) (*connect.Response[corev1.CreateOrganizationResponse], error) {
	return connectResponse(a.svc.CreateOrganization(ctx, req.Msg))
}

func (a connectAuthAdapter) ListAccountMembers(ctx context.Context, req *connect.Request[corev1.ListAccountMembersRequest]) (*connect.Response[corev1.ListAccountMembersResponse], error) {
	return connectResponse(a.svc.ListAccountMembers(ctx, req.Msg))
}

func (a connectAuthAdapter) SetAccountMember(ctx context.Context, req *connect.Request[corev1.SetAccountMemberRequest]) (*connect.Response[corev1.SetAccountMemberResponse], error) {
	return connectResponse(a.svc.SetAccountMember(ctx, req.Msg))
}

func (a connectAuthAdapter) RemoveAccountMember(ctx context.Context, req *connect.Request[corev1.RemoveAccountMemberRequest]) (*connect.Response[corev1.RemoveAccountMemberResponse], error) {
	return connectResponse(a.svc.RemoveAccountMember(ctx, req.Msg))
}

func (a connectAuthAdapter) InviteAccountMember(ctx context.Context, req *connect.Request[corev1.InviteAccountMemberRequest]) (*connect.Response[corev1.InviteAccountMemberResponse], error) {
	return connectResponse(a.svc.InviteAccountMember(ctx, req.Msg))
}

func (a connectAuthAdapter) ListAccountInvitations(ctx context.Context, req *connect.Request[corev1.ListAccountInvitationsRequest]) (*connect.Response[corev1.ListAccountInvitationsResponse], error) {
	return connectResponse(a.svc.ListAccountInvitations(ctx, req.Msg))
}

func (a connectAuthAdapter) ListMyInvitations(ctx context.Context, req *connect.Request[corev1.ListMyInvitationsRequest]) (*connect.Response[corev1.ListMyInvitationsResponse], error) {
	return connectResponse(a.svc.ListMyInvitations(ctx, req.Msg))
}

func (a connectAuthAdapter) RespondToInvitation(ctx context.Context, req *connect.Request[corev1.RespondToInvitationRequest]) (*connect.Response[corev1.RespondToInvitationResponse], error) {
	return connectResponse(a.svc.RespondToInvitation(ctx, req.Msg))
}

func (a connectAuthAdapter) CancelAccountInvitation(ctx context.Context, req *connect.Request[corev1.CancelAccountInvitationRequest]) (*connect.Response[corev1.CancelAccountInvitationResponse], error) {
	return connectResponse(a.svc.CancelAccountInvitation(ctx, req.Msg))
}

func (a connectAuthAdapter) GetAccountProfile(ctx context.Context, req *connect.Request[corev1.GetAccountProfileRequest]) (*connect.Response[corev1.AccountProfile], error) {
	return connectResponse(a.svc.GetAccountProfile(ctx, req.Msg))
}

func (a connectAuthAdapter) SetAccountAvatar(ctx context.Context, req *connect.Request[corev1.SetAccountAvatarRequest]) (*connect.Response[corev1.AccountProfile], error) {
	return connectResponse(a.svc.SetAccountAvatar(ctx, req.Msg))
}

func (a connectAuthAdapter) UpdateAccountProfile(ctx context.Context, req *connect.Request[corev1.UpdateAccountProfileRequest]) (*connect.Response[corev1.AccountProfile], error) {
	return connectResponse(a.svc.UpdateAccountProfile(ctx, req.Msg))
}

func (a connectAuthAdapter) RegisterAgent(ctx context.Context, req *connect.Request[corev1.RegisterAgentRequest]) (*connect.Response[corev1.RegisterAgentResponse], error) {
	return connectResponse(a.svc.RegisterAgent(ctx, req.Msg))
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

func (a connectRepositoryAdapter) RenderFile(ctx context.Context, req *connect.Request[corev1.RenderFileRequest]) (*connect.Response[corev1.RenderFileResponse], error) {
	return connectResponse(a.svc.RenderFile(ctx, req.Msg))
}

func (a connectRepositoryAdapter) ReadFiles(ctx context.Context, req *connect.Request[corev1.ReadFilesRequest]) (*connect.Response[corev1.ReadFilesResponse], error) {
	return connectResponse(a.svc.ReadFiles(ctx, req.Msg))
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

func (a connectRepositoryAdapter) BlameFile(ctx context.Context, req *connect.Request[corev1.BlameFileRequest]) (*connect.Response[corev1.BlameFileResponse], error) {
	return connectResponse(a.svc.BlameFile(ctx, req.Msg))
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

func (a connectRepositoryAdapter) Checkout(ctx context.Context, req *connect.Request[corev1.CheckoutRequest], stream *connect.ServerStream[corev1.CheckoutResponse]) error {
	return connectError(a.svc.Checkout(req.Msg, connectRepositoryCheckoutStream{ctx: ctx, stream: stream}))
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

func (a connectBlobAdapter) UploadBlobs(ctx context.Context, req *connect.Request[corev1.UploadBlobsRequest]) (*connect.Response[corev1.UploadBlobsResponse], error) {
	return connectResponse(a.svc.UploadBlobs(ctx, req.Msg))
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

func (a connectSliceAdapter) CreateTag(ctx context.Context, req *connect.Request[corev1.CreateTagRequest]) (*connect.Response[corev1.Tag], error) {
	return connectResponse(a.svc.CreateTag(ctx, req.Msg))
}

func (a connectSliceAdapter) ListTags(ctx context.Context, req *connect.Request[corev1.ListTagsRequest]) (*connect.Response[corev1.ListTagsResponse], error) {
	return connectResponse(a.svc.ListTags(ctx, req.Msg))
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

func (a connectSliceAdapter) RenameSlice(ctx context.Context, req *connect.Request[corev1.RenameSliceRequest]) (*connect.Response[corev1.Slice], error) {
	return connectResponse(a.svc.RenameSlice(ctx, req.Msg))
}

func (a connectSliceAdapter) SetSliceArchived(ctx context.Context, req *connect.Request[corev1.SetSliceArchivedRequest]) (*connect.Response[corev1.Slice], error) {
	return connectResponse(a.svc.SetSliceArchived(ctx, req.Msg))
}

func (a connectSliceAdapter) SetSliceDescription(ctx context.Context, req *connect.Request[corev1.SetSliceDescriptionRequest]) (*connect.Response[corev1.Slice], error) {
	return connectResponse(a.svc.SetSliceDescription(ctx, req.Msg))
}

func (a connectSliceAdapter) GetSliceGitMirror(ctx context.Context, req *connect.Request[corev1.GetSliceGitMirrorRequest]) (*connect.Response[corev1.SliceGitMirror], error) {
	return connectResponse(a.svc.GetSliceGitMirror(ctx, req.Msg))
}

func (a connectSliceAdapter) SetSliceGitMirror(ctx context.Context, req *connect.Request[corev1.SetSliceGitMirrorRequest]) (*connect.Response[corev1.SliceGitMirror], error) {
	return connectResponse(a.svc.SetSliceGitMirror(ctx, req.Msg))
}

func (a connectSliceAdapter) DeleteSliceGitMirror(ctx context.Context, req *connect.Request[corev1.DeleteSliceGitMirrorRequest]) (*connect.Response[corev1.Empty], error) {
	return connectResponse(a.svc.DeleteSliceGitMirror(ctx, req.Msg))
}

func (a connectSliceAdapter) PushSliceGitMirror(ctx context.Context, req *connect.Request[corev1.PushSliceGitMirrorRequest]) (*connect.Response[corev1.SliceGitMirror], error) {
	return connectResponse(a.svc.PushSliceGitMirror(ctx, req.Msg))
}

func (a connectSliceAdapter) SetSliceMergeOnSubmit(ctx context.Context, req *connect.Request[corev1.SetSliceMergeOnSubmitRequest]) (*connect.Response[corev1.Slice], error) {
	return connectResponse(a.svc.SetSliceMergeOnSubmit(ctx, req.Msg))
}

func (a connectSliceAdapter) SetSliceCIDaemon(ctx context.Context, req *connect.Request[corev1.SetSliceCIDaemonRequest]) (*connect.Response[corev1.Slice], error) {
	return connectResponse(a.svc.SetSliceCIDaemon(ctx, req.Msg))
}

func (a connectSliceAdapter) SetSliceSecret(ctx context.Context, req *connect.Request[corev1.SetSliceSecretRequest]) (*connect.Response[corev1.Empty], error) {
	return connectResponse(a.svc.SetSliceSecret(ctx, req.Msg))
}

func (a connectSliceAdapter) DeleteSliceSecret(ctx context.Context, req *connect.Request[corev1.DeleteSliceSecretRequest]) (*connect.Response[corev1.Empty], error) {
	return connectResponse(a.svc.DeleteSliceSecret(ctx, req.Msg))
}

func (a connectSliceAdapter) ListSliceSecrets(ctx context.Context, req *connect.Request[corev1.ListSliceSecretsRequest]) (*connect.Response[corev1.ListSliceSecretsResponse], error) {
	return connectResponse(a.svc.ListSliceSecrets(ctx, req.Msg))
}

func (a connectSliceAdapter) DeleteSlice(ctx context.Context, req *connect.Request[corev1.DeleteSliceRequest]) (*connect.Response[corev1.DeleteSliceResponse], error) {
	return connectResponse(a.svc.DeleteSlice(ctx, req.Msg))
}

func (a connectSliceAdapter) ListSliceMembers(ctx context.Context, req *connect.Request[corev1.ListSliceMembersRequest]) (*connect.Response[corev1.ListSliceMembersResponse], error) {
	return connectResponse(a.svc.ListSliceMembers(ctx, req.Msg))
}

func (a connectSliceAdapter) SetSliceMember(ctx context.Context, req *connect.Request[corev1.SetSliceMemberRequest]) (*connect.Response[corev1.SliceMember], error) {
	return connectResponse(a.svc.SetSliceMember(ctx, req.Msg))
}

func (a connectSliceAdapter) RemoveSliceMember(ctx context.Context, req *connect.Request[corev1.RemoveSliceMemberRequest]) (*connect.Response[corev1.Empty], error) {
	return connectResponse(a.svc.RemoveSliceMember(ctx, req.Msg))
}

func (a connectSliceAdapter) ListSharedSlices(ctx context.Context, req *connect.Request[corev1.ListSharedSlicesRequest]) (*connect.Response[corev1.ListSharedSlicesResponse], error) {
	return connectResponse(a.svc.ListSharedSlices(ctx, req.Msg))
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

func (a connectChangesetAdapter) UpdateChangesetInfo(ctx context.Context, req *connect.Request[corev1.UpdateChangesetInfoRequest]) (*connect.Response[corev1.Changeset], error) {
	return connectResponse(a.svc.UpdateChangesetInfo(ctx, req.Msg))
}

func (a connectChangesetAdapter) AddChangesetCollaborator(ctx context.Context, req *connect.Request[corev1.AddChangesetCollaboratorRequest]) (*connect.Response[corev1.Changeset], error) {
	return connectResponse(a.svc.AddChangesetCollaborator(ctx, req.Msg))
}

func (a connectChangesetAdapter) RemoveChangesetCollaborator(ctx context.Context, req *connect.Request[corev1.RemoveChangesetCollaboratorRequest]) (*connect.Response[corev1.Changeset], error) {
	return connectResponse(a.svc.RemoveChangesetCollaborator(ctx, req.Msg))
}

func (a connectChangesetAdapter) HandoffChangeset(ctx context.Context, req *connect.Request[corev1.HandoffChangesetRequest]) (*connect.Response[corev1.Changeset], error) {
	return connectResponse(a.svc.HandoffChangeset(ctx, req.Msg))
}

func (a connectChangesetAdapter) CopyPath(ctx context.Context, req *connect.Request[corev1.CopyPathRequest]) (*connect.Response[corev1.PathOperationResponse], error) {
	return connectResponse(a.svc.CopyPath(ctx, req.Msg))
}

func (a connectChangesetAdapter) MovePath(ctx context.Context, req *connect.Request[corev1.MovePathRequest]) (*connect.Response[corev1.PathOperationResponse], error) {
	return connectResponse(a.svc.MovePath(ctx, req.Msg))
}

func (a connectChangesetAdapter) CreatePathIntent(ctx context.Context, req *connect.Request[corev1.CreatePathIntentRequest]) (*connect.Response[corev1.PathIntent], error) {
	return connectResponse(a.svc.CreatePathIntent(ctx, req.Msg))
}

func (a connectChangesetAdapter) ListPathIntents(ctx context.Context, req *connect.Request[corev1.ListPathIntentsRequest]) (*connect.Response[corev1.ListPathIntentsResponse], error) {
	return connectResponse(a.svc.ListPathIntents(ctx, req.Msg))
}

func (a connectChangesetAdapter) DeletePathIntent(ctx context.Context, req *connect.Request[corev1.DeletePathIntentRequest]) (*connect.Response[corev1.Empty], error) {
	return connectResponse(a.svc.DeletePathIntent(ctx, req.Msg))
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
