import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { dirname, resolve } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import { dashboardHtml } from "../src/dashboard.ts";
import {
  authorized,
  capRetiredList,
  computeNextAlarmMs,
  corsHeaders,
  filterSlicePaths,
  NATIVE_BUSY_WINDOW_MS,
  NATIVE_POLL_BUSY_MS,
  NATIVE_POLL_IDLE_MS,
  normalizeSlicePath,
  protectedPatterns,
  pruneNativeQueued,
  pruneSessionsMap,
  readAuthRequired,
  redactSession,
} from "../src/policy.ts";
import type { Session } from "../src/types.ts";

function makeSession(overrides: Partial<Session> = {}): Session {
  const now = 1_700_000_000_000;
  return {
    id: "sess.demo.store.agent.123456",
    agent: "agent",
    task: "update copy",
    account: "demo",
    slice: "store",
    remote: "https://example.com/repo.git",
    baseline: { repo: "base.1", gitCommit: "aaa", nativeCommit: "bbb", tree: "ccc", createdAt: now },
    intent: [],
    touched: [],
    status: "landed",
    pushes: 1,
    signals: [],
    createdAt: now,
    updatedAt: now,
    ...overrides,
  };
}

test("authorized compares comma-separated API keys >= 16 chars in constant time", async () => {
  const env = { AGENTS_API_KEY: "short, valid-secret-key-alpha-1234, valid-secret-key-bravo-5678" };
  const req = (token?: string) =>
    new Request("https://agents.gitslice.io/v1/sessions", {
      headers: token ? { authorization: `Bearer ${token}` } : {},
    });

  assert.equal(await authorized(req("valid-secret-key-alpha-1234"), env), true);
  assert.equal(await authorized(req("valid-secret-key-bravo-5678"), env), true);
  assert.equal(await authorized(req("short"), env), false);
  assert.equal(await authorized(req("valid-secret-key-alpha-1235"), env), false);
  assert.equal(await authorized(req(), env), false);
});

test("readAuthRequired, corsHeaders, and redactSession protect sensitive data", () => {
  assert.equal(readAuthRequired({}), false);
  assert.equal(readAuthRequired({ AGENTS_READ_AUTH: "false" }), false);
  assert.equal(readAuthRequired({ AGENTS_READ_AUTH: "1" }), true);

  const sameOriginReq = new Request("https://agents.gitslice.io/v1/sessions", {
    headers: { origin: "https://gitslice.io" },
  });
  assert.equal(
    corsHeaders(sameOriginReq, { GITSLICE_WEB: "https://gitslice.io" })["access-control-allow-origin"],
    "https://gitslice.io",
  );
  const crossOriginReq = new Request("https://agents.gitslice.io/v1/sessions", {
    headers: { origin: "https://evil.example" },
  });
  assert.equal(
    corsHeaders(crossOriginReq, { GITSLICE_WEB: "https://gitslice.io" })["access-control-allow-origin"],
    undefined,
  );

  const session = makeSession({
    status: "conflict",
    signals: [
      {
        at: 1,
        kind: "conflict",
        message: "Overlapping edits",
        data: { paths: ["/demo/store/secret.ts"], hunks: [{ path: "/demo/store/secret.ts", ours: ["const KEY = 'x'"] }] },
      },
    ],
  });
  const redacted = redactSession(session);
  assert.deepEqual(redacted.signals[0].data, { paths: ["/demo/store/secret.ts"] });
  assert.ok("hunks" in (session.signals[0].data as Record<string, unknown>));
});

test("protectedPatterns always escalates /.gitslice/** and **/.gitslice/** changes", () => {
  const patterns = protectedPatterns({ PROTECTED_PATHS: "/src/checkout/payments*" });
  const matches = (path: string) => patterns.some((re) => re.test(path));

  assert.equal(matches("/.gitslice/config.json"), true);
  assert.equal(matches("/demo/store/.gitslice/checks.yaml"), true);
  assert.equal(matches("/demo/store/nested/.gitslice/triggers/deploy.ts"), true);
  assert.equal(matches(".gitslice/hooks.json"), true);
  assert.equal(matches("/demo/store/src/checkout/payments.ts"), true);
  assert.equal(matches("/demo/store/src/catalog/items.ts"), false);
  assert.equal(matches("/demo/store/docs/gitslice-notes.md"), false);
});

test("normalizeSlicePath and filterSlicePaths enforce slice prefix and block traversal and .gitslice in fixer", () => {
  assert.equal(
    normalizeSlicePath("/demo/store/src/new-dir/item.ts", "demo", "store", { allowDotGitslice: false }),
    "demo/store/src/new-dir/item.ts",
  );
  assert.equal(normalizeSlicePath("other/slice/src/item.ts", "demo", "store", { allowDotGitslice: false }), null);
  assert.equal(normalizeSlicePath("demo/store/../../etc/passwd", "demo", "store", { allowDotGitslice: false }), null);
  assert.equal(normalizeSlicePath("demo/store/.git/config", "demo", "store", { allowDotGitslice: true }), null);
  assert.equal(normalizeSlicePath("demo/store/.gitslice/checks.yaml", "demo", "store", { allowDotGitslice: false }), null);
  assert.equal(
    normalizeSlicePath("demo/store/.gitslice/checks.yaml", "demo", "store", { allowDotGitslice: true, leadingSlash: true }),
    "/demo/store/.gitslice/checks.yaml",
  );

  const filtered = filterSlicePaths(
    ["/demo/store/a.ts", "/other/slice/b.ts", "/demo/store/../escape.ts", "/demo/store/.gitslice/c.yaml"],
    "demo",
    "store",
    { allowDotGitslice: false },
  );
  assert.deepEqual(filtered, ["demo/store/a.ts"]);
});

test("computeNextAlarmMs scales to zero when idle and wakes only when active or watched", () => {
  const now = 1_700_000_500_000;

  // Completely idle native-enabled Hub with 0 WebSockets and only landed sessions -> null (NO 20s alarm!).
  assert.equal(
    computeNextAlarmMs({
      now,
      sessions: [makeSession({ status: "landed", native: true, updatedAt: now - 300_000 })],
      nativeEnabled: true,
      nativeActiveAt: now - NATIVE_BUSY_WINDOW_MS - 1_000,
      openWebSockets: 0,
    }),
    null,
  );

  // Within NATIVE_BUSY_WINDOW_MS -> polls at NATIVE_POLL_BUSY_MS.
  assert.equal(
    computeNextAlarmMs({
      now,
      sessions: [],
      nativeEnabled: true,
      nativeActiveAt: now - 5_000,
      openWebSockets: 0,
    }),
    now + NATIVE_POLL_BUSY_MS,
  );

  // Open dashboard WebSocket -> polls at NATIVE_POLL_IDLE_MS while viewer is connected.
  assert.equal(
    computeNextAlarmMs({
      now,
      sessions: [],
      nativeEnabled: true,
      nativeActiveAt: 0,
      openWebSockets: 1,
    }),
    now + NATIVE_POLL_IDLE_MS,
  );

  // Native needs-human session past busy window and past watch window with 0 viewers -> scales to zero!
  assert.equal(
    computeNextAlarmMs({
      now,
      sessions: [makeSession({ status: "needs-human", native: true, updatedAt: now - 20 * 60_000 })],
      nativeEnabled: true,
      nativeActiveAt: 0,
      openWebSockets: 0,
    }),
    null,
  );
});

test("pruneSessionsMap, pruneNativeQueued, and capRetiredList bound Hub memory and DO storage keys", () => {
  const map = new Map<string, Session>();
  for (let i = 0; i < 6; i++) {
    map.set(
      `s${i}`,
      makeSession({
        id: `s${i}`,
        status: i < 4 ? "landed" : "reviewing",
        updatedAt: 1000 + i,
      }),
    );
  }
  const evicted = pruneSessionsMap(map, 3);
  assert.equal(map.size, 3);
  assert.deepEqual(
    evicted.map((s) => s.id),
    ["s0", "s1", "s2"],
  );
  assert.ok(map.has("s4") && map.has("s5"));

  const queued = new Map<string, number>([
    ["old:1", 100],
    ["fresh:1", 995_000],
    ["fresh:2", 996_000],
    ["fresh:3", 997_000],
  ]);
  pruneNativeQueued(queued, 1_000_000, 60_000, 2);
  assert.deepEqual([...queued.keys()], ["fresh:2", "fresh:3"]);

  const retired = ["r0", "r1", "base.current", "r2", "r3"];
  const overflow = capRetiredList(retired, "base.current", 2);
  assert.equal(retired.length, 2);
  assert.deepEqual(overflow, ["r0", "r1"]);
});

test("dashboardHtml avoids perpetual setInterval timers and publish-baseline.sh avoids argv token leaks", () => {
  const html = dashboardHtml({
    account: "demo",
    slice: "store",
    gitsliceWeb: "https://gitslice.io",
    gitsliceGit: "https://gitslice.io/git",
  });
  assert.ok(!html.includes("setInterval("));
  assert.ok(html.includes("visibilitychange"));

  const here = dirname(fileURLToPath(import.meta.url));
  const scriptPath = resolve(here, "../scripts/publish-baseline.sh");
  execFileSync("bash", ["-n", scriptPath]);
});
