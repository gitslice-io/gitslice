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

export const defaultApiBaseUrl = import.meta.env.VITE_API_BASE_URL ?? "";

export interface CreateApiClientOptions {
  getToken: () => Promise<string | null | undefined>;
  baseUrl?: string;
  fetch?: typeof globalThis.fetch;
}
