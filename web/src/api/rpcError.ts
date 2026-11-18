// The API's error type and the client's options, apart from the client itself:
// the router and the page shell need these on every page, and the client
// (Connect, protobuf and the service descriptors) is loaded only when a call
// is made (./lazyClient).

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

// isNotFoundError says the server answered that the thing asked for does not
// exist (as opposed to failing, or refusing this caller).
export function isNotFoundError(error: unknown): boolean {
  return (
    error instanceof RpcError &&
    (error.status === 404 ||
      error.code === 5 ||
      error.code === "5" ||
      error.code === "NotFound" ||
      error.code === "not_found")
  );
}

// isUnauthenticatedError says the server refused the call because the caller
// is not signed in or holds no valid session/credential.
export function isUnauthenticatedError(error: unknown): boolean {
  return (
    error instanceof RpcError &&
    (error.status === 401 ||
      error.code === 16 ||
      error.code === "16" ||
      error.code === "Unauthenticated" ||
      error.code === "unauthenticated")
  );
}

// isPermissionDeniedError says the server refused an authenticated caller who
// lacks authority for the requested action.
export function isPermissionDeniedError(error: unknown): boolean {
  return (
    error instanceof RpcError &&
    (error.status === 403 ||
      error.code === 7 ||
      error.code === "7" ||
      error.code === "PermissionDenied" ||
      error.code === "permission_denied")
  );
}

// isDefiniteClientError says retrying the exact same request will not change
// the answer (invalid argument, unauthenticated, permission denied, not found).
export function isDefiniteClientError(error: unknown): boolean {
  if (!(error instanceof RpcError)) {
    return false;
  }
  return (
    error.status === 400 ||
    error.code === 3 ||
    error.code === "3" ||
    error.code === "InvalidArgument" ||
    error.code === "invalid_argument" ||
    isUnauthenticatedError(error) ||
    isPermissionDeniedError(error) ||
    isNotFoundError(error)
  );
}

export const defaultApiBaseUrl = import.meta.env.VITE_API_BASE_URL ?? "";

export interface CreateApiClientOptions {
  getToken: () => Promise<string | null | undefined>;
  baseUrl?: string;
  fetch?: typeof globalThis.fetch;
}
