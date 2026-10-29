// A client for the server's TriggerRunService over Connect's JSON protocol
// (proto/core/v1/trigger.proto).

export class ApiError extends Error {
  constructor(
    readonly code: string,
    message: string,
    readonly status: number,
  ) {
    super(message);
  }
}

export interface TriggerRun {
  id: string;
  triggerId: string;
  status: string;
  eventKind?: string;
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
  constructor(private readonly base: string) {}

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
