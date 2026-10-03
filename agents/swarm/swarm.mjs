#!/usr/bin/env node
// Run a swarm of agents against gitslice-agents. Every agent opens a session
// (its own Artifacts fork of the slice), clones it with plain git, makes its
// change, pushes, and follows the signals until the change lands:
//   conflict          → rework on a newer baseline (a new session, resume=…)
//   changes-requested → push the fix to the same session
//   needs-human       → wait for a person to approve in Gitslice
//
//   AGENTS_API_KEY=… node swarm/swarm.mjs [--slice demo/storefront] [--reset] [--concurrency 24]
//       [--only i18n,merge,…|agent,…] [--skip agent,…] [--agents N] [--url https://agents.gitslice.io] [--quiet]
//
// --reset first puts the slice back to its seed and clears the dashboard, so
// every agent has its work to do.

import { spawn } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { storefrontTasks } from "./storefront.mjs";

const args = parseArgs(process.argv.slice(2));
const URL_BASE = args.url ?? process.env.AGENTS_URL ?? "https://agents.gitslice.io";
const SLICE = args.slice ?? "demo/storefront";
const KEY = process.env.AGENTS_API_KEY ?? (args["key-file"] ? (await readFile(args["key-file"], "utf8")).trim() : "");
const CONCURRENCY = Number(args.concurrency ?? 24);
const LIMIT = Number(args.agents ?? Infinity);
const STAGGER_MS = Number(args.stagger ?? 120);
const HUMAN_WAIT_MS = Number(args["human-wait"] ?? 15 * 60_000);
const ONLY = args.only ? new Set(String(args.only).split(",")) : null;
const SKIP = new Set(String(args.skip ?? "").split(",").filter(Boolean));
const QUIET = Boolean(args.quiet);
if (!KEY) fail("set AGENTS_API_KEY or --key-file");

const COLOR = { reset: "\x1b[0m", dim: "\x1b[2m", bold: "\x1b[1m", green: "\x1b[32m", red: "\x1b[31m", yellow: "\x1b[33m", magenta: "\x1b[35m", cyan: "\x1b[36m", blue: "\x1b[34m", gray: "\x1b[90m" };
const STATUS_COLOR = { forked: "gray", pushed: "blue", reviewing: "magenta", approved: "cyan", landing: "cyan", merging: "yellow", landed: "green", conflict: "red", "needs-human": "yellow", "changes-requested": "red", failed: "red" };
const TERMINAL = new Set(["landed", "conflict", "needs-human", "changes-requested", "failed"]);

const results = [];
const started = Date.now();
const finished = new Map(); // agent → promise of its outcome, for pushAfter

if (args.reset) {
  const [account, slice] = SLICE.split("/");
  process.stdout.write(`Restoring ${SLICE} to its seed…\n`);
  const r = await api("POST", `/v1/slices/${account}/${slice}/restore`, {});
  process.stdout.write(`${r.changed ? `restored ${r.changed} files (${r.handle})` : "already at the seed"}; deleted ${r.deleted} repos from the last run\n\n`);
}

const tasks = interleave(storefrontTasks(SLICE).filter((t) => (!ONLY || ONLY.has(t.kind) || ONLY.has(t.agent)) && !SKIP.has(t.agent))).slice(0, LIMIT);
banner(`${tasks.length} agents → ${SLICE} via ${URL_BASE} (concurrency ${CONCURRENCY})`);
for (const task of tasks) {
  let resolve;
  finished.set(task.agent, { promise: new Promise((r) => (resolve = r)), resolve });
}
await pool(tasks, CONCURRENCY, async (task, i) => {
  await sleep(i < CONCURRENCY ? i * STAGGER_MS : 0);
  const result = await runAgent(task).catch((err) => ({ agent: task.agent, outcome: "error", error: String(err.message ?? err) }));
  finished.get(task.agent).resolve(result);
  results.push(result);
});
summary();

async function runAgent(task) {
  const t0 = Date.now();
  let resume;
  let reworked = false;
  for (let attempt = 1; attempt <= 3; attempt++) {
    const session = await api("POST", "/v1/sessions", { slice: SLICE, agent: task.agent, task: task.task, intent: task.intent, resume });
    log(task.agent, "forked", `${session.id}${resume ? "  (rework on a newer baseline)" : ""}`);
    for (const signal of session.signals ?? []) log(task.agent, "signal", signal.message, "yellow");
    const dir = await mkdtemp(join(tmpdir(), "agent-"));
    try {
      await git(["-c", auth(session.token), "clone", "-q", session.remote, dir]);
      const change = await task.apply(dir, { assist });
      if (!change) {
        log(task.agent, "noop", "nothing to change");
        return { agent: task.agent, outcome: "noop" };
      }
      if (task.pushAfter && attempt === 1 && finished.has(task.pushAfter)) {
        log(task.agent, "waiting", `to push until ${task.pushAfter} lands`, "gray");
        await Promise.race([finished.get(task.pushAfter).promise, sleep(3 * 60_000)]);
      }
      await commitAndPush(dir, task, session, change);
      let state = await follow(task.agent, session.id, t0);
      if (state.status === "changes-requested" && task.fix) {
        const fix = await task.fix(dir);
        await commitAndPush(dir, task, session, fix);
        state = await follow(task.agent, session.id, t0, state.pushes);
      }
      if (state.status === "changes-requested" && task.stall) {
        // This agent never answers its review: a fixer agent has to.
        log(task.agent, "waiting", "does not answer the review; a fixer agent should take over", "yellow");
        state = await untilLanded(session.id, 4 * 60_000);
      }
      if (state.status === "needs-human") {
        log(task.agent, "waiting", "for a human to approve in Gitslice", "yellow");
        state = await follow(task.agent, session.id, t0, undefined, HUMAN_WAIT_MS);
      }
      if (state.status === "conflict" && attempt < 3) {
        reworked = true;
        resume = session.id;
        continue;
      }
      return { agent: task.agent, outcome: state.status, autoMerged: Boolean(state.autoMerged), reworked, fixed: Boolean(task.stall && state.status === "landed"), ms: Date.now() - t0, handle: state.handle };
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
  }
  return { agent: task.agent, outcome: "conflict", reworked, ms: Date.now() - t0 };
}

async function commitAndPush(dir, task, session, change) {
  const message = [
    change.subject,
    "",
    change.body,
    "",
    `Agent: ${task.agent}`,
    `Task: ${task.task}`,
    `Model: ${task.model ? "openai/gpt-oss-120b (Workers AI)" : "scripted"}`,
  ].join("\n");
  await git(["-C", dir, "add", "-A"]);
  await git(["-C", dir, "-c", `user.name=${task.agent}`, "-c", `user.email=${task.agent}@agents.gitslice.io`, "commit", "-q", "-F", "-"], message);
  await git(["-C", dir, "-c", auth(session.token), "push", "-q", "origin", "HEAD:main"]);
  log(task.agent, "pushed", change.subject);
  // Artifacts emits a repo.pushed event on its own; the nudge only saves time.
  api("POST", `/v1/sessions/${session.id}/pushed`, {}).catch(() => {});
}

// follow polls the session and prints its transitions until it settles.
async function follow(agent, id, t0, afterPushes, maxMs = 5 * 60_000) {
  let last = "";
  const deadline = Date.now() + maxMs;
  while (Date.now() < deadline) {
    const s = await api("GET", `/v1/sessions/${id}`);
    if (afterPushes !== undefined && s.pushes <= afterPushes) {
      await sleep(600);
      continue;
    }
    if (s.status !== last) {
      last = s.status;
      const detail =
        s.status === "landed"
          ? `${s.handle ?? ""}  +${((Date.now() - t0) / 1000).toFixed(1)}s${s.autoMerged ? "  auto-merged" : ""}`
          : s.status === "reviewing"
            ? s.handle ?? ""
            : s.status === "pushed"
              ? ""
              : lastSignal(s);
      log(agent, s.status, detail, STATUS_COLOR[s.status]);
    }
    if (TERMINAL.has(s.status) && !(s.status === "needs-human" && maxMs > 5 * 60_000)) return s;
    if (s.status === "landed" || s.status === "failed" || s.status === "conflict") return s;
    await sleep(700);
  }
  return { status: "timeout" };
}

// untilLanded polls a session until it lands or ends in something else.
async function untilLanded(id, maxMs) {
  const deadline = Date.now() + maxMs;
  while (Date.now() < deadline) {
    const s = await api("GET", `/v1/sessions/${id}`);
    if (s.status === "landed" || s.status === "failed" || s.status === "needs-human") return s;
    await sleep(700);
  }
  return { status: "timeout" };
}

function lastSignal(s) {
  const sig = s.signals?.[s.signals.length - 1];
  return sig ? sig.message : "";
}

async function assist(instructions, input) {
  const res = await api("POST", "/v1/assist", { instructions, input });
  return res.text;
}

async function api(method, path, body) {
  for (let attempt = 0; ; attempt++) {
    const res = await fetch(URL_BASE + path, {
      method,
      headers: { authorization: `Bearer ${KEY}`, "content-type": "application/json" },
      body: body === undefined || method === "GET" ? undefined : JSON.stringify(body),
    });
    if (res.ok) return res.json();
    const text = await res.text();
    if (attempt < 4 && (res.status >= 500 || res.status === 429)) {
      await sleep(400 * 2 ** attempt);
      continue;
    }
    throw new Error(`${method} ${path}: ${res.status} ${text.slice(0, 200)}`);
  }
}

function git(argv, stdin) {
  return new Promise((resolve, reject) => {
    const child = spawn("git", argv, { stdio: ["pipe", "pipe", "pipe"], env: { ...process.env, GIT_TERMINAL_PROMPT: "0" } });
    let err = "";
    child.stderr.on("data", (d) => (err += d));
    child.on("error", reject);
    child.stdin.on("error", () => {}); // git may exit before reading stdin; its exit code says why
    child.on("close", (code) => (code === 0 ? resolve() : reject(new Error(`git ${argv.find((a) => !a.startsWith("-") && !a.includes("=")) ?? ""} failed: ${err.trim().slice(0, 300)}`))));
    child.stdin.end(stdin ?? "");
  });
}

function auth(token) {
  return `http.extraHeader=Authorization: Bearer ${token}`;
}

function log(agent, status, detail = "", color = STATUS_COLOR[status] ?? "gray") {
  if (QUIET && !["landed", "conflict", "failed", "needs-human", "changes-requested"].includes(status)) return;
  const time = new Date().toLocaleTimeString("en-GB", { hour12: false });
  const c = COLOR[color] ?? "";
  process.stdout.write(`${COLOR.gray}${time}${COLOR.reset}  ${agent.padEnd(22)} ${c}${status.padEnd(17)}${COLOR.reset} ${COLOR.dim}${String(detail).slice(0, 110)}${COLOR.reset}\n`);
}

function banner(text) {
  process.stdout.write(`${COLOR.bold}${text}${COLOR.reset}\n\n`);
}

function summary() {
  const count = (fn) => results.filter(fn).length;
  const landed = results.filter((r) => r.outcome === "landed");
  const times = landed.map((r) => r.ms).sort((a, b) => a - b);
  const wall = (Date.now() - started) / 1000;
  process.stdout.write(
    `\n${COLOR.bold}${landed.length}/${results.length} landed in ${wall.toFixed(0)}s${COLOR.reset}` +
      `  ·  auto-merged ${count((r) => r.autoMerged)}  ·  reworked after conflicts ${count((r) => r.reworked)}` +
      `  ·  human-approved ${count((r) => r.outcome === "landed" && r.agent.startsWith("payments"))}` +
      `  ·  fixed by a fixer agent ${count((r) => r.fixed)}` +
      `  ·  failed ${count((r) => !["landed", "noop"].includes(r.outcome))}` +
      (times.length ? `  ·  median ${(times[Math.floor(times.length / 2)] / 1000).toFixed(1)}s per agent` : "") +
      "\n",
  );
  for (const r of results.filter((r) => !["landed", "noop"].includes(r.outcome))) {
    process.stdout.write(`  ${COLOR.red}${r.agent}: ${r.outcome}${r.error ? " — " + r.error : ""}${COLOR.reset}\n`);
  }
}

// interleave mixes task kinds so the run starts with a bit of everything.
function interleave(tasks) {
  const byKind = new Map();
  for (const t of tasks) {
    if (!byKind.has(t.kind)) byKind.set(t.kind, []);
    byKind.get(t.kind).push(t);
  }
  const out = [];
  while (out.length < tasks.length) {
    for (const list of byKind.values()) if (list.length) out.push(list.shift());
  }
  return out;
}

async function pool(items, size, fn) {
  let next = 0;
  await Promise.all(
    Array.from({ length: Math.min(size, items.length) }, async () => {
      while (next < items.length) {
        const i = next++;
        await fn(items[i], i);
      }
    }),
  );
}

function parseArgs(argv) {
  const out = {};
  for (let i = 0; i < argv.length; i++) {
    if (!argv[i].startsWith("--")) continue;
    const key = argv[i].slice(2);
    const value = argv[i + 1] && !argv[i + 1].startsWith("--") ? argv[++i] : true;
    out[key] = value;
  }
  return out;
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function fail(message) {
  console.error(message);
  process.exit(2);
}
