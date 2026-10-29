// The module a trigger script runs behind (design/34_triggers.md). It is
// generated per script and is the dynamic Worker's main module: it imports
// the script, gives it the gs API, keeps console output per run (an isolate
// serves every run of its script, maybe at once), and calls onEvent or
// onCallback.

export const ENTRY_MODULE = "__gitslice_entry.js";

export function entrySource(mainModule: string): string {
  return `
import { WorkerEntrypoint } from "cloudflare:workers";
import { AsyncLocalStorage } from "node:async_hooks";
import * as loaded from ${JSON.stringify("./" + mainModule)};

const script = loaded.default ?? loaded;
const runLogs = new AsyncLocalStorage();

function format(value) {
  if (typeof value === "string") return value;
  if (value instanceof Error) return value.stack || String(value);
  try { return JSON.stringify(value); } catch { return String(value); }
}
for (const [name, stream] of [["log", "stdout"], ["info", "stdout"], ["debug", "stdout"], ["warn", "stderr"], ["error", "stderr"]]) {
  const original = console[name].bind(console);
  console[name] = (...args) => {
    const lines = runLogs.getStore();
    if (!lines) return original(...args);
    lines.push({ stream, text: args.map(format).join(" ") });
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
  };
}

export default class extends WorkerEntrypoint {
  async run(kind, eventJson, gs, run, request) {
    const lines = [];
    try {
      return await runLogs.run(lines, async () => {
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
      if (lines.length > 0) await gs.log(lines);
    }
  }
}
`;
}
