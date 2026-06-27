import { ConnectError, Code, createClient, type Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

import { AgentService } from "../gen/proto/core/v1/agent_pb";
import { AuthService } from "../gen/proto/core/v1/auth_pb";
import { BlobService } from "../gen/proto/core/v1/blob_pb";
import {
  ChangesetService,
  ChangesetStackService
} from "../gen/proto/core/v1/changeset_pb";
import { RepositoryService } from "../gen/proto/core/v1/repository_pb";
import { SliceService } from "../gen/proto/core/v1/slice_pb";
import type { ApiClient } from "./useApi";
import type * as Api from "./types";

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
  const transport = createConnectTransport({
    baseUrl: baseUrl.replace(/\/+$/, ""),
    interceptors: [authInterceptor(getToken)]
  });

  const auth = createClient(AuthService, transport);
  const repository = createClient(RepositoryService, transport);
  const blob = createClient(BlobService, transport);
  const slice = createClient(SliceService, transport);
  const changeset = createClient(ChangesetService, transport);
  const stack = createClient(ChangesetStackService, transport);
  const agent = createClient(AgentService, transport);

  const unary = async <TResponse>(call: () => Promise<unknown>) => {
    try {
      return fromProto<TResponse>(await call());
    } catch (error) {
      throw rpcErrorFrom(error);
    }
  };

  return {
    getAuthStatus: (request) =>
      unary<Api.GetAuthStatusResponse>(() =>
        auth.getAuthStatus(toProtoRequest(request))
      ),
    checkUsernameAvailable: (request) =>
      unary<Api.CheckUsernameAvailableResponse>(() =>
        auth.checkUsernameAvailable(toProtoRequest(request))
      ),
    chooseUsername: (request) =>
      unary<Api.ChooseUsernameResponse>(() =>
        auth.chooseUsername(toProtoRequest(request))
      ),
    completeCliLogin: (request) =>
      unary<Api.CompleteCliLoginResponse>(() =>
        auth.completeCliLogin(toProtoRequest(request))
      ),
    resolvePath: (request) =>
      unary<Api.ResolvePathResponse>(() =>
        repository.resolvePath(toProtoRequest(request))
      ),
    listDirectory: (request) =>
      unary<Api.ListDirectoryResponse>(() =>
        repository.listDirectory(toProtoRequest(request))
      ),
    readFile: (request) =>
      unary<Api.ReadFileResponse>(() => repository.readFile(toProtoRequest(request))),
    getCommit: (request) =>
      unary<Api.Commit>(() => repository.getCommit(toProtoRequest(request))),
    listCommits: (request) =>
      unary<Api.ListCommitsResponse>(() =>
        repository.listCommits(toProtoRequest(request))
      ),
    getRef: (request) =>
      unary<Api.Ref>(() => repository.getRef(toProtoRequest(request))),
    getBlobStatus: (request) =>
      unary<Api.GetBlobStatusResponse>(() =>
        blob.getBlobStatus(toProtoRequest(request))
      ),
    uploadBlob: (request) =>
      unary<Api.UploadBlobResponse>(() => blob.uploadBlob(toProtoRequest(request))),
    resolveSlice: (request) =>
      unary<Api.Slice>(() => slice.resolveSlice(toProtoRequest(request))),
    getSlice: (request) =>
      unary<Api.Slice>(() => slice.getSlice(toProtoRequest(request))),
    listSlices: (request) =>
      unary<Api.ListSlicesResponse>(() => slice.listSlices(toProtoRequest(request))),
    createSlice: (request) =>
      unary<Api.Slice>(() => slice.createSlice(toProtoRequest(request))),
    updateSliceDefinition: (request) =>
      unary<Api.SliceDefinition>(() =>
        slice.updateSliceDefinition(toProtoRequest(request))
      ),
    createChangeset: (request) =>
      unary<Api.Changeset>(() =>
        changeset.createChangeset(toProtoRequest(request))
      ),
    listChangesets: (request) =>
      unary<Api.ListChangesetsResponse>(() =>
        changeset.listChangesets(toProtoRequest(request))
      ),
    getChangeset: (request) =>
      unary<Api.Changeset>(() => changeset.getChangeset(toProtoRequest(request))),
    diffChangeset: (request) =>
      unary<Api.DiffChangesetResponse>(() =>
        changeset.diffChangeset(toProtoRequest(request))
      ),
    updateChangeset: (request) =>
      unary<Api.Patchset>(() =>
        changeset.updateChangeset(toProtoRequest(request))
      ),
    approveChangeset: (request) =>
      unary<Api.ApproveChangesetResponse>(() =>
        changeset.approveChangeset(toProtoRequest(request))
      ),
    submitChangeset: (request) =>
      unary<Api.SubmitChangesetResponse>(() =>
        changeset.submitChangeset(toProtoRequest(request))
      ),
    abandonChangeset: (request) =>
      unary<Api.Empty>(() => changeset.abandonChangeset(toProtoRequest(request))),
    createStack: (request) =>
      unary<Api.ChangesetStack>(() => stack.createStack(toProtoRequest(request))),
    getStack: (request) =>
      unary<Api.ChangesetStack>(() => stack.getStack(toProtoRequest(request))),
    listStacks: (request) =>
      unary<Api.ListStacksResponse>(() => stack.listStacks(toProtoRequest(request))),
    addStackEntry: (request) =>
      unary<Api.Changeset>(() => stack.addStackEntry(toProtoRequest(request))),
    moveStackEntry: (request) =>
      unary<Api.ChangesetStack>(() =>
        stack.moveStackEntry(toProtoRequest(request))
      ),
    reparentStackEntry: (request) =>
      unary<Api.ChangesetStack>(() =>
        stack.reparentStackEntry(toProtoRequest(request))
      ),
    detachStackEntry: (request) =>
      unary<Api.DetachStackEntryResponse>(() =>
        stack.detachStackEntry(toProtoRequest(request))
      ),
    restack: (request) =>
      unary<Api.RestackResponse>(() => stack.restack(toProtoRequest(request))),
    submitStack: (request) =>
      unary<Api.SubmitStackResponse>(() => stack.submitStack(toProtoRequest(request))),
    listDaemons: (request) =>
      unary<Api.ListDaemonsResponse>(() => agent.listDaemons(toProtoRequest(request))),
    createConversation: (request) =>
      unary<Api.Conversation>(() =>
        agent.createConversation(toProtoRequest(request))
      ),
    listConversations: (request) =>
      unary<Api.ListConversationsResponse>(() =>
        agent.listConversations(toProtoRequest(request))
      ),
    getConversation: (request) =>
      unary<Api.Conversation>(() => agent.getConversation(toProtoRequest(request))),
    closeConversation: (request) =>
      unary<Api.Conversation>(() =>
        agent.closeConversation(toProtoRequest(request))
      ),
    sendAgentMessage: (request) =>
      unary<Api.SendAgentMessageResponse>(() =>
        agent.sendAgentMessage(toProtoRequest(request))
      ),
    getConversationEvents: (request) =>
      unary<Api.GetConversationEventsResponse>(() =>
        agent.getConversationEvents(toProtoRequest(request))
      ),
    streamConversation: async function* (request, signal) {
      try {
        for await (const event of agent.streamConversation(
          toProtoRequest(request),
          { signal }
        )) {
          yield fromProto<Api.ConversationEvent>(event);
        }
      } catch (error) {
        throw rpcErrorFrom(error);
      }
    }
  };
}

function authInterceptor(
  getToken: () => Promise<string | null | undefined>
): Interceptor {
  return (next) => async (req) => {
    const token = await getToken();
    if (token) {
      req.header.set("Authorization", `Bearer ${token}`);
    }
    return next(req);
  };
}

const int64FieldNames = new Set([
  "afterSeq",
  "ackedClientSeq",
  "authoringConversationSeq",
  "beforeSeq",
  "clientSeq",
  "current",
  "currentPatchsetNumber",
  "depth",
  "displayOrder",
  "length",
  "number",
  "offset",
  "seq",
  "siblingOrder",
  "size",
  "stackDepth",
  "stackOrder",
  "total",
  "version"
]);

const bytesFieldNames = new Set(["data"]);

function toProtoRequest(value: unknown): any {
  return toProtoValue("", value);
}

function toProtoValue(key: string, value: unknown): unknown {
  if (value === undefined || value === null) {
    return undefined;
  }
  if (int64FieldNames.has(key)) {
    return toBigInt(value);
  }
  if (bytesFieldNames.has(key)) {
    return toBytes(value);
  }
  if (Array.isArray(value)) {
    return value.map((item) => toProtoValue("", item));
  }
  if (typeof value === "object") {
    if (value instanceof Uint8Array) {
      return value;
    }
    const out: Record<string, unknown> = {};
    for (const [childKey, childValue] of Object.entries(
      value as Record<string, unknown>
    )) {
      const converted = toProtoValue(childKey, childValue);
      if (converted !== undefined) {
        out[childKey] = converted;
      }
    }
    return out;
  }
  return value;
}

function fromProto<T>(value: unknown): T {
  return fromProtoValue("", value) as T;
}

function fromProtoValue(key: string, value: unknown): unknown {
  if (typeof value === "bigint") {
    return value.toString();
  }
  if (value instanceof Uint8Array) {
    return bytesToBase64(value);
  }
  if (key === "kind" && typeof value === "number") {
    return entryKindName(value);
  }
  if (Array.isArray(value)) {
    return value.map((item) => fromProtoValue("", item));
  }
  if (value && typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const [childKey, childValue] of Object.entries(
      value as Record<string, unknown>
    )) {
      if (childKey === "$typeName" || childKey === "$unknown") {
        continue;
      }
      out[childKey] = fromProtoValue(childKey, childValue);
    }
    return out;
  }
  return value;
}

function toBigInt(value: unknown): bigint | undefined {
  if (typeof value === "bigint") {
    return value;
  }
  if (typeof value === "number") {
    return BigInt(Math.trunc(value));
  }
  if (typeof value === "string") {
    const trimmed = value.trim();
    return trimmed ? BigInt(trimmed) : undefined;
  }
  return undefined;
}

function toBytes(value: unknown): Uint8Array | undefined {
  if (value instanceof Uint8Array) {
    return value;
  }
  if (typeof value !== "string") {
    return undefined;
  }
  const binary = globalThis.atob(value);
  return Uint8Array.from(binary, (char) => char.charCodeAt(0));
}

function bytesToBase64(bytes: Uint8Array) {
  let binary = "";
  const chunkSize = 0x8000;
  for (let index = 0; index < bytes.length; index += chunkSize) {
    const chunk = bytes.subarray(index, index + chunkSize);
    binary += String.fromCharCode(...chunk);
  }
  return globalThis.btoa(binary);
}

function entryKindName(value: number): Api.EntryKind {
  switch (value) {
    case 1:
      return "ENTRY_KIND_FILE";
    case 2:
      return "ENTRY_KIND_DIRECTORY";
    case 3:
      return "ENTRY_KIND_SYMLINK";
    default:
      return "ENTRY_KIND_UNSPECIFIED";
  }
}

function rpcErrorFrom(error: unknown): RpcError {
  const connectError = ConnectError.from(error);
  return new RpcError(httpStatusForCode(connectError.code), {
    code: connectError.code,
    message: connectError.rawMessage || connectError.message,
    details: connectError.details
  });
}

function httpStatusForCode(code: Code) {
  switch (code) {
    case Code.Canceled:
      return 499;
    case Code.InvalidArgument:
    case Code.FailedPrecondition:
    case Code.OutOfRange:
      return 400;
    case Code.Unauthenticated:
      return 401;
    case Code.PermissionDenied:
      return 403;
    case Code.NotFound:
      return 404;
    case Code.AlreadyExists:
      return 409;
    case Code.ResourceExhausted:
      return 429;
    case Code.Unimplemented:
      return 501;
    case Code.Unavailable:
      return 503;
    default:
      return 500;
  }
}
