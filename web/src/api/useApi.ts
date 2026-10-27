import { useAuth } from "@clerk/tanstack-react-start";
import { useCallback, useMemo } from "react";

import { createApiClient, defaultApiBaseUrl } from "./client";
import type {
  AbandonChangesetRequest,
  AgentDaemon,
  ApproveChangesetRequest,
  ApproveChangesetResponse,
  Changeset,
  ChangesetStack,
  AcceptClaimRequest,
  CopyPathRequest,
  MovePathRequest,
  PathOperationResponse,
  AcceptClaimResponse,
  CheckUsernameAvailableRequest,
  CheckUsernameAvailableResponse,
  CheckRun,
  CheckRunLog,
  ChooseUsernameRequest,
  ChooseUsernameResponse,
  CloseConversationRequest,
  Commit,
  CompleteCliLoginRequest,
  CompleteCliLoginResponse,
  AddStackEntryRequest,
  Conversation,
  ConversationEvent,
  CreateConversationRequest,
  CreateChangesetRequest,
  CreateStackRequest,
  CreateSliceRequest,
  DetachStackEntryRequest,
  DetachStackEntryResponse,
  DiffChangesetRequest,
  DiffChangesetResponse,
  Empty,
  GetConversationRequest,
  GetConversationEventsRequest,
  GetConversationEventsResponse,
  GetAuthStatusRequest,
  ListOwnedAgentsRequest,
  ListOwnedAgentsResponse,
  ListAccountMembersRequest,
  ListAccountMembersResponse,
  SetAccountMemberRequest,
  SetAccountMemberResponse,
  RemoveAccountMemberRequest,
  RemoveAccountMemberResponse,
  AccountProfile,
  CancelAccountInvitationRequest,
  CreateOrganizationRequest,
  CreateOrganizationResponse,
  GetAccountProfileRequest,
  InviteAccountMemberRequest,
  InviteAccountMemberResponse,
  ListAccountInvitationsRequest,
  ListInvitationsResponse,
  ListMyInvitationsRequest,
  RespondToInvitationRequest,
  RespondToInvitationResponse,
  SetAccountAvatarRequest,
  UpdateAccountProfileRequest,
  ListPendingClaimsRequest,
  ListPendingClaimsResponse,
  GetAuthStatusResponse,
  GetBlobStatusRequest,
  GetBlobStatusResponse,
  GetCheckRunRequest,
  GetChangesetRequest,
  GetCommitRequest,
  GetRefRequest,
  GetSliceRequest,
  GetStackRequest,
  ListDaemonsRequest,
  ListDaemonsResponse,
  RenameDaemonRequest,
  ExposeDaemonRequest,
  UnexposeDaemonRequest,
  CreateWebhookRequest,
  CreateSecretRequest,
  ListSecretsRequest,
  ListSecretsResponse,
  ListSliceSecretsRequest,
  ListSliceSecretsResponse,
  Secret,
  SecretIdRequest,
  UpdateSecretRequest,
  ListWebhooksRequest,
  ListWebhooksResponse,
  ListSliceMembersRequest,
  ListSliceMembersResponse,
  SetSliceMemberRequest,
  RemoveSliceMemberRequest,
  SliceMember,
  ListSharedSlicesRequest,
  ListSharedSlicesResponse,
  UpdateWebhookRequest,
  Webhook,
  WebhookDelivery,
  WebhookIdRequest,
  ListWebhookDeliveriesRequest,
  ListWebhookDeliveriesResponse,
  RedeliverWebhookDeliveryRequest,
  ListCommitsRequest,
  ListCommitsResponse,
  ListChangesetsRequest,
  ListChangesetsResponse,
  ListCheckRunsRequest,
  ListCheckRunsResponse,
  ListConversationsRequest,
  ListConversationsResponse,
  ListDirectoryRequest,
  ListDirectoryResponse,
  ListSlicesRequest,
  ListSlicesResponse,
  ListStacksRequest,
  ListStacksResponse,
  MoveStackEntryRequest,
  Patchset,
  BlameFileRequest,
  BlameFileResponse,
  ReadFileRequest,
  ReadFileResponse,
  RenderFileRequest,
  RenderFileResponse,
  ReparentStackEntryRequest,
  Ref,
  RestackRequest,
  RestackResponse,
  ResolvePathRequest,
  ResolvePathResponse,
  ResolveSliceRequest,
  RerunCheckRequest,
  SendAgentMessageRequest,
  SendAgentMessageResponse,
  SetSliceCIDaemonRequest,
  SetSliceDescriptionRequest,
  SetSliceMergeOnSubmitRequest,
  Slice,
  StreamConversationRequest,
  StreamCheckRunRequest,
  SubmitStackRequest,
  SubmitStackResponse,
  SubmitChangesetRequest,
  SubmitChangesetResponse,
  UpdateChangesetInfoRequest,
  UpdateChangesetRequest,
  UpdateSliceDefinitionRequest,
  UploadBlobRequest,
  UploadBlobResponse,
  SliceDefinition
} from "./types";

export interface ApiClient {
  getAuthStatus(
    request: GetAuthStatusRequest
  ): Promise<GetAuthStatusResponse>;
  checkUsernameAvailable(
    request: CheckUsernameAvailableRequest
  ): Promise<CheckUsernameAvailableResponse>;
  chooseUsername(
    request: ChooseUsernameRequest
  ): Promise<ChooseUsernameResponse>;
  completeCliLogin(
    request: CompleteCliLoginRequest
  ): Promise<CompleteCliLoginResponse>;
  listPendingClaims(
    request: ListPendingClaimsRequest
  ): Promise<ListPendingClaimsResponse>;
  acceptClaim(request: AcceptClaimRequest): Promise<AcceptClaimResponse>;
  listOwnedAgents(
    request: ListOwnedAgentsRequest
  ): Promise<ListOwnedAgentsResponse>;
  listAccountMembers(
    request: ListAccountMembersRequest
  ): Promise<ListAccountMembersResponse>;
  setAccountMember(
    request: SetAccountMemberRequest
  ): Promise<SetAccountMemberResponse>;
  removeAccountMember(
    request: RemoveAccountMemberRequest
  ): Promise<RemoveAccountMemberResponse>;
  createOrganization(
    request: CreateOrganizationRequest
  ): Promise<CreateOrganizationResponse>;
  inviteAccountMember(
    request: InviteAccountMemberRequest
  ): Promise<InviteAccountMemberResponse>;
  listAccountInvitations(
    request: ListAccountInvitationsRequest
  ): Promise<ListInvitationsResponse>;
  listMyInvitations(request: ListMyInvitationsRequest): Promise<ListInvitationsResponse>;
  respondToInvitation(
    request: RespondToInvitationRequest
  ): Promise<RespondToInvitationResponse>;
  cancelAccountInvitation(
    request: CancelAccountInvitationRequest
  ): Promise<Record<string, never>>;
  getAccountProfile(request: GetAccountProfileRequest): Promise<AccountProfile>;
  updateAccountProfile(request: UpdateAccountProfileRequest): Promise<AccountProfile>;
  setAccountAvatar(request: SetAccountAvatarRequest): Promise<AccountProfile>;
  resolvePath(request: ResolvePathRequest): Promise<ResolvePathResponse>;
  listDirectory(
    request: ListDirectoryRequest
  ): Promise<ListDirectoryResponse>;
  readFile(request: ReadFileRequest): Promise<ReadFileResponse>;
  // renderFile is a Markdown file as safe HTML, rendered on the server, so a
  // README can be part of the server-rendered page.
  renderFile(request: RenderFileRequest): Promise<RenderFileResponse>;
  blameFile(request: BlameFileRequest): Promise<BlameFileResponse>;
  getCommit(request: GetCommitRequest): Promise<Commit>;
  listCommits(request: ListCommitsRequest): Promise<ListCommitsResponse>;
  getRef(request: GetRefRequest): Promise<Ref>;
  getBlobStatus(
    request: GetBlobStatusRequest
  ): Promise<GetBlobStatusResponse>;
  uploadBlob(request: UploadBlobRequest): Promise<UploadBlobResponse>;
  resolveSlice(request: ResolveSliceRequest): Promise<Slice>;
  getSlice(request: GetSliceRequest): Promise<Slice>;
  listSlices(request: ListSlicesRequest): Promise<ListSlicesResponse>;
  createSlice(request: CreateSliceRequest): Promise<Slice>;
  updateSliceDefinition(
    request: UpdateSliceDefinitionRequest
  ): Promise<SliceDefinition>;
  setSliceCIDaemon(request: SetSliceCIDaemonRequest): Promise<Slice>;
  setSliceDescription(request: SetSliceDescriptionRequest): Promise<Slice>;
  setSliceMergeOnSubmit(request: SetSliceMergeOnSubmitRequest): Promise<Slice>;
  createChangeset(request: CreateChangesetRequest): Promise<Changeset>;
  listChangesets(
    request: ListChangesetsRequest
  ): Promise<ListChangesetsResponse>;
  getChangeset(request: GetChangesetRequest): Promise<Changeset>;
  diffChangeset(
    request: DiffChangesetRequest & { paths?: string[] }
  ): Promise<DiffChangesetResponse>;
  updateChangeset(request: UpdateChangesetRequest): Promise<Patchset>;
  approveChangeset(
    request: ApproveChangesetRequest
  ): Promise<ApproveChangesetResponse>;
  submitChangeset(
    request: SubmitChangesetRequest
  ): Promise<SubmitChangesetResponse>;
  // updateChangesetInfo changes the title and/or description of a draft
  // changeset; a field left out is kept.
  updateChangesetInfo(request: UpdateChangesetInfoRequest): Promise<Changeset>;
  abandonChangeset(request: AbandonChangesetRequest): Promise<Empty>;
  // copyPath and movePath land a copy or move at once, as their own
  // changeset, without the slice's required approvals and checks.
  copyPath(request: CopyPathRequest): Promise<PathOperationResponse>;
  movePath(request: MovePathRequest): Promise<PathOperationResponse>;
  listCheckRuns(request: ListCheckRunsRequest): Promise<ListCheckRunsResponse>;
  getCheckRun(request: GetCheckRunRequest): Promise<CheckRun>;
  rerunCheck(request: RerunCheckRequest): Promise<CheckRun>;
  streamCheckRun(
    request: StreamCheckRunRequest,
    signal: AbortSignal
  ): AsyncGenerator<CheckRunLog>;
  createStack(request: CreateStackRequest): Promise<ChangesetStack>;
  getStack(request: GetStackRequest): Promise<ChangesetStack>;
  listStacks(request: ListStacksRequest): Promise<ListStacksResponse>;
  addStackEntry(request: AddStackEntryRequest): Promise<Changeset>;
  moveStackEntry(request: MoveStackEntryRequest): Promise<ChangesetStack>;
  reparentStackEntry(
    request: ReparentStackEntryRequest
  ): Promise<ChangesetStack>;
  detachStackEntry(
    request: DetachStackEntryRequest
  ): Promise<DetachStackEntryResponse>;
  restack(request: RestackRequest): Promise<RestackResponse>;
  submitStack(request: SubmitStackRequest): Promise<SubmitStackResponse>;
  listSecrets(request: ListSecretsRequest): Promise<ListSecretsResponse>;
  createSecret(request: CreateSecretRequest): Promise<Secret>;
  updateSecret(request: UpdateSecretRequest): Promise<Secret>;
  deleteSecret(request: SecretIdRequest): Promise<Empty>;
  listSliceSecrets(request: ListSliceSecretsRequest): Promise<ListSliceSecretsResponse>;
  createWebhook(request: CreateWebhookRequest): Promise<Webhook>;
  listWebhooks(request: ListWebhooksRequest): Promise<ListWebhooksResponse>;
  listSliceMembers(request: ListSliceMembersRequest): Promise<ListSliceMembersResponse>;
  setSliceMember(request: SetSliceMemberRequest): Promise<SliceMember>;
  removeSliceMember(request: RemoveSliceMemberRequest): Promise<Empty>;
  listSharedSlices(request: ListSharedSlicesRequest): Promise<ListSharedSlicesResponse>;
  updateWebhook(request: UpdateWebhookRequest): Promise<Webhook>;
  deleteWebhook(request: WebhookIdRequest): Promise<Empty>;
  pingWebhook(request: WebhookIdRequest): Promise<WebhookDelivery>;
  listWebhookDeliveries(
    request: ListWebhookDeliveriesRequest
  ): Promise<ListWebhookDeliveriesResponse>;
  redeliverWebhookDelivery(
    request: RedeliverWebhookDeliveryRequest
  ): Promise<WebhookDelivery>;
  listDaemons(request: ListDaemonsRequest): Promise<ListDaemonsResponse>;
  renameDaemon(request: RenameDaemonRequest): Promise<AgentDaemon>;
  exposeDaemon(request: ExposeDaemonRequest): Promise<AgentDaemon>;
  unexposeDaemon(request: UnexposeDaemonRequest): Promise<AgentDaemon>;
  createConversation(request: CreateConversationRequest): Promise<Conversation>;
  listConversations(
    request: ListConversationsRequest
  ): Promise<ListConversationsResponse>;
  getConversation(request: GetConversationRequest): Promise<Conversation>;
  closeConversation(request: CloseConversationRequest): Promise<Conversation>;
  sendAgentMessage(
    request: SendAgentMessageRequest
  ): Promise<SendAgentMessageResponse>;
  getConversationEvents(
    request: GetConversationEventsRequest
  ): Promise<GetConversationEventsResponse>;
  streamConversation(
    request: StreamConversationRequest,
    signal: AbortSignal
  ): AsyncGenerator<ConversationEvent>;
}

export function useApi(baseUrl = defaultApiBaseUrl): ApiClient {
  const { getToken, isLoaded, isSignedIn } = useAuth();

  const getApiToken = useCallback(
    async () => (isLoaded && isSignedIn ? await getToken() : null),
    [getToken, isLoaded, isSignedIn]
  );

  return useMemo(
    () => createApiClient({ getToken: getApiToken, baseUrl }),
    [baseUrl, getApiToken]
  );
}
