// The trigger dispatcher (design/34_triggers.md). The Gitslice server posts it
// run ids; it claims each run with the server, loads the trigger's script as
// a Dynamic Worker (one isolate per script, keyed by its code hash), and calls
// onEvent. Outside systems call runs back at /cb/<run>/<signature>, which
// calls onCallback. Scripts reach the internet only through the Gate, which
// allows the trigger's network hosts and opens secret placeholders there.

import { RpcTarget, WorkerEntrypoint } from "cloudflare:workers";

import {
  Api,
  ApiError,
  loaderIsolateKey,
  triggerScope,
  type Annotation,
  type CheckRun,
  type StartResponse,
} from "./api.ts";
import { PlaceholderError, seal, sealKey, verifyDispatch } from "./crypto.ts";
import { ENTRY_MODULE, entrySource, MAX_RUN_LOG_LINE_CHARS, MAX_RUN_LOG_LINES } from "./entry.ts";
import { GatePolicyError, prepareGateRequest } from "./gate.ts";
import { BashRunner, GsBridge } from "./bash_runner.ts";

export { BashRunner, GsBridge };

export interface Env {
  LOADER: WorkerLoader;
  BASH_RUNNER: DurableObjectNamespace<BashRunner>;
  GITSLICE_API: string;
  DISPATCH_SECRET: string;
}

// The dispatch request's own deadline: waitUntil gets 30 s after the
// response, so a script call must end before that.
const SCRIPT_TIMEOUT_MS = 25_000;
const MAX_CALLBACK_BODY = 1 << 20;
const COMPATIBILITY_DATE = "2026-10-01";

interface GateProps {
  network: string[];
  scope: string;
}

// Gate is every dynamic Worker's globalOutbound.
export class Gate extends WorkerEntrypoint<Env, GateProps> {
  async fetch(request: Request): Promise<Response> {
    const { network, scope } = this.ctx.props;
    const key = await sealKey(this.env.DISPATCH_SECRET);
    try {
      const prepared = await prepareGateRequest(request, key, scope, network);
      return await fetch(prepared.target, {
        method: prepared.method,
        headers: prepared.headers,
        body: prepared.body,
        redirect: "manual",
      });
    } catch (err) {
      if (err instanceof GatePolicyError || err instanceof PlaceholderError) {
        return new Response(err.message, { status: 403 });
      }
      throw err;
    }
  }
}

interface RunContext {
  runId: string;
  secrets: Record<string, string>;
  callbackUrl: string;
}

// Gs is the gs object of one call of a script, passed to it over RPC. Every
// call goes to the server with the run's token.
class Gs extends RpcTarget {
  waiting = false;
  finished = false;

  constructor(
    private readonly api: Api,
    private readonly start: StartResponse,
  ) {
    super();
  }

  private call<T>(method: string, body: Record<string, unknown>): Promise<T> {
    return this.api.call<T>(method, { runToken: this.start.runToken, ...body });
  }

  async createCheck(fields: { name: string; status?: string; summary?: string; detailsUrl?: string; annotations?: Annotation[] }): Promise<CheckRun> {
    return this.call("CreateCheck", fields);
  }

  async updateCheck(id: string, fields: { status?: string; summary?: string; detailsUrl?: string; annotations?: Annotation[] }): Promise<CheckRun> {
    return this.call("UpdateCheck", { checkRunId: id, ...fields });
  }

  async readFile(path: string, at: string): Promise<string | null> {
    const res = await this.call<{ found?: boolean; content?: string }>("ReadFile", { path, at });
    if (!res.found) return null;
    const bytes = Uint8Array.from(atob(res.content ?? ""), (c) => c.charCodeAt(0));
    return new TextDecoder().decode(bytes);
  }

  async listDirectory(path: string, at: string): Promise<{ name: string; path: string; kind: string }[] | null> {
    const res = await this.call<{ found?: boolean; entries?: { name: string; path: string; kind: string }[] }>("ListDirectory", { path, at });
    return res.found ? (res.entries ?? []) : null;
  }

  async getChangeset(): Promise<unknown> {
    return this.call("GetChangeset", {});
  }

  async getState(): Promise<unknown> {
    const res = await this.call<{ stateJson?: string }>("GetRunState", {});
    return res.stateJson ? JSON.parse(res.stateJson) : null;
  }

  async setState(value: unknown): Promise<void> {
    await this.call("SetRunState", { stateJson: value === undefined || value === null ? "" : JSON.stringify(value) });
  }

  async revealSecret(name: string): Promise<string> {
    if (!(this.start.reveal ?? []).includes(name)) {
      throw new Error(`secret ${name} is not in the trigger's reveal`);
    }
    const value = this.start.secrets?.[name];
    if (value === undefined) throw new Error(`no secret ${name}`);
    return value;
  }

  async runToken(): Promise<string> {
    if (!(this.start.reveal ?? []).includes("RUN_TOKEN")) {
      throw new Error("RUN_TOKEN is not in the trigger's reveal");
    }
    return this.start.runToken;
  }

  async log(lines: { stream: string; text: string }[]): Promise<void> {
    const capped = lines.slice(0, MAX_RUN_LOG_LINES + 1).map((line) => ({
      stream: line.stream === "stderr" ? "stderr" : "stdout",
      text: String(line.text ?? "").slice(0, MAX_RUN_LOG_LINE_CHARS),
    }));
    for (let i = 0; i < capped.length; i += 200) {
      await this.call("AppendRunLog", { lines: capped.slice(i, i + 200) });
    }
  }

  waitForCallback(): void {
    this.waiting = true;
  }

  finish(): void {
    this.finished = true;
  }
}

// placeholders seals each listed secret for this tenant/folder/script scope,
// until the run's deadline.
async function placeholders(env: Env, start: StartResponse, scope: string): Promise<Record<string, string>> {
  const key = await sealKey(env.DISPATCH_SECRET);
  const exp = Number(start.deadlineUnixMs) || Date.now() + 3_600_000;
  const out: Record<string, string> = {};
  for (const [name, value] of Object.entries(start.secrets ?? {})) {
    out[name] = await seal(key, { v: value, code: scope, exp });
  }
  return out;
}

async function loadScript(env: Env, ctx: ExecutionContext, start: StartResponse, scope: string) {
  const exports = (ctx as unknown as { exports: { Gate: (opts: { props: GateProps }) => Fetcher } }).exports;
  const network = start.network ?? [];
  const key = await loaderIsolateKey(scope, network);
  return env.LOADER.get(key, async () => ({
    compatibilityDate: COMPATIBILITY_DATE,
    compatibilityFlags: ["nodejs_als"],
    mainModule: ENTRY_MODULE,
    modules: { ...start.modules, [ENTRY_MODULE]: entrySource(start.mainModule) },
    globalOutbound: exports.Gate({ props: { network, scope } }),
  }));
}

interface CallbackRequest {
  url: string;
  method: string;
  headers: [string, string][];
  body: string;
}

// execute calls the script once and tells the server how the call ended.
async function execute(env: Env, ctx: ExecutionContext, api: Api, start: StartResponse, kind: "event" | "callback", request?: CallbackRequest): Promise<{ status: number; body: string } | null> {
  const gs = new Gs(api, start);
  const scope = triggerScope(start);
  const run: RunContext = { runId: start.run.id, secrets: await placeholders(env, start, scope), callbackUrl: start.callbackUrl ?? "" };
  let outcome = "succeeded";
  let error = "";
  let result: { status: number; body: string } | null = null;
  try {
    const worker = await loadScript(env, ctx, start, scope);
    const entrypoint = worker.getEntrypoint() as unknown as {
      run(kind: string, eventJson: string, gs: Gs, run: RunContext, request?: CallbackRequest): Promise<{ status: number; body: string } | null>;
    };
    let timer: ReturnType<typeof setTimeout> | undefined;
    const timeout = new Promise<never>((_, reject) => {
      timer = setTimeout(() => reject(new Error(`the script ran longer than ${SCRIPT_TIMEOUT_MS / 1000} s`)), SCRIPT_TIMEOUT_MS);
    });
    try {
      result = await Promise.race([entrypoint.run(kind, start.eventJson, gs, run, request), timeout]);
    } finally {
      clearTimeout(timer);
    }
  } catch (err) {
    outcome = "errored";
    error = err instanceof Error ? (err.stack ?? err.message) : String(err);
  }
  try {
    await api.call("FinishRun", { runToken: start.runToken, outcome, error, wait: gs.waiting, finish: gs.finished, callback: kind === "callback" });
  } catch (err) {
    // The server settles the run when its lease runs out.
    console.warn("finish trigger run", start.run.id, err);
  }
  return result;
}

async function startEvent(env: Env, ctx: ExecutionContext, runId: string): Promise<void> {
  const api = new Api(env.GITSLICE_API);
  let start: StartResponse;
  try {
    start = await api.call<StartResponse>("StartRun", { runId, dispatcherKey: env.DISPATCH_SECRET });
  } catch (err) {
    if (err instanceof ApiError && err.code === "failed_precondition") {
      return; // started already, or canceled: a duplicate post
    }
    console.error("start trigger run", runId, err);
    return; // the server posts it again
  }
  await execute(env, ctx, api, start, "event");
}

async function callback(env: Env, ctx: ExecutionContext, request: Request, runId: string, signature: string): Promise<Response> {
  if (request.method !== "POST") {
    return new Response("callbacks are POSTs", { status: 405 });
  }
  const length = Number(request.headers.get("content-length") ?? "0");
  if (length > MAX_CALLBACK_BODY) {
    return new Response("callback body too large", { status: 413 });
  }
  const body = await request.text();
  if (body.length > MAX_CALLBACK_BODY) {
    return new Response("callback body too large", { status: 413 });
  }
  const api = new Api(env.GITSLICE_API);
  let start: StartResponse;
  try {
    start = await api.call<StartResponse>("StartCallback", { runId, dispatcherKey: env.DISPATCH_SECRET, callbackSignature: signature });
  } catch (err) {
    if (err instanceof ApiError) {
      const status = err.code === "permission_denied" ? 403 : err.code === "failed_precondition" ? 410 : err.code === "not_found" ? 404 : 502;
      return new Response(err.message, { status });
    }
    throw err;
  }
  const result = await execute(env, ctx, api, start, "callback", {
    url: request.url,
    method: request.method,
    headers: [...request.headers],
    body,
  });
  if (result) return new Response(result.body, { status: result.status });
  return Response.json({ ok: true });
}

export default {
  async fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    const url = new URL(request.url);
    if (request.method === "POST" && url.pathname === "/run") {
      const body = await request.text();
      if (!(await verifyDispatch(env.DISPATCH_SECRET, body, request.headers.get("x-gitslice-dispatch-signature")))) {
        return new Response("bad signature", { status: 401 });
      }
      let msg: { run_id?: string; action?: string; ts?: number };
      try {
        msg = JSON.parse(body);
      } catch {
        return new Response("bad body", { status: 400 });
      }
      if (!msg.run_id || !msg.ts || Math.abs(Date.now() / 1000 - msg.ts) > 300) {
        return new Response("stale or incomplete post", { status: 400 });
      }
      if (msg.action === "bash") {
        const stub = env.BASH_RUNNER.get(env.BASH_RUNNER.idFromName(msg.run_id));
        ctx.waitUntil(stub.startRun(msg.run_id));
      } else {
        ctx.waitUntil(startEvent(env, ctx, msg.run_id));
      }
      return new Response(null, { status: 202 });
    }
    if (request.method === "POST" && url.pathname === "/cancel") {
      const body = await request.text();
      if (!(await verifyDispatch(env.DISPATCH_SECRET, body, request.headers.get("x-gitslice-dispatch-signature")))) {
        return new Response("bad signature", { status: 401 });
      }
      let msg: { run_id?: string; ts?: number };
      try {
        msg = JSON.parse(body);
      } catch {
        return new Response("bad body", { status: 400 });
      }
      if (!msg.run_id || !msg.ts || Math.abs(Date.now() / 1000 - msg.ts) > 300) {
        return new Response("stale or incomplete post", { status: 400 });
      }
      const stub = env.BASH_RUNNER.get(env.BASH_RUNNER.idFromName(msg.run_id));
      ctx.waitUntil(stub.cancelRun());
      return new Response(null, { status: 202 });
    }
    const cb = url.pathname.match(/^\/cb\/(trun_[A-Za-z0-9]+)\/([0-9a-f]{32})$/);
    if (cb) {
      return callback(env, ctx, request, cb[1], cb[2]);
    }
    if (url.pathname === "/") {
      return new Response("Gitslice automation dispatcher\n");
    }
    return new Response("not found", { status: 404 });
  },
} satisfies ExportedHandler<Env>;
