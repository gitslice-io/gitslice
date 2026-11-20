// The module a trigger script runs behind (design/34_triggers.md). It is
// generated per script and is the dynamic Worker's main module: it imports
// the script, gives it the gs API, keeps console output per run (an isolate
// serves every run of its script, maybe at once), and calls onEvent or
// onCallback.

export const ENTRY_MODULE = "__gitslice_entry.js";
export const MAX_RUN_LOG_LINES = 500;
export const MAX_RUN_LOG_LINE_CHARS = 4096;
export const MAX_RUN_LOG_TOTAL_CHARS = 256 * 1024;
export const SECRET_PREFIX = "gs-secret:";
export const WHOLE_URL_SECRET_MESSAGE =
  "a secret cannot be the whole URL of a fetch: the gate opens secrets only where they cannot change the request's origin. " +
  "Write the scheme and host in the script and put the secret in the path, query or a header, " +
  "such as fetch(`https://api.example.com/hook?${gs.secret(\"HOOK_QUERY\")}`).";

export interface LogBuffer {
  lines: { stream: string; text: string }[];
  chars: number;
  truncated: boolean;
}

export function appendLogLine(buf: LogBuffer, stream: string, rawText: string): void {
  if (buf.truncated) return;
  const text =
    rawText.length > MAX_RUN_LOG_LINE_CHARS ? rawText.slice(0, MAX_RUN_LOG_LINE_CHARS - 1) + "…" : rawText;
  if (buf.lines.length >= MAX_RUN_LOG_LINES || buf.chars + text.length > MAX_RUN_LOG_TOTAL_CHARS) {
    buf.truncated = true;
    buf.lines.push({ stream: "stderr", text: "[gitslice: trigger console output truncated]" });
    return;
  }
  buf.lines.push({ stream, text });
  buf.chars += text.length;
}

export function entrySource(mainModule: string): string {
  return `
import { WorkerEntrypoint } from "cloudflare:workers";
import { AsyncLocalStorage } from "node:async_hooks";
import * as loaded from ${JSON.stringify("./" + mainModule)};

const script = loaded.default ?? loaded;

// A secret placeholder can be opened by the gate only where it cannot change
// the request's origin, so a placeholder can never be the whole URL. Say so
// plainly instead of the runtime's "Fetch API cannot load: gs-secret:...".
const realFetch = globalThis.fetch;
globalThis.fetch = function (input, init) {
  const url = typeof input === "string" ? input : input instanceof URL ? input.href : input && input.url;
  if (typeof url === "string" && url.trimStart().startsWith(${JSON.stringify(SECRET_PREFIX)})) {
    return Promise.reject(new TypeError(${JSON.stringify(WHOLE_URL_SECRET_MESSAGE)}));
  }
  return realFetch(input, init);
};
const runLogs = new AsyncLocalStorage();
const MAX_LINES = ${MAX_RUN_LOG_LINES};
const MAX_LINE_CHARS = ${MAX_RUN_LOG_LINE_CHARS};
const MAX_TOTAL_CHARS = ${MAX_RUN_LOG_TOTAL_CHARS};

function format(value) {
  if (typeof value === "string") return value;
  if (value instanceof Error) return value.stack || String(value);
  try { return JSON.stringify(value); } catch { return String(value); }
}
for (const [name, stream] of [["log", "stdout"], ["info", "stdout"], ["debug", "stdout"], ["warn", "stderr"], ["error", "stderr"]]) {
  const original = console[name].bind(console);
  console[name] = (...args) => {
    const store = runLogs.getStore();
    if (!store) return original(...args);
    if (store.truncated) return;
    const raw = args.map(format).join(" ");
    const text = raw.length > MAX_LINE_CHARS ? raw.slice(0, MAX_LINE_CHARS - 1) + "…" : raw;
    if (store.lines.length >= MAX_LINES || store.chars + text.length > MAX_TOTAL_CHARS) {
      store.truncated = true;
      store.lines.push({ stream: "stderr", text: "[gitslice: trigger console output truncated]" });
      return;
    }
    store.lines.push({ stream, text });
    store.chars += text.length;
  };
}

function api(gs, run) {
  const check = (c) => ({ ...c, update: (fields) => gs.updateCheck(c.id, fields).then(check) });
  return {
    checks: {
      create: (fields) => gs.createCheck(fields).then(check),
      update: (id, fields) => gs.updateCheck(id, fields).then(check),
    },
    files: {
      read: (path, options) => gs.readFile(path, options?.at ?? "event"),
      list: (path, options) => gs.listDirectory(path, options?.at ?? "event"),
    },
    changeset: { get: () => gs.getChangeset() },
    state: { get: () => gs.getState(), set: (value) => gs.setState(value) },
    secret(name) {
      const placeholder = run.secrets[name];
      if (!placeholder) throw new Error("secret " + name + " is not listed in the trigger's secrets");
      return placeholder;
    },
    revealSecret: (name) => gs.revealSecret(name),
    runToken: () => gs.runToken(),
    callbackUrl(data) {
      if (!run.callbackUrl) throw new Error("this server has no callback URL for triggers");
      if (!data) return run.callbackUrl;
      return run.callbackUrl + "?" + new URLSearchParams(Object.entries(data).map(([k, v]) => [k, String(v)]));
    },
    idempotencyKey: (label) => run.runId + ":" + String(label ?? ""),
    waitForCallback: () => gs.waitForCallback(),
    finish: () => gs.finish(),
    fail: (message) => gs.fail(String(message ?? "")),
    setDetailsUrl: (url) => gs.setDetailsUrl(String(url ?? "")),
  };
}

export default class extends WorkerEntrypoint {
  async run(kind, eventJson, gs, run, request) {
    const store = { lines: [], chars: 0, truncated: false };
    try {
      return await runLogs.run(store, async () => {
        const event = JSON.parse(eventJson);
        if (kind === "event") {
          if (typeof script.onEvent !== "function") throw new Error("the script exports no onEvent");
          await script.onEvent(event, api(gs, run));
          return null;
        }
        if (typeof script.onCallback !== "function") throw new Error("the script exports no onCallback");
        const req = new Request(request.url, { method: request.method, headers: request.headers, body: request.body || undefined });
        const res = await script.onCallback(req, api(gs, run));
        if (res instanceof Response) return { status: res.status, body: await res.text() };
        return null;
      });
    } finally {
      if (store.lines.length > 0) {
        try {
          await gs.log(store.lines);
        } catch {
          // Best-effort log shipping; never prevent FinishRun from settling the run.
        }
      }
    }
  }
}
`;
}
