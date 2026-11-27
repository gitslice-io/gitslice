import assert from "node:assert/strict";
import { test } from "node:test";

import { Gitslice, GitsliceError, parseConnectErrorEnvelope } from "../src/gitslice.ts";
import { adoptHubBaseline, selectBaselineToFork, useHubBaseline, type BaselineHost } from "../src/hubBaseline.ts";
import {
  appendHubEvent,
  broadcastToSockets,
  buildHubSnapshot,
  computeHubStats,
  evictStaleSockets,
  MAX_EVENTS,
  parseStreamSliceFromUrl,
} from "../src/hubDashboard.ts";
import { findSessionForChange, isNativeEnabled, registerNativePatchset, watchNativeSlice, type NativeHost } from "../src/hubNative.ts";
import {
  findOverlappingSessions,
  globalPath,
  hasOverlapSignal,
  intersect,
  markSessionLanded,
  MAX_SIGNALS,
  overlapSignal,
  parseTitle,
  pathsOf,
  pushSignal,
  shortPath,
  shortRepo,
  type SessionLifecycleHost,
} from "../src/hubOverlap.ts";
import type { Baseline, HubEvent, Session } from "../src/types.ts";

function makeSession(overrides: Partial<Session> = {}): Session {
  const now = 1_700_000_000_000;
  return {
    id: "s.demo.store.agent-a.111111",
    agent: "agent-a",
    task: "update checkout",
    account: "demo",
    slice: "store",
    remote: "https://example.com/repo.git",
    baseline: { repo: "base.1", gitCommit: "aaa", nativeCommit: "sha256:11111111111111111111", tree: "ccc", createdAt: now },
    intent: ["/demo/store/src/cart.ts"],
    touched: [],
    status: "forked",
    pushes: 0,
    signals: [],
    createdAt: now,
    updatedAt: now,
    ...overrides,
  };
}

test("hubOverlap detects overlapping sessions, directory prefix intents, and signal caps", () => {
  const s1 = makeSession({ id: "s.1", agent: "alice", intent: ["/demo/store/src/cart/"] });
  const s2 = makeSession({ id: "s.2", agent: "bob", touched: ["/demo/store/src/cart/item.ts"], status: "pushed" });
  const s3 = makeSession({ id: "s.3", agent: "carol", intent: ["/demo/store/src/other.ts"], status: "pushed" });
  const sLanded = makeSession({ id: "s.4", agent: "dave", touched: ["/demo/store/src/cart/item.ts"], status: "landed" });
  const sFixer = makeSession({ id: "s.5", agent: "fixer-alice", fixerFor: "s.1", intent: ["/demo/store/src/cart/"] });

  assert.deepEqual(intersect(pathsOf(s1), pathsOf(s2)), ["/demo/store/src/cart/item.ts"]);
  const overlaps = findOverlappingSessions([s1, s2, s3, sLanded, sFixer], s1, pathsOf(s1));
  assert.deepEqual(overlaps.map((s) => s.id), ["s.2"]);

  const sig = overlapSignal(s2, ["/demo/store/src/cart/item.ts"]);
  pushSignal(s1, sig);
  assert.equal(hasOverlapSignal(s1, "s.2"), true);
  assert.equal(hasOverlapSignal(s1, "s.3"), false);

  for (let i = 0; i < MAX_SIGNALS + 10; i++) {
    pushSignal(s1, { kind: "info", message: `msg-${i}` });
  }
  assert.equal(s1.signals.length, MAX_SIGNALS);
  assert.equal(s1.signals.at(-1)?.message, `msg-${MAX_SIGNALS + 9}`);
  assert.equal(globalPath("demo/store/a.ts"), "/demo/store/a.ts");
  assert.equal(shortPath("/demo/store/src/cart.ts"), "src/cart.ts");
  assert.equal(shortRepo("a".repeat(40)).length, 34);
  assert.deepEqual(parseTitle("bot-1: Fix cart rounding", "fallback"), { agent: "bot-1", task: "Fix cart rounding" });
});

test("markSessionLanded schedules alarm recalculation to clear idle alarms", async () => {
  const s1 = makeSession({ id: "s.1", status: "landing" });
  let alarmCalls = 0;
  const host: SessionLifecycleHost = {
    env: {} as SessionLifecycleHost["env"],
    storage: {
      put: async () => {},
    },
    sessions: new Map([[s1.id, s1]]),
    baselineGen: 0,
    counters: { forks: 1, baselines: 1, pushes: 1 },
    saveSession: async () => {},
    recordEvent: () => {},
    broadcast: () => {},
    broadcastStats: () => {},
    scheduleAlarm: async () => {
      alarmCalls++;
    },
  };
  await markSessionLanded(host, s1.id, "sha256:abcdef0123456789");
  assert.equal(s1.status, "landed");
  assert.equal(alarmCalls, 1);
});

test("hubDashboard computes stats, caps events, evicts stale sockets, and parses stream URLs", () => {
  const s1 = makeSession({
    id: "s.1",
    agent: "alice",
    status: "landed",
    changesetId: "cs_1",
    pushedAt: 1000,
    landedAt: 4000,
    autoMerged: true,
    review: { verdict: "approve", summary: "ok", comments: [], model: "m" },
  });
  const s2 = makeSession({
    id: "s.2",
    agent: "bob",
    status: "reviewing",
    changesetId: "cs_2",
  });
  const stats = computeHubStats([s1, s2], { forks: 3, baselines: 2, pushes: 4 });
  assert.equal(stats.agents, 2);
  assert.equal(stats.landed, 1);
  assert.equal(stats.autoMerged, 1);
  assert.equal(stats.inFlight, 1);
  assert.equal(stats.medianLandMs, 3000);

  const events: HubEvent[] = [];
  for (let i = 0; i < MAX_EVENTS + 5; i++) {
    appendHubEvent(events, { kind: "pushed", message: `e-${i}` }, 1000 + i);
  }
  assert.equal(events.length, MAX_EVENTS);
  const snap = buildHubSnapshot([s2, s1], events, { forks: 3, baselines: 2, pushes: 4 }, null);
  assert.equal(snap.events.length, 120);

  const closed: number[] = [];
  const sent: string[] = [];
  const sockets = [
    { close: (code?: number) => closed.push(code ?? 0), send: (msg: string) => sent.push(msg) },
    { close: (code?: number) => closed.push(code ?? 0), send: (msg: string) => sent.push(msg) },
  ];
  evictStaleSockets(sockets, 2);
  assert.deepEqual(closed, [1008]);
  broadcastToSockets(sockets, { type: "ping" });
  assert.equal(sent.length, 2);
  assert.deepEqual(parseStreamSliceFromUrl("https://agents.gitslice.io/v1/slices/acme/store/stream"), {
    account: "acme",
    slice: "store",
  });
});

test("hubBaseline rotates baselines, retires old repos, and reuses baseline above floor", async () => {
  const puts: Array<unknown> = [];
  const host: BaselineHost = {
    env: {
      ARTIFACTS: {
        get: async () => ({
          log: async () => [
            {
              hash: "git123",
              treeHash: "tree123",
              message: "Commit\n\nGitslice-Commit: sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
            },
          ],
          [Symbol.dispose]: () => {},
        }),
        delete: async () => true,
      },
    } as unknown as BaselineHost["env"],
    storage: {
      put: async (...args: unknown[]) => {
        puts.push(args);
      },
    },
    sessions: new Map(),
    baseline: null,
    baselineGen: 1,
    baselineFor: -1,
    refreshing: null,
    lastImportAt: Date.now(),
    floor: 0,
    retired: [],
    counters: { forks: 0, baselines: 0, pushes: 0 },
    saveSession: async () => {},
    recordEvent: () => {},
    broadcast: () => {},
    broadcastStats: () => {},
  };

  const b1: Baseline = {
    repo: "base.acme.store.1",
    gitCommit: "g1",
    nativeCommit: "sha256:11111111111111111111",
    tree: "t1",
    createdAt: 2000,
  };
  await useHubBaseline(host, b1, 1, "imported");
  assert.equal(host.baseline?.repo, "base.acme.store.1");
  assert.equal(host.counters.baselines, 1);

  // Older baseline finishing late is retired without replacing b1.
  const bOld: Baseline = { ...b1, repo: "base.acme.store.old", createdAt: 1000 };
  const kept = await useHubBaseline(host, bOld, 1, "imported");
  assert.equal(kept.repo, "base.acme.store.1");
  assert.deepEqual(host.retired, ["base.acme.store.old"]);

  const adopted = await adoptHubBaseline(host, "base.acme.store.adopted");
  assert.equal(adopted.repo, "base.acme.store.adopted");
  assert.ok(host.retired.includes("base.acme.store.1"));

  const forked = await selectBaselineToFork(host, "acme", "store", false);
  assert.equal(forked.repo, "base.acme.store.adopted");
  assert.ok(puts.length >= 3);
});

test("hubNative registers native patchsets, matches fixer sessions, and respects NATIVE_SLICES", async () => {
  let alarmScheduled = 0;
  const host: NativeHost = {
    env: {
      NATIVE_SLICES: "demo/store, acme/core",
      GITSLICE_GIT: "https://git.gitslice.io",
      GITSLICE_API: "https://api.gitslice.io",
      GITSLICE_TOKEN: "tok",
    } as unknown as NativeHost["env"],
    storage: {
      put: async () => {},
    },
    sessions: new Map(),
    ident: null,
    nativeActiveAt: 0,
    nativeQueued: new Map(),
    nativePollAt: 0,
    nativeSince: 0,
    counters: { forks: 0, baselines: 0, pushes: 0 },
    saveSession: async (s) => {
      host.sessions.set(s.id, s);
    },
    landed: async () => {},
    recordEvent: () => {},
    broadcast: () => {},
    broadcastStats: () => {},
    scheduleAlarm: async () => {
      alarmScheduled++;
    },
  };

  await watchNativeSlice(host, "demo", "store", true);
  assert.equal(isNativeEnabled(host.env, host.ident), true);
  assert.equal(alarmScheduled, 1);

  const created = await registerNativePatchset(host, {
    account: "demo",
    slice: "store",
    changeset: { id: "cs_abcd1234", handle: "abcd1234", title: "coder-1: Add checkout retry", baseCommitId: "sha256:base" },
    patchset: { id: "ps_1", number: "1", createdAtMs: 5000, changedPaths: ["demo/store/src/retry.ts", "other/slice/ignore.ts"] },
  });
  assert.ok(created);
  assert.equal(created.agent, "coder-1");
  assert.deepEqual(created.touched, ["/demo/store/src/retry.ts"]);
  assert.equal(await registerNativePatchset(host, {
    account: "demo",
    slice: "store",
    changeset: { id: "cs_abcd1234", handle: "abcd1234", title: "coder-1: Add checkout retry", baseCommitId: "sha256:base" },
    patchset: { id: "ps_1", number: "1", createdAtMs: 5000, changedPaths: ["demo/store/src/retry.ts"] },
  }), null);

  assert.equal(findSessionForChange(host.sessions.values(), "abcd1234")?.id, created.id);
});

test("GitsliceError parses Connect JSON error envelopes (code, message, details, status) and fallbacks", async () => {
  const envelopeText = JSON.stringify({
    code: "failed_precondition",
    message: "changeset has stale path base",
    details: [
      {
        type: "gitslice.core.v1.SubmitRequirementFailure",
        value: "CgxjaGVja19mYWlsZWQ=",
        debug: { reason: "required approvals missing" },
      },
    ],
  });

  const parsed = parseConnectErrorEnvelope(envelopeText, "fallback");
  assert.equal(parsed.code, "failed_precondition");
  assert.equal(parsed.message, "changeset has stale path base");
  assert.equal(parsed.details.length, 1);
  assert.equal(parsed.details[0].type, "gitslice.core.v1.SubmitRequirementFailure");

  const err = GitsliceError.fromResponse(400, envelopeText);
  assert.equal(err.httpStatus, 400);
  assert.equal(err.status, 400);
  assert.equal(err.code, "failed_precondition");
  assert.equal(err.conflict, true);
  assert.equal(err.details.length, 1);

  const plainErr = GitsliceError.fromResponse(502, "Bad Gateway");
  assert.equal(plainErr.code, "unknown");
  assert.equal(plainErr.message, "Bad Gateway");
  assert.equal(plainErr.status, 502);
  assert.deepEqual(plainErr.details, []);

  const emptyErr = GitsliceError.fromResponse(500, "   ", "ChangesetService/SubmitChangeset: HTTP 500");
  assert.equal(emptyErr.code, "unknown");
  assert.equal(emptyErr.message, "ChangesetService/SubmitChangeset: HTTP 500");

  const origFetch = globalThis.fetch;
  try {
    globalThis.fetch = async () =>
      new Response(envelopeText, {
        status: 412,
        headers: { "content-type": "application/json" },
      });
    const client = new Gitslice("https://api.gitslice.io", "secret-token");
    await assert.rejects(
      () => client.submit("cs_1", "ps_1"),
      (thrown: unknown) => {
        assert.ok(thrown instanceof GitsliceError);
        assert.equal(thrown.status, 412);
        assert.equal(thrown.code, "failed_precondition");
        assert.equal(thrown.conflict, true);
        assert.equal(thrown.details.length, 1);
        return true;
      },
    );
  } finally {
    globalThis.fetch = origFetch;
  }
});
