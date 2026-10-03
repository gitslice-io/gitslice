#!/usr/bin/env node
// The same agents as swarm.mjs, without Cloudflare Artifacts or the Worker:
// they talk to Gitslice's own Git endpoint.
//
//   clone      git clone https://gitslice.io/git/<slice>.git
//   push       git push origin HEAD:refs/changes/new   → a changeset
//   review     a stand-in reviewer (deterministic checks only, no AI) approves
//   submit     gs cs submit; Gitslice validates every path against its base
//   stale base git fetch + git rebase (Git merges disjoint hunks itself), then
//              git push origin HEAD:refs/changes/<id> for another patchset
//   a real conflict: start again from the new head and redo the task
//
//   node swarm/native.mjs [--slice demo/storefront] [--concurrency 24] [--reset]
//       [--only i18n,…|agent,…] [--skip agent,…] [--quiet]
//
// Identities: the agents author changesets as the bridge identity; a different
// identity approves them, because Gitslice does not let an author approve its
// own change. Both come from `gs` config directories:
//   GITSLICE_AGENT_HOME     (default ~/.config/gitslice-agents-bridge)
//   GITSLICE_REVIEWER_HOME  (default ~/.config/gitslice-operator)
//   GS_BIN                  the gs CLI (default: gs)
// Tasks that write with a model (docs, copy) need AGENTS_API_KEY for the
// agents Worker's /v1/assist; without it they are skipped. Tasks that rely on a
// fixer agent need the Worker and are not part of this mode.

import { spawn } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { homedir, tmpdir } from "node:os";
import { join } from "node:path";
import { storefrontTasks } from "./storefront.mjs";

const args = parseArgs(process.argv.slice(2));
const SLICE = args.slice ?? "demo/storefront";
const GIT_URL = `${args["git-url"] ?? "https://gitslice.io/git"}/${SLICE}.git`;
const CONCURRENCY = Number(args.concurrency ?? 24);
const STAGGER_MS = Number(args.stagger ?? 120);
const ONLY = args.only ? new Set(String(args.only).split(",")) : null;
const SKIP = new Set(String(args.skip ?? "").split(",").filter(Boolean));
const QUIET = Boolean(args.quiet);
const HUMAN_WAIT_MS = Number(args["human-wait"] ?? 15 * 60_000);
const AGENT_HOME = process.env.GITSLICE_AGENT_HOME ?? join(homedir(), ".config/gitslice-agents-bridge");
const REVIEWER_HOME = process.env.GITSLICE_REVIEWER_HOME ?? join(homedir(), ".config/gitslice-operator");
const GS = process.env.GS_BIN ?? "gs";
const AGENTS_URL = args.url ?? process.env.AGENTS_URL ?? "https://agents.gitslice.io";
const AGENTS_KEY = process.env.AGENTS_API_KEY ?? "";
const TOKEN = JSON.parse(await readFile(join(AGENT_HOME, ".gitslice/config.json"), "utf8")).token;

const COLOR = { reset: "\x1b[0m", dim: "\x1b[2m", bold: "\x1b[1m", green: "\x1b[32m", red: "\x1b[31m", yellow: "\x1b[33m", magenta: "\x1b[35m", cyan: "\x1b[36m", blue: "\x1b[34m", gray: "\x1b[90m" };
const STATUS_COLOR = { cloned: "gray", pushed: "blue", approved: "cyan", submitted: "cyan", landed: "green", rebased: "yellow", reworked: "yellow", waiting: "yellow", rejected: "red", failed: "red" };

if (args.reset) {
  if (!AGENTS_KEY) fail("--reset needs AGENTS_API_KEY");
  const r = await agentsApi("POST", `/v1/slices/${SLICE}/restore`, {});
  process.stdout.write(`${r.changed ? `restored ${r.changed} files` : "already at the seed"}; deleted ${r.deleted} repos from the last run\n\n`);
}

const results = [];
const started = Date.now();
const tasks = interleave(
  storefrontTasks(SLICE).filter((t) => (!ONLY || ONLY.has(t.kind) || ONLY.has(t.agent)) && !SKIP.has(t.agent) && t.kind !== "fixer" && (AGENTS_KEY || !t.model)),
);
banner(`${tasks.length} agents → ${GIT_URL} (concurrency ${CONCURRENCY}, no Artifacts)`);
await pool(tasks, CONCURRENCY, async (task, i) => {
  await sleep(i < CONCURRENCY ? i * STAGGER_MS : 0);
  results.push(await runAgent(task).catch((err) => ({ agent: task.agent, outcome: "error", error: String(err.message ?? err).slice(0, 300) })));
});
summary();

async function runAgent(task) {
  const t0 = Date.now();
  const dir = await mkdtemp(join(tmpdir(), "native-"));
  // git(...args, { input }) runs git in the agent's checkout, as the agent.
  const git = (...a) => {
    const opts = typeof a.at(-1) === "object" ? a.pop() : {};
    return run("git", ["-c", `http.extraHeader=Authorization: Bearer ${TOKEN}`, "-c", `user.name=${task.agent}`, "-c", `user.email=${task.agent}@agents.gitslice.io`, ...a], dir, opts);
  };
  let rebases = 0;
  let reworks = 0;
  try {
    const clone = await run("git", ["-c", `http.extraHeader=Authorization: Bearer ${TOKEN}`, "clone", "-q", GIT_URL, dir]);
    if (clone.code) return { agent: task.agent, outcome: "failed", error: `clone: ${clone.err.trim().slice(0, 160)}` };
    log(task.agent, "cloned");
    const message = (change) => [change.subject, "", change.body, "", `Agent: ${task.agent}`, `Task: ${task.task}`].join("\n");
    let change = await task.apply(dir, { assist });
    if (!change) return { agent: task.agent, outcome: "noop" };
    await git("add", "-A");
    await git("commit", "-q", "-F", "-", { input: message(change) });

    // Push; the server turns it into a changeset.
    let target = "HEAD:refs/changes/new";
    let id = null;
    const tPush = Date.now();
    for (let attempt = 0; attempt < 8; attempt++) {
      const push = await git("push", "origin", target);
      const m = /changeset (\w+) patchset/.exec(push.err + push.out);
      if (push.code === 0 && m) {
        id = m[1];
        break;
      }
      if (/NEEDS_REBASE|fetch first|non-fast-forward/.test(push.err)) {
        await syncOnto(git, dir, task, () => change);
      } else if (attempt === 7) {
        return { agent: task.agent, outcome: "failed", error: `push: ${push.err.trim().slice(-200)}` };
      } else {
        await sleep(1000 * (attempt + 1));
      }
    }
    if (!id) return { agent: task.agent, outcome: "failed", error: "push never produced a changeset" };
    log(task.agent, "pushed", `${id}  ${change.subject}`);
    target = `HEAD:refs/changes/${id}`;

    // Review, then submit, until it lands. A stale base is rebased.
    const deadline = Date.now() + HUMAN_WAIT_MS;
    let needsApproval = true;
    let fixed = false;
    while (Date.now() < deadline) {
      if (needsApproval) {
        const verdict = await review(task, dir, id);
        if (verdict.kind === "reject") {
          log(task.agent, "rejected", verdict.reason, "red");
          if (!task.fix || fixed) return { agent: task.agent, outcome: "rejected", reason: verdict.reason };
          // The agent answers its review: fix, push another patchset, ask again.
          fixed = true;
          const fix = await task.fix(dir);
          await git("add", "-A");
          await git("commit", "-q", "-F", "-", { input: [fix.subject, "", fix.body, "", `Agent: ${task.agent}`, `Task: ${task.task}`].join("\n") });
          const update = await git("push", "origin", target);
          if (update.code) return { agent: task.agent, outcome: "failed", error: `fix push: ${update.err.trim().slice(-200)}` };
          log(task.agent, "pushed", `${id}  ${fix.subject}`);
          continue;
        }
        if (verdict.kind === "approved") {
          log(task.agent, "approved", id);
          needsApproval = false;
        } else {
          log(task.agent, "waiting", "for a person to approve in Gitslice", "yellow");
          needsApproval = false; // a person approves; keep submitting
        }
      }
      const submit = await run(GS, ["cs", "submit", id], tmpdir(), { env: { HOME: AGENT_HOME } });
      const text = submit.err + submit.out;
      if (submit.code === 0 && /submitted/.test(text)) {
        log(task.agent, "landed", `${id}  +${((Date.now() - t0) / 1000).toFixed(1)}s${rebases ? "  rebased" : ""}${reworks ? "  reworked" : ""}`);
        return { agent: task.agent, outcome: "landed", ms: Date.now() - t0, pushToLandedMs: Date.now() - tPush, rebases, reworks };
      }
      if (/path base conflict|stale|rebase/i.test(text)) {
        const how = await syncOnto(git, dir, task, () => change);
        if (how === "rebased") rebases++;
        else {
          reworks++;
          change = how.change;
        }
        log(task.agent, how === "rebased" ? "rebased" : "reworked", how === "rebased" ? "onto the latest main" : "on the latest main after a conflict", "yellow");
        const push = await git("push", "origin", target);
        if (push.code) return { agent: task.agent, outcome: "failed", error: `update push: ${push.err.trim().slice(-200)}` };
        needsApproval = true; // a new patchset needs a new approval
        continue;
      }
      if (/required approval|approval/i.test(text)) {
        await sleep(2500);
        continue;
      }
      await sleep(1200);
    }
    return { agent: task.agent, outcome: "timeout" };
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

// syncOnto brings the agent's commit onto the latest main. Git merges edits to
// different parts of a file itself; when it cannot, the agent starts again from
// main and redoes its task (a rework).
async function syncOnto(git, dir, task, currentChange) {
  await git("fetch", "-q", "origin", "main");
  const rebase = await git("rebase", "origin/main");
  if (rebase.code === 0) return "rebased";
  await git("rebase", "--abort");
  await git("reset", "-q", "--hard", "origin/main");
  const change = await task.apply(dir, { assist });
  if (!change) throw new Error("nothing left to change after the rework: the change is already in main");
  await git("add", "-A");
  await git("commit", "-q", "-F", "-", { input: [change.subject, "", change.body, "", `Agent: ${task.agent}`, `Task: ${task.task}`].join("\n") });
  return { change };
}

// review is the stand-in reviewer: deterministic checks only. It approves as a
// different identity than the author, as Gitslice requires.
async function review(task, dir, id) {
  const files = await run("git", ["diff", "--name-only", "origin/main...HEAD"], dir);
  const changed = files.out.split("\n").filter(Boolean);
  for (const path of changed.filter((p) => p.endsWith(".json"))) {
    try {
      const parsed = JSON.parse(await readFile(join(dir, path), "utf8"));
      if (/\/src\/catalog\/products\//.test(path)) {
        const bad = ["sku", "name", "priceCents", "currency", "tags", "stock"].filter((k) => parsed[k] === undefined);
        if (bad.length) return { kind: "reject", reason: `${path} is missing ${bad.join(", ")}` };
      }
    } catch (err) {
      return { kind: "reject", reason: `${path} is not valid JSON` };
    }
  }
  if (changed.some((p) => /\/checkout\/payments/.test(p))) return { kind: "human" };
  const approve = await run(GS, ["cs", "approve", id], tmpdir(), { env: { HOME: REVIEWER_HOME } });
  if (approve.code) throw new Error(`approve ${id}: ${(approve.err + approve.out).trim().slice(-160)}`);
  return { kind: "approved" };
}

async function assist(instructions, input) {
  const res = await agentsApi("POST", "/v1/assist", { instructions, input });
  return res.text;
}

async function agentsApi(method, path, body) {
  const res = await fetch(AGENTS_URL + path, {
    method,
    headers: { authorization: `Bearer ${AGENTS_KEY}`, "content-type": "application/json" },
    body: body === undefined || method === "GET" ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${(await res.text()).slice(0, 200)}`);
  return res.json();
}

function run(cmd, argv, cwd, { input, env } = {}) {
  return new Promise((resolve) => {
    const child = spawn(cmd, argv, { cwd, env: { ...process.env, ...(env ?? {}), GIT_TERMINAL_PROMPT: "0" } });
    let out = "";
    let err = "";
    child.stdout.on("data", (d) => (out += d));
    child.stderr.on("data", (d) => (err += d));
    child.on("error", (e) => resolve({ code: 1, out, err: String(e) }));
    child.stdin.on("error", () => {});
    child.on("close", (code) => resolve({ code, out, err }));
    child.stdin.end(input ?? "");
  });
}

function log(agent, status, detail = "", color = STATUS_COLOR[status] ?? "gray") {
  if (QUIET && !["landed", "rejected", "failed"].includes(status)) return;
  const time = new Date().toLocaleTimeString("en-GB", { hour12: false });
  process.stdout.write(`${COLOR.gray}${time}${COLOR.reset}  ${agent.padEnd(22)} ${COLOR[color] ?? ""}${status.padEnd(10)}${COLOR.reset} ${COLOR.dim}${String(detail).slice(0, 110)}${COLOR.reset}\n`);
}

function banner(text) {
  process.stdout.write(`${COLOR.bold}${text}${COLOR.reset}\n\n`);
}

function summary() {
  const landed = results.filter((r) => r.outcome === "landed");
  const times = landed.map((r) => r.ms).sort((a, b) => a - b);
  const push = landed.map((r) => r.pushToLandedMs).sort((a, b) => a - b);
  const wall = (Date.now() - started) / 1000;
  const sum = (key) => landed.reduce((a, r) => a + (r[key] ?? 0), 0);
  process.stdout.write(
    `\n${COLOR.bold}${landed.length}/${results.length} landed in ${wall.toFixed(0)}s${COLOR.reset}` +
      `  ·  rebased ${landed.filter((r) => r.rebases).length} (${sum("rebases")} rebases)  ·  reworked ${landed.filter((r) => r.reworks).length}` +
      `  ·  rejected ${results.filter((r) => r.outcome === "rejected").length}  ·  failed ${results.filter((r) => !["landed", "noop", "rejected"].includes(r.outcome)).length}` +
      (times.length ? `  ·  median ${(times[Math.floor(times.length / 2)] / 1000).toFixed(1)}s per agent, ${(push[Math.floor(push.length / 2)] / 1000).toFixed(1)}s push → landed` : "") +
      "\n",
  );
  for (const r of results.filter((r) => !["landed", "noop"].includes(r.outcome))) {
    process.stdout.write(`  ${COLOR.red}${r.agent}: ${r.outcome}${r.error ?? r.reason ? " — " + (r.error ?? r.reason) : ""}${COLOR.reset}\n`);
  }
}

function interleave(list) {
  const byKind = new Map();
  for (const t of list) {
    if (!byKind.has(t.kind)) byKind.set(t.kind, []);
    byKind.get(t.kind).push(t);
  }
  const out = [];
  while (out.length < list.length) for (const l of byKind.values()) if (l.length) out.push(l.shift());
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
    out[key] = argv[i + 1] && !argv[i + 1].startsWith("--") ? argv[++i] : true;
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
