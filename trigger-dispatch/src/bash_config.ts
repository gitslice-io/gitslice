// Pure bootstrap, egress policy, and GsBridge HTTP routing helpers for bash
// automations running inside Cloudflare Containers.

import type { Api, StartResponse } from "./api.ts";
import { sameString, seal, sealKey } from "./crypto.ts";
import { expandSecretVariants, maskSecrets } from "./mask.ts";

export type BashRuntime = "base" | "node" | "python" | "go" | "cloud";
export type BashSize = "lite" | "basic" | "standard-1" | "standard-2";
export type BashEgress = "offline" | "http-gate" | "direct";

export interface BashBootstrapPayload {
  script: string;
  event_json: string;
  env: Record<string, string>;
  secret_files: Record<string, string>;
  checkout: string;
  folder_rel: string;
  working_dir: string;
  timeout_ms: number;
}

export interface PreparedBashRun {
  bootstrap: BashBootstrapPayload;
  runtime: BashRuntime;
  size: BashSize;
  egress: BashEgress;
  enableInternet: boolean;
  network: string[];
  scope: string;
  timeoutMs: number;
  secretValues: string[];
}

const VALID_RUNTIMES = new Set<BashRuntime>(["base", "node", "python", "go", "cloud"]);
const VALID_SIZES = new Set<BashSize>(["lite", "basic", "standard-1", "standard-2"]);

const DEFAULT_BASH_TIMEOUT_MS = 5 * 60_000;
const MAX_BASH_TIMEOUT_MS = 15 * 60_000;

export function inputEnvName(name: string): string {
  return "INPUT_" + name.trim().replace(/-/g, "_").toUpperCase();
}

export function folderRelFromCanonical(account: string, folder: string): string {
  const prefix = "/" + account.replace(/^\/+|\/+$/g, "");
  const clean = folder.replace(/\/+$/, "");
  if (clean === prefix) return "";
  if (clean.startsWith(prefix + "/")) return clean.slice(prefix.length + 1);
  return clean.replace(/^\/+/, "");
}

export function normalizeRuntime(raw?: string): BashRuntime {
  const val = (raw ?? "").trim().toLowerCase() as BashRuntime;
  return VALID_RUNTIMES.has(val) ? val : "base";
}

export function normalizeSize(raw?: string): BashSize {
  const val = (raw ?? "").trim().toLowerCase() as BashSize;
  return VALID_SIZES.has(val) ? val : "basic";
}

export function normalizeEgress(raw?: string, network: readonly string[] = []): BashEgress {
  const val = (raw ?? "").trim().toLowerCase();
  if (val === "direct" || val === "http-gate" || val === "offline") {
    return val;
  }
  return network.length > 0 ? "http-gate" : "offline";
}

// prepareBashRun computes the container boot configuration and one-time
// bootstrap payload. Secrets not in reveal are AES-GCM sealed for Gate;
// secrets in reveal are passed verbatim (and still scrubbed from all logs).
export async function prepareBashRun(
  dispatchSecret: string,
  start: StartResponse,
  scope: string,
): Promise<PreparedBashRun> {
  const network = start.network ?? [];
  const egress = normalizeEgress(start.egress, network);
  const enableInternet = egress === "direct";
  const runtime = normalizeRuntime(start.bashRuntime);
  const size = normalizeSize(start.bashSize);

  const rawTimeout = Number(start.timeoutMs) || DEFAULT_BASH_TIMEOUT_MS;
  const timeoutMs = Math.min(Math.max(rawTimeout, 1_000), MAX_BASH_TIMEOUT_MS);

  const revealSet = new Set(start.reveal ?? []);
  const key = await sealKey(dispatchSecret);
  const exp = Number(start.deadlineUnixMs) || Date.now() + timeoutMs + 60_000;

  const env: Record<string, string> = {};
  for (const [k, v] of Object.entries(start.bashEnv ?? {})) {
    env[k] = v;
  }

  // Standard Gitslice metadata env vars.
  env.GITSLICE_RUN_ID = start.run.id;
  env.GITSLICE_ACCOUNT = start.run.account ?? "";
  env.GITSLICE_AUTOMATION_ID = start.run.automationId;
  env.GITSLICE_FOLDER = start.run.folder ?? "";
  env.GITSLICE_EVENT = start.run.eventKind ?? "";
  if (start.run.commitId) env.GITSLICE_COMMIT = start.run.commitId;
  if (start.run.definitionCommitId) env.GITSLICE_DEFINITION_COMMIT = start.run.definitionCommitId;
  if (start.run.targetRef) env.GITSLICE_REF = start.run.targetRef;

  // Typed manual inputs passed strictly as data env vars.
  for (const [name, value] of Object.entries(start.inputs ?? {})) {
    env[inputEnvName(name)] = value;
  }

  // Secrets: sealed placeholders unless explicitly listed in reveal.
  const secretFiles: Record<string, string> = {};
  const secretValues: string[] = [];
  for (const [name, value] of Object.entries(start.secrets ?? {})) {
    secretValues.push(value);
    const exposed = revealSet.has(name) ? value : await seal(key, { v: value, code: scope, exp });
    env[name] = exposed;
    secretFiles[name] = exposed;
  }
  if (revealSet.has("RUN_TOKEN") && start.runToken) {
    env.RUN_TOKEN = start.runToken;
    secretFiles.RUN_TOKEN = start.runToken;
    secretValues.push(start.runToken);
  }

  return {
    bootstrap: {
      script: start.bashScript ?? "",
      event_json: start.eventJson || "{}",
      env,
      secret_files: secretFiles,
      checkout: start.bashCheckout || "none",
      folder_rel: folderRelFromCanonical(start.run.account ?? "", start.run.folder ?? ""),
      working_dir: start.bashWorkingDir || ".",
      timeout_ms: timeoutMs,
    },
    runtime,
    size,
    egress,
    enableInternet,
    network,
    scope,
    timeoutMs,
    secretValues,
  };
}

export interface BridgeBackend {
  consumeBootstrap(nonce: string): Promise<BashBootstrapPayload | null>;
  finishContainer(): Promise<void>;
}

export interface BridgeContext {
  api: Pick<Api, "call" | "fetchArchive">;
  runToken: string;
  secretValues: readonly string[];
  backend: BridgeBackend;
}

// handleBridgeRequest serves http://gitslice.internal/* requests intercepted
// from inside a running bash container.
export async function handleBridgeRequest(request: Request, ctx: BridgeContext): Promise<Response> {
  const url = new URL(request.url);
  const path = url.pathname;
  const variants = expandSecretVariants(ctx.secretValues);

  if (request.method === "GET" && path === "/bootstrap") {
    const nonce = request.headers.get("x-bootstrap-nonce") ?? "";
    if (!nonce) {
      return new Response("missing bootstrap nonce", { status: 403 });
    }
    const payload = await ctx.backend.consumeBootstrap(nonce);
    if (!payload) {
      return new Response("invalid or already consumed bootstrap nonce", { status: 403 });
    }
    return Response.json(payload);
  }

  if (request.method === "GET" && path === "/archive") {
    const res = await ctx.api.fetchArchive(ctx.runToken);
    if (!res.ok) {
      const text = await res.text();
      return new Response(maskSecrets(text, variants), { status: res.status });
    }
    return new Response(res.body, {
      status: 200,
      headers: { "content-type": res.headers.get("content-type") ?? "application/gzip" },
    });
  }

  if (request.method === "POST" && path === "/logs") {
    let parsed: { lines?: { stream?: string; text?: string }[] };
    try {
      parsed = (await request.json()) as { lines?: { stream?: string; text?: string }[] };
    } catch {
      return new Response("invalid JSON", { status: 400 });
    }
    const lines = (parsed.lines ?? []).slice(0, 200).map((line) => ({
      stream: line.stream === "stderr" ? "stderr" : "stdout",
      text: maskSecrets(String(line.text ?? "").slice(0, 4096), variants),
    }));
    try {
      await ctx.api.call("AppendRunLog", { runToken: ctx.runToken, lines });
      return Response.json({ ok: true });
    } catch (err) {
      const status = mapApiErrStatus(err);
      return new Response(maskSecrets(err instanceof Error ? err.message : String(err), variants), { status });
    }
  }

  if (request.method === "POST" && path === "/finish") {
    let parsed: { outcome?: string; exit_code?: number; error?: string };
    try {
      parsed = (await request.json()) as { outcome?: string; exit_code?: number; error?: string };
    } catch {
      return new Response("invalid JSON", { status: 400 });
    }
    const outcome = parsed.outcome === "succeeded" ? "succeeded" : parsed.outcome === "failed" ? "failed" : "errored";
    const error = maskSecrets(String(parsed.error ?? ""), variants);
    try {
      await ctx.api.call("FinishRun", {
        runToken: ctx.runToken,
        outcome,
        error,
        wait: false,
        finish: true,
        callback: false,
      });
    } catch {
      // Run may already be canceled or timed out; still tear down container.
    }
    await ctx.backend.finishContainer();
    return Response.json({ ok: true });
  }

  if (request.method === "POST" && path.startsWith("/gs/")) {
    let body: Record<string, unknown>;
    try {
      body = (await request.json()) as Record<string, unknown>;
    } catch {
      return new Response("invalid JSON", { status: 400 });
    }
    const methodMap: Record<string, string> = {
      "/gs/check/create": "CreateCheck",
      "/gs/check/update": "UpdateCheck",
      "/gs/state/get": "GetRunState",
      "/gs/state/set": "SetRunState",
    };
    const rpcMethod = methodMap[path];
    if (!rpcMethod) {
      return new Response("unknown bridge endpoint", { status: 404 });
    }
    try {
      const result = await ctx.api.call(rpcMethod, { ...body, runToken: ctx.runToken });
      return Response.json(result);
    } catch (err) {
      return new Response(maskSecrets(err instanceof Error ? err.message : String(err), variants), {
        status: mapApiErrStatus(err),
      });
    }
  }

  return new Response("not found", { status: 404 });
}

function mapApiErrStatus(err: unknown): number {
  const code = (err as { code?: string })?.code;
  if (code === "failed_precondition") return 412;
  if (code === "permission_denied") return 403;
  if (code === "unauthenticated") return 401;
  if (code === "not_found") return 404;
  if (code === "invalid_argument") return 400;
  return 502;
}

export function verifyBootstrapNonce(expected: string, actual: string): boolean {
  if (!expected || !actual) return false;
  return sameString(expected, actual);
}
