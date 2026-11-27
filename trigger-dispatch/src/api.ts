// A client for the server's AutomationRunService over Connect's JSON protocol
// (proto/core/v1/automation.proto).

export interface ConnectErrorDetail {
  type?: string;
  value?: string;
  debug?: unknown;
  [key: string]: unknown;
}

export interface ConnectErrorEnvelope {
  code: string;
  message: string;
  details: ConnectErrorDetail[];
}

export function parseConnectErrorEnvelope(text: string, fallbackMessage: string): ConnectErrorEnvelope {
  const trimmed = text.trim();
  if (trimmed) {
    try {
      const parsed = JSON.parse(trimmed) as unknown;
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        const record = parsed as { code?: unknown; message?: unknown; details?: unknown };
        const code = typeof record.code === "string" && record.code.trim() ? record.code.trim() : "unknown";
        const message = typeof record.message === "string" && record.message.trim() ? record.message : fallbackMessage;
        const details = Array.isArray(record.details)
          ? record.details.filter((d): d is ConnectErrorDetail => Boolean(d && typeof d === "object" && !Array.isArray(d)))
          : [];
        return { code, message, details };
      }
    } catch {
      // not a Connect error body
    }
  }
  return {
    code: "unknown",
    message: trimmed || fallbackMessage,
    details: [],
  };
}

export class ApiError extends Error {
  readonly code: string;
  readonly status: number;
  readonly details: readonly ConnectErrorDetail[];

  constructor(code: string, message: string, status: number, details: readonly ConnectErrorDetail[] = []) {
    super(message);
    this.name = "ApiError";
    this.code = code;
    this.status = status;
    this.details = details;
  }

  static fromResponse(status: number, text: string, fallbackMessage = `HTTP ${status}`): ApiError {
    const env = parseConnectErrorEnvelope(text, fallbackMessage);
    return new ApiError(env.code, env.message, status, env.details);
  }
}

export interface AutomationRun {
  id: string;
  account?: string;
  automationId: string;
  folder?: string;
  status: string;
  eventKind?: string;
  commitId?: string;
  definitionCommitId?: string;
  targetRef?: string;
}

const scopeEncoder = new TextEncoder();

// triggerScope binds isolate reuse and sealed secret placeholders to a single
// tenant account, folder, and script code hash.
export function triggerScope(start: Pick<StartResponse, "codeHash"> & { run?: Pick<AutomationRun, "account" | "folder"> }): string {
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
  run: AutomationRun;
  action?: string;
  eventJson: string;
  modules: Record<string, string>;
  mainModule: string;
  codeHash: string;
  secrets?: Record<string, string>;
  reveal?: string[];
  network?: string[];
  egress?: string;
  runToken: string;
  callbackUrl?: string;
  stateJson?: string;
  deadlineUnixMs: string | number;
  bashScript?: string;
  bashRuntime?: string;
  bashSize?: string;
  bashCheckout?: string;
  bashInclude?: string[];
  bashWorkingDir?: string;
  bashEnv?: Record<string, string>;
  inputs?: Record<string, string>;
  timeoutMs?: string | number;
}

export interface Annotation {
  path: string;
  line?: number;
  startLine?: number;
  endLine?: number;
  level?: string;
  message: string;
}

export interface DiffChangesetOptions {
  paths?: string[];
  fromPatchset?: string;
}

export interface DiffChangesetResult {
  changesetId: string;
  fromPatchsetId?: string;
  toPatchsetId: string;
  changedPaths: string[];
  diff: string;
}

export function normalizeAnnotations(annotations?: Annotation[]): Annotation[] | undefined {
  if (!annotations) return undefined;
  return annotations.map((a) => {
    const startLine = a.startLine ?? a.line;
    const endLine = a.endLine ?? a.line;
    const out: Annotation = {
      path: a.path,
      level: a.level,
      message: a.message,
    };
    if (startLine !== undefined) out.startLine = startLine;
    if (endLine !== undefined) out.endLine = endLine;
    return out;
  });
}

export interface CheckRun {
  id: string;
  checkName: string;
  status: string;
  summary?: string;
  detailsUrl?: string;
}

const DEFAULT_RETRY_DELAYS_MS = [100, 250];

function isTransientStatus(status: number): boolean {
  return status === 502 || status === 503;
}

export class Api {
  private readonly base: string;
  private readonly retryDelaysMs: readonly number[];

  constructor(base: string, options: { retryDelaysMs?: readonly number[] } = {}) {
    this.base = base;
    this.retryDelaysMs = options.retryDelaysMs ?? DEFAULT_RETRY_DELAYS_MS;
  }

  async call<T>(method: string, body: unknown): Promise<T> {
    const url = `${this.base.replace(/\/$/, "")}/gitslice.core.v1.AutomationRunService/${method}`;
    const payload = JSON.stringify(body);
    let lastError: unknown;
    for (let attempt = 0; attempt <= this.retryDelaysMs.length; attempt++) {
      if (attempt > 0) {
        const delay = this.retryDelaysMs[attempt - 1];
        if (delay > 0) {
          await new Promise((resolve) => setTimeout(resolve, delay));
        }
      }
      let res: Response;
      try {
        res = await fetch(url, {
          method: "POST",
          headers: { "content-type": "application/json", "user-agent": "Gitslice-Trigger-Dispatch/1.0" },
          body: payload,
        });
      } catch (err) {
        lastError = err;
        if (attempt < this.retryDelaysMs.length) continue;
        throw err;
      }
      const text = await res.text();
      if (!res.ok) {
        const apiErr = ApiError.fromResponse(res.status, text, `${method}: HTTP ${res.status}`);
        if (isTransientStatus(res.status) && attempt < this.retryDelaysMs.length) {
          lastError = apiErr;
          continue;
        }
        throw apiErr;
      }
      return (text ? JSON.parse(text) : {}) as T;
    }
    throw lastError;
  }

  async fetchArchive(runToken: string): Promise<Response> {
    return fetch(`${this.base.replace(/\/$/, "")}/automation-archive`, {
      method: "GET",
      headers: {
        authorization: `Bearer ${runToken}`,
        "user-agent": "Gitslice-Trigger-Dispatch/1.0",
      },
    });
  }
}
