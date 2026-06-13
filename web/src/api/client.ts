export type RpcService =
  | "AuthService"
  | "RepositoryService"
  | "BlobService"
  | "SliceService"
  | "ChangesetService";

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
