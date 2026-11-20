import { ConnectError, Code, createClient, type Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import type { DescMethod, DescService } from "@bufbuild/protobuf";

import { AgentService } from "../gen/proto/core/v1/agent_pb";
import { AuthService } from "../gen/proto/core/v1/auth_pb";
import { BlobService } from "../gen/proto/core/v1/blob_pb";
import { CheckService } from "../gen/proto/core/v1/check_pb";
import {
  ChangesetService,
  ChangesetStackService
} from "../gen/proto/core/v1/changeset_pb";
import { FeedbackService } from "../gen/proto/core/v1/feedback_pb";
import { RepositoryService } from "../gen/proto/core/v1/repository_pb";
import { SecretService } from "../gen/proto/core/v1/secret_pb";
import { SliceService } from "../gen/proto/core/v1/slice_pb";
import { AutomationService } from "../gen/proto/core/v1/automation_pb";
import type { ApiClient } from "./useApi";
import { decodeMessage, encodeMessage } from "./codec";

import { RpcError, defaultApiBaseUrl, type CreateApiClientOptions } from "./rpcError";

// Kept here for the code that has always imported them from the client. Code
// that every page loads imports them from ./rpcError instead, so it does not
// bring the client with it (see ./lazyClient).
export {
  RpcError,
  defaultApiBaseUrl,
  isDefiniteClientError,
  isNotFoundError,
  isPermissionDeniedError,
  isUnauthenticatedError
} from "./rpcError";
export type { CreateApiClientOptions, RpcErrorBody } from "./rpcError";

// The services a browser calls. AutomationRunService, WorkspaceService and the
// daemon half of AgentService are for the CLI and agents, and share method
// names (readFile, listDirectory, getChangeset) with these.
export const API_SERVICES: readonly DescService[] = [
  AuthService,
  AgentService,
  BlobService,
  CheckService,
  ChangesetService,
  ChangesetStackService,
  FeedbackService,
  RepositoryService,
  SecretService,
  SliceService,
  AutomationService
];

// RPCs that legitimately run longer than the transport's default: a blame of
// a long history, and a submit, copy or move that lands a large changeset.
const METHOD_TIMEOUTS_MS: Record<string, number> = {
  blameFile: 120_000,
  copyPath: 300_000,
  movePath: 300_000,
  submitChangeset: 300_000
};

// A method of the client: a promise for a unary RPC, an async generator (that
// takes a signal) for a server-streaming one. A client- or bidirectional-
// streaming RPC is not offered.
type ClientMethod = (request: unknown, signal?: AbortSignal) => unknown;

// createApiClient builds the client from the generated service descriptors:
// every unary and server-streaming RPC of API_SERVICES is a method named as
// the proto names it, its request and response converted by ./codec from the
// message descriptors. A new RPC is in the client as soon as it is in the
// generated code; ApiClient (./useApi) types the ones the app uses.
export function createApiClient({
  getToken,
  baseUrl = defaultApiBaseUrl,
  fetch: fetchFn
}: CreateApiClientOptions): ApiClient {
  const transport = createConnectTransport({
    baseUrl: baseUrl.replace(/\/+$/, ""),
    interceptors: [authInterceptor(getToken)],
    ...(fetchFn ? { fetch: fetchFn } : {})
  });

  const methods = new Map<string, ClientMethod>();
  for (const service of API_SERVICES) {
    const client = createClient(service, transport) as unknown as Record<string, (...args: unknown[]) => unknown>;
    for (const method of service.methods) {
      if (methods.has(method.localName)) {
        throw new Error(`two services of the API client both have a method ${method.localName}`);
      }
      const call = clientMethod(client, method);
      if (call) {
        methods.set(method.localName, call);
      }
    }
  }
  return Object.fromEntries(methods) as unknown as ApiClient;
}

function clientMethod(client: Record<string, (...args: unknown[]) => unknown>, method: DescMethod): ClientMethod | undefined {
  const invoke = client[method.localName];
  const timeoutMs = METHOD_TIMEOUTS_MS[method.localName];
  const options = (signal?: AbortSignal) => (timeoutMs || signal ? { ...(timeoutMs ? { timeoutMs } : {}), ...(signal ? { signal } : {}) } : undefined);
  switch (method.methodKind) {
    case "unary":
      return async (request) => {
        try {
          return decodeMessage(method.output, await invoke(encodeMessage(method.input, request), options()));
        } catch (error) {
          throw rpcErrorFrom(error);
        }
      };
    case "server_streaming":
      return async function* (request, signal) {
        try {
          for await (const message of invoke(encodeMessage(method.input, request), options(signal)) as AsyncIterable<unknown>) {
            yield decodeMessage(method.output, message);
          }
        } catch (error) {
          throw rpcErrorFrom(error);
        }
      };
    default:
      return undefined;
  }
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
