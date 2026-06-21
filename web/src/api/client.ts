import type { ApiClient } from "./useApi";
import type {
  AbandonChangesetRequest,
  ApproveChangesetRequest,
  ApproveChangesetResponse,
  Changeset,
  ChangesetStack,
  CheckUsernameAvailableRequest,
  CheckUsernameAvailableResponse,
  ChooseUsernameRequest,
  ChooseUsernameResponse,
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
  SendAgentMessageRequest,
  SendAgentMessageResponse,
  Slice,
  StreamConversationRequest,
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

export type RpcService =
  | "AuthService"
  | "RepositoryService"
  | "BlobService"
  | "SliceService"
  | "ChangesetService"
  | "ChangesetStackService"
  | "AgentService";

export interface RpcErrorBody {
  code?: string | number;
  message?: string;
  details?: unknown;
}

export class RpcError extends Error {
  readonly code: string | number;
  readonly status: number;
  readonly details?: unknown;

  constructor(status: number, body: RpcErrorBody = {}) {
    super(body.message || `RPC failed with HTTP ${status}`);
    this.name = "RpcError";
    this.code = body.code ?? status;
    this.status = status;
    this.details = body.details;
  }
}

export const defaultApiBaseUrl = import.meta.env.VITE_API_BASE_URL ?? "";

export interface CreateApiClientOptions {
  getToken: () => Promise<string | null | undefined>;
  baseUrl?: string;
}

export function createApiClient({
  getToken,
  baseUrl = defaultApiBaseUrl
}: CreateApiClientOptions): ApiClient {
  const invoke = async <TRequest, TResponse>(
    service: RpcService,
    method: string,
    request: TRequest
  ) => {
    const token = await getToken();
    return callRpc<TRequest, TResponse>(
      service,
      method,
      request,
      token,
      baseUrl
    );
  };

  return {
    getAuthStatus: (request) =>
      invoke<GetAuthStatusRequest, GetAuthStatusResponse>(
        "AuthService",
        "GetAuthStatus",
        request
      ),
    checkUsernameAvailable: (request) =>
      invoke<CheckUsernameAvailableRequest, CheckUsernameAvailableResponse>(
        "AuthService",
        "CheckUsernameAvailable",
        request
      ),
    chooseUsername: (request) =>
      invoke<ChooseUsernameRequest, ChooseUsernameResponse>(
        "AuthService",
        "ChooseUsername",
        request
      ),
    completeCliLogin: (request) =>
      invoke<CompleteCliLoginRequest, CompleteCliLoginResponse>(
        "AuthService",
        "CompleteCliLogin",
        request
      ),
    resolvePath: (request) =>
      invoke<ResolvePathRequest, ResolvePathResponse>(
        "RepositoryService",
        "ResolvePath",
        request
      ),
    listDirectory: (request) =>
      invoke<ListDirectoryRequest, ListDirectoryResponse>(
        "RepositoryService",
        "ListDirectory",
        request
      ),
    readFile: (request) =>
      invoke<ReadFileRequest, ReadFileResponse>(
        "RepositoryService",
        "ReadFile",
        request
      ),
    getCommit: (request) =>
      invoke<GetCommitRequest, Commit>(
        "RepositoryService",
        "GetCommit",
        request
      ),
    listCommits: (request) =>
      invoke<ListCommitsRequest, ListCommitsResponse>(
        "RepositoryService",
        "ListCommits",
        request
      ),
    getRef: (request) =>
      invoke<GetRefRequest, Ref>("RepositoryService", "GetRef", request),
    getBlobStatus: (request) =>
      invoke<GetBlobStatusRequest, GetBlobStatusResponse>(
        "BlobService",
        "GetBlobStatus",
        request
      ),
    uploadBlob: (request) =>
      invoke<UploadBlobRequest, UploadBlobResponse>(
        "BlobService",
        "UploadBlob",
        request
      ),
    resolveSlice: (request) =>
      invoke<ResolveSliceRequest, Slice>(
        "SliceService",
        "ResolveSlice",
        request
      ),
    getSlice: (request) =>
      invoke<GetSliceRequest, Slice>("SliceService", "GetSlice", request),
    listSlices: (request) =>
      invoke<ListSlicesRequest, ListSlicesResponse>(
        "SliceService",
        "ListSlices",
        request
      ),
    createSlice: (request) =>
      invoke<CreateSliceRequest, Slice>(
        "SliceService",
        "CreateSlice",
        request
      ),
    updateSliceDefinition: (request) =>
      invoke<UpdateSliceDefinitionRequest, SliceDefinition>(
        "SliceService",
        "UpdateSliceDefinition",
        request
      ),
    createChangeset: (request) =>
      invoke<CreateChangesetRequest, Changeset>(
        "ChangesetService",
        "CreateChangeset",
        request
      ),
    listChangesets: (request) =>
      invoke<ListChangesetsRequest, ListChangesetsResponse>(
        "ChangesetService",
        "ListChangesets",
        request
      ),
    getChangeset: (request) =>
      invoke<GetChangesetRequest, Changeset>(
        "ChangesetService",
        "GetChangeset",
        request
      ),
    diffChangeset: (request) =>
      invoke<DiffChangesetRequest, DiffChangesetResponse>(
        "ChangesetService",
        "DiffChangeset",
        request
      ),
    updateChangeset: (request) =>
      invoke<UpdateChangesetRequest, Patchset>(
        "ChangesetService",
        "UpdateChangeset",
        request
      ),
    approveChangeset: (request) =>
      invoke<ApproveChangesetRequest, ApproveChangesetResponse>(
        "ChangesetService",
        "ApproveChangeset",
        request
      ),
    submitChangeset: (request) =>
      invoke<SubmitChangesetRequest, SubmitChangesetResponse>(
        "ChangesetService",
        "SubmitChangeset",
        request
      ),
    abandonChangeset: (request) =>
      invoke<AbandonChangesetRequest, Empty>(
        "ChangesetService",
        "AbandonChangeset",
        request
      ),
    createStack: (request) =>
      invoke<CreateStackRequest, ChangesetStack>(
        "ChangesetStackService",
        "CreateStack",
        request
      ),
    getStack: (request) =>
      invoke<GetStackRequest, ChangesetStack>(
        "ChangesetStackService",
        "GetStack",
        request
      ),
    listStacks: (request) =>
      invoke<ListStacksRequest, ListStacksResponse>(
        "ChangesetStackService",
        "ListStacks",
        request
      ),
    addStackEntry: (request) =>
      invoke<AddStackEntryRequest, Changeset>(
        "ChangesetStackService",
        "AddStackEntry",
        request
      ),
    moveStackEntry: (request) =>
      invoke<MoveStackEntryRequest, ChangesetStack>(
        "ChangesetStackService",
        "MoveStackEntry",
        request
      ),
    reparentStackEntry: (request) =>
      invoke<ReparentStackEntryRequest, ChangesetStack>(
        "ChangesetStackService",
        "ReparentStackEntry",
        request
      ),
    detachStackEntry: (request) =>
      invoke<DetachStackEntryRequest, DetachStackEntryResponse>(
        "ChangesetStackService",
        "DetachStackEntry",
        request
      ),
    restack: (request) =>
      invoke<RestackRequest, RestackResponse>(
        "ChangesetStackService",
        "Restack",
        request
      ),
    submitStack: (request) =>
      invoke<SubmitStackRequest, SubmitStackResponse>(
        "ChangesetStackService",
        "SubmitStack",
        request
      ),
    listDaemons: (request) =>
      invoke<ListDaemonsRequest, ListDaemonsResponse>(
        "AgentService",
        "ListDaemons",
        request
      ),
    createConversation: (request) =>
      invoke<CreateConversationRequest, Conversation>(
        "AgentService",
        "CreateConversation",
        request
      ),
    listConversations: (request) =>
      invoke<ListConversationsRequest, ListConversationsResponse>(
        "AgentService",
        "ListConversations",
        request
      ),
    getConversation: (request) =>
      invoke<GetConversationRequest, Conversation>(
        "AgentService",
        "GetConversation",
        request
      ),
    sendAgentMessage: (request) =>
      invoke<SendAgentMessageRequest, SendAgentMessageResponse>(
        "AgentService",
        "SendAgentMessage",
        request
      ),
    getConversationEvents: (request) =>
      invoke<GetConversationEventsRequest, GetConversationEventsResponse>(
        "AgentService",
        "GetConversationEvents",
        request
      ),
    streamConversation: async function* (request, signal) {
      const token = await getToken();
      yield* streamConversation(request, token, signal, baseUrl);
    }
  };
}

export async function callRpc<TRequest, TResponse>(
  service: RpcService,
  method: string,
  body: TRequest,
  token: string | null | undefined,
  baseUrl = defaultApiBaseUrl
): Promise<TResponse> {
  const trimmedBaseUrl = baseUrl.replace(/\/+$/, "");
  const response = await fetch(
    `${trimmedBaseUrl}/gitslice.core.v1.${service}/${method}`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        ...(token ? { Authorization: `Bearer ${token}` } : {})
      },
      body: JSON.stringify(body ?? {})
    }
  );

  const text = await response.text();
  const parsed = text ? JSON.parse(text) : {};

  if (!response.ok) {
    throw new RpcError(response.status, parsed as RpcErrorBody);
  }

  return parsed as TResponse;
}

export async function* streamConversation(
  req: StreamConversationRequest,
  token: string | null | undefined,
  signal: AbortSignal,
  baseUrl = defaultApiBaseUrl
): AsyncGenerator<ConversationEvent> {
  const trimmedBaseUrl = baseUrl.replace(/\/+$/, "");
  const response = await fetch(
    `${trimmedBaseUrl}/gitslice.core.v1.AgentService/StreamConversation`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        ...(token ? { Authorization: `Bearer ${token}` } : {})
      },
      body: JSON.stringify(req ?? {}),
      signal
    }
  );

  if (!response.ok) {
    const text = await response.text();
    const parsed = text ? JSON.parse(text) : {};
    throw new RpcError(response.status, parsed as RpcErrorBody);
  }

  if (!response.body) {
    throw new Error("StreamConversation did not return a response body.");
  }

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";

  try {
    while (!signal.aborted) {
      const { done, value } = await reader.read();
      if (done) {
        break;
      }

      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split("\n");
      buffer = lines.pop() ?? "";

      for (const line of lines) {
        if (signal.aborted) {
          return;
        }
        const event = parseConversationStreamLine(line, response.status);
        if (event) {
          yield event;
        }
      }
    }

    if (signal.aborted) {
      return;
    }

    buffer += decoder.decode();
    const event = parseConversationStreamLine(buffer, response.status);
    if (event && !signal.aborted) {
      yield event;
    }
  } finally {
    reader.releaseLock();
  }
}

function parseConversationStreamLine(line: string, status: number) {
  const trimmed = line.trim();
  if (!trimmed) {
    return undefined;
  }

  const parsed = JSON.parse(trimmed) as {
    result?: ConversationEvent;
    error?: RpcErrorBody;
  };

  if (parsed.error) {
    throw new RpcError(status, parsed.error);
  }

  return parsed.result;
}
