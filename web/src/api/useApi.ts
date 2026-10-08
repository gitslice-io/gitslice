import { useAuth } from "@clerk/tanstack-react-start";
import { useCallback, useMemo } from "react";

import { createApiClient, defaultApiBaseUrl } from "./client";
import type {
  AbandonChangesetRequest,
  ApproveChangesetRequest,
  ApproveChangesetResponse,
  Changeset,
  ChangesetStack,
  AcceptClaimRequest,
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
  CreateWebhookRequest,
  ListWebhooksRequest,
  ListWebhooksResponse,
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
  ReadFileRequest,
  ReadFileResponse,
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
  Slice,
  StreamConversationRequest,
  StreamCheckRunRequest,
  SubmitStackRequest,
  SubmitStackResponse,
  SubmitChangesetRequest,
  SubmitChangesetResponse,
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
  resolvePath(request: ResolvePathRequest): Promise<ResolvePathResponse>;
  listDirectory(
    request: ListDirectoryRequest
  ): Promise<ListDirectoryResponse>;
  readFile(request: ReadFileRequest): Promise<ReadFileResponse>;
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
  abandonChangeset(request: AbandonChangesetRequest): Promise<Empty>;
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
  createWebhook(request: CreateWebhookRequest): Promise<Webhook>;
  listWebhooks(request: ListWebhooksRequest): Promise<ListWebhooksResponse>;
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
