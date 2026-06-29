import { useAuth } from "@clerk/tanstack-react-start";
import { useCallback, useMemo } from "react";

import { createApiClient, defaultApiBaseUrl } from "./client";
import type {
  AbandonChangesetRequest,
  ApproveChangesetRequest,
  ApproveChangesetResponse,
  Changeset,
  ChangesetStack,
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
    request: DiffChangesetRequest
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
