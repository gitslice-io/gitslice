// The harness of `gs automation run --local` (internal/cli/trigger_local.go).
// It runs a worker automation's script with node against a stand-in gs, the
// way the trigger dispatcher would (trigger-dispatch/src/entry.ts): onEvent
// once, then onCallback for each --callback body. Its configuration arrives
// as JSON on stdin. fetch follows the dispatcher's gate: https only, hosts
// in the automation's network, and secrets opened only where they cannot
// change the request's origin. Exit status: 0 succeeded, 3 still waiting
// for a callback, 1 failed or errored.
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

const cfg = JSON.parse(readFileSync(0, "utf8"));
const PREFIX = "gs-local-secret:";
const listed = new Set(cfg.secrets || []);
const reveal = new Set(cfg.reveal || []);
const values = cfg.secretValues || {};
const network = cfg.network || [];
const say = (...args) => console.error("[gs]", ...args);

function hostAllowed(host, entries) {
  host = host.toLowerCase();
  for (const entry of entries) {
    const e = String(entry).toLowerCase();
    if (e.startsWith("*.") ? host.endsWith(e.slice(1)) && host.length > e.length - 1 : host === e) return true;
  }
  return false;
}

function openSecrets(text) {
  return text.replace(/gs-local-secret:([A-Za-z_][A-Za-z0-9_]*)/g, (_, name) => {
    if (!(name in values)) throw new Error(`secret ${name} has no local value: pass --secret ${name}=...`);
    return values[name];
  });
}

const realFetch = globalThis.fetch;
globalThis.fetch = async (input, init = {}) => {
  const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
  if (raw.trimStart().startsWith(PREFIX)) throw new TypeError(cfg.wholeUrlMessage);
  const url = new URL(raw);
  if (url.protocol !== "https:") throw new TypeError(`trigger scripts may only use https (${url.protocol})`);
  if (url.username || url.password) throw new TypeError("trigger scripts may not send URL credentials");
  if (!hostAllowed(url.hostname, network)) throw new TypeError(`${url.hostname} is not in the automation's network`);
  const target = new URL(openSecrets(raw));
  if (target.origin !== url.origin) throw new TypeError("secret placeholders may not change the request origin");
  const headers = new Headers();
  for (const [name, value] of new Headers(init.headers || (typeof input === "object" && !(input instanceof URL) ? input.headers : undefined))) {
    headers.set(name, openSecrets(value));
  }
  let body = init.body;
  if (typeof body === "string") body = openSecrets(body);
  const method = init.method || "GET";
  say(`fetch ${method} ${url.origin}${url.pathname}`);
  return realFetch(target, { ...init, method, headers, body, redirect: "manual" });
};

let waiting = false;
let finished = false;
let failed = null;
let state = null;
let checkSeq = 0;
const checks = new Map();

function resolvePath(path) {
  return String(path).startsWith("/") ? join(cfg.root, path) : join(cfg.folderDir, path);
}

function checkObject(c) {
  return { ...c, update: (fields) => gs.checks.update(c.id, fields) };
}

const gs = {
  secret(name) {
    if (!listed.has(name)) throw new Error("secret " + name + " is not listed in the trigger's secrets");
    return PREFIX + name;
  },
  async revealSecret(name) {
    if (!reveal.has(name)) throw new Error(`secret ${name} is not in the trigger's reveal`);
    if (!(name in values)) throw new Error(`secret ${name} has no local value: pass --secret ${name}=...`);
    return values[name];
  },
  async runToken() {
    if (!reveal.has("RUN_TOKEN")) throw new Error("RUN_TOKEN is not in the trigger's reveal");
    return "local-run-token";
  },
  callbackUrl(data) {
    const base = "https://callback.local.invalid/cb/run_local";
    if (!data) return base;
    return base + "?" + new URLSearchParams(Object.entries(data).map(([k, v]) => [k, String(v)]));
  },
  idempotencyKey: (label) => "run_local:" + String(label ?? ""),
  waitForCallback() {
    waiting = true;
  },
  finish() {
    finished = true;
  },
  fail(message) {
    failed = String(message ?? "");
  },
  async setDetailsUrl(url) {
    const u = new URL(String(url));
    if (u.protocol !== "https:") throw new Error("a details URL must use https");
    say(`details: ${u.href}`);
  },
  checks: {
    async create(fields) {
      const c = { id: `check_local_${++checkSeq}`, status: "queued", ...fields };
      checks.set(c.id, c);
      say(`check ${c.name}: ${c.status}`);
      return checkObject(c);
    },
    async update(id, fields) {
      const c = checks.get(id);
      if (!c) throw new Error(`no check ${id}`);
      Object.assign(c, fields);
      say(`check ${c.name}: ${c.status}${c.summary ? " — " + c.summary : ""}`);
      return checkObject(c);
    },
  },
  files: {
    async read(path) {
      try {
        return readFileSync(resolvePath(path), "utf8");
      } catch {
        return null;
      }
    },
    async list(path) {
      try {
        const dir = resolvePath(path);
        return readdirSync(dir).map((name) => ({
          name,
          path: String(path).replace(/\/$/, "") + "/" + name,
          kind: statSync(join(dir, name)).isDirectory() ? "dir" : "file",
        }));
      } catch {
        return null;
      }
    },
  },
  changeset: { get: async () => null },
  state: {
    get: async () => state,
    set: async (value) => {
      state = value ?? null;
    },
  },
};

let outcome = "succeeded";
let error = "";
async function call(fn) {
  try {
    await fn();
  } catch (err) {
    outcome = "errored";
    error = err instanceof Error ? err.stack || err.message : String(err);
  }
}

const loaded = await import(pathToFileURL(cfg.script).href);
const script = loaded.default ?? loaded;
if (typeof script.onEvent !== "function") {
  outcome = "errored";
  error = "the script exports no onEvent";
} else {
  await call(() => script.onEvent(cfg.event, gs));
}
for (const [i, body] of (cfg.callbacks || []).entries()) {
  if (outcome !== "succeeded" || failed !== null || finished) break;
  if (!waiting) {
    say("onEvent did not call gs.waitForCallback(); the remaining callbacks are not sent");
    break;
  }
  if (typeof script.onCallback !== "function") {
    outcome = "errored";
    error = "the script exports no onCallback";
    break;
  }
  say(`callback ${i + 1}: POST ${body.length} bytes`);
  await call(async () => {
    const res = await script.onCallback(
      new Request(gs.callbackUrl(), { method: "POST", headers: { "content-type": "application/json" }, body }),
      gs,
    );
    if (res instanceof Response) say(`callback ${i + 1} answered ${res.status}`);
  });
}
if (outcome === "succeeded" && failed !== null) {
  outcome = "failed";
  error = failed || "the script failed the run";
}
const openChecks = [...checks.values()].some((c) => c.status === "queued" || c.status === "running");
if (outcome === "succeeded" && !finished && (waiting || openChecks)) outcome = "waiting";
say(`run ${outcome}${error ? ": " + error : ""}`);
process.exit(outcome === "succeeded" ? 0 : outcome === "waiting" ? 3 : 1);
