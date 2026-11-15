// A client for the server's TriggerRunService over Connect's JSON protocol
// (proto/core/v1/trigger.proto).

export class ApiError extends Error {
  readonly code: string;
  readonly status: number;

  constructor(code: string, message: string, status: number) {
    super(message);
    this.code = code;
    this.status = status;
  }
}

export interface TriggerRun {
  id: string;
  account?: string;
  triggerId: string;
  folder?: string;
  status: string;
  eventKind?: string;
}

const scopeEncoder = new TextEncoder();

// triggerScope binds isolate reuse and sealed secret placeholders to a single
// tenant account, folder, and script code hash.
export function triggerScope(start: Pick<StartResponse, "codeHash"> & { run?: Pick<TriggerRun, "account" | "folder"> }): string {
  return `${start.run?.account ?? ""}:${start.run?.folder ?? ""}:${start.codeHash}`;
}

// loaderIsolateKey hashes the tenant scope and sorted network allowlist into a
// safe WorkerLoader key so isolates never cross account/folder boundaries or
// retain stale Gate network rules.
export async function loaderIsolateKey(scope: string, network: readonly string[] = []): Promise<string> {
  const normalizedNet = [...network].map((entry) => entry.trim().toLowerCase()).sort().join(",");
  const digest = await crypto.subtle.digest("SHA-256", scopeEncoder.encode(`${scope}\0${normalizedNet}`));
  const hex = [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, "0")).join("");
  return `trigger-${hex}`;
}

export interface StartResponse {
  run: TriggerRun;
  eventJson: string;
  modules: Record<string, string>;
  mainModule: string;
  codeHash: string;
  secrets?: Record<string, string>;
  reveal?: string[];
  network?: string[];
  runToken: string;
  callbackUrl?: string;
  stateJson?: string;
  deadlineUnixMs: string | number;
}

export interface Annotation {
  path: string;
  startLine?: number;
  endLine?: number;
  level?: string;
  message: string;
}

export interface CheckRun {
  id: string;
  checkName: string;
  status: string;
  summary?: string;
  detailsUrl?: string;
}

export class Api {
  private readonly base: string;

  constructor(base: string) {
    this.base = base;
  }

  async call<T>(method: string, body: unknown): Promise<T> {
    const res = await fetch(`${this.base.replace(/\/$/, "")}/gitslice.core.v1.TriggerRunService/${method}`, {
      method: "POST",
      headers: { "content-type": "application/json", "user-agent": "Gitslice-Trigger-Dispatch/1.0" },
      body: JSON.stringify(body),
    });
    const text = await res.text();
    if (!res.ok) {
      let err: { code?: string; message?: string } = {};
      try {
        err = JSON.parse(text);
      } catch {
        // not a Connect error body
      }
      throw new ApiError(err.code ?? "unknown", err.message ?? `${method}: HTTP ${res.status}`, res.status);
    }
    return (text ? JSON.parse(text) : {}) as T;
  }
}
