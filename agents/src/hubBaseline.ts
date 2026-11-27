// Baseline repository import, adoption, rotation, retirement, and session forking for Hub.

import { nativeCommitOf, sessionRepoName } from "./artifacts.ts";
import type { Env } from "./env.ts";
import type { HubCounters } from "./hubDashboard.ts";
import { HUB_KEYS } from "./hubKeys.ts";
import {
  disposeRepo,
  findOverlappingSessions,
  globalPath,
  intersect,
  mapLimit,
  nonce,
  overlapSignal,
  pathsOf,
  pushSignal,
  shortRepo,
} from "./hubOverlap.ts";
import { capRetiredList } from "./policy.ts";
import type { Baseline, HubEvent, Session } from "./types.ts";

export interface OpenSessionInput {
  account: string;
  slice: string;
  agent: string;
  task: string;
  intent?: string[];
  resume?: string;
}

export const MIN_IMPORT_INTERVAL_MS = 4000;

export interface BaselineStorage {
  put(key: string, value: unknown): Promise<void>;
  put(entries: Record<string, unknown>): Promise<void>;
}

export interface BaselineHost {
  readonly env: Env;
  readonly storage: BaselineStorage;
  readonly sessions: Map<string, Session>;
  baseline: Baseline | null;
  baselineGen: number;
  baselineFor: number;
  refreshing: Promise<Baseline> | null;
  lastImportAt: number;
  floor: number;
  retired: string[];
  counters: HubCounters;
  saveSession(session: Session): Promise<void>;
  recordEvent(event: Omit<HubEvent, "at">): void;
  broadcast(message: unknown): void;
  broadcastStats(): void;
}

export function retireBaselineRepo(host: Pick<BaselineHost, "env" | "retired" | "baseline">, repo: string): void {
  if (!host.retired.includes(repo)) host.retired.push(repo);
  const overflow = capRetiredList(host.retired, host.baseline?.repo);
  if (overflow.length > 0) {
    mapLimit(overflow, 4, (name) => host.env.ARTIFACTS.delete(name).catch(() => {})).catch(() => {});
  }
}

export async function useHubBaseline(host: BaselineHost, baseline: Baseline, gen: number, how: string): Promise<Baseline> {
  if (host.baseline && baseline.createdAt < host.baseline.createdAt) {
    // An import that started earlier finished later: keep the newer one.
    retireBaselineRepo(host, baseline.repo);
    await host.storage.put(HUB_KEYS.retired, host.retired);
    return host.baseline;
  }
  if (host.baseline && host.baseline.repo !== baseline.repo) {
    retireBaselineRepo(host, host.baseline.repo);
  }
  host.baseline = baseline;
  host.baselineFor = Math.max(host.baselineFor, gen);
  host.counters.baselines++;
  await host.storage.put({
    baseline,
    baselineFor: host.baselineFor,
    counters: host.counters,
    retired: host.retired,
  });
  host.recordEvent({
    kind: "baseline",
    message: `${how} baseline ${shortRepo(baseline.repo)} at ${baseline.nativeCommit.slice(7, 19)}`,
  });
  host.broadcast({ type: "baseline", baseline });
  host.broadcastStats();
  return baseline;
}

// importFailedBaseline keeps agents working on the last baseline: a stale
// baseline only means more merging.
export function importFailedBaseline(host: BaselineHost, err: unknown, gen: number): Baseline {
  console.warn(JSON.stringify({ msg: "baseline import failed", error: String(err) }));
  host.baselineFor = Math.max(host.baselineFor, gen);
  host.recordEvent({ kind: "baseline", message: `baseline import failed; reusing ${shortRepo(host.baseline!.repo)}` });
  return host.baseline!;
}

// importHubBaseline asks Artifacts to import the slice straight from Gitslice's
// Git endpoint. The projection is deterministic, so the baseline's commits
// are the same commits every Gitslice clone sees.
export async function importHubBaseline(env: Env, account: string, slice: string): Promise<Baseline> {
  const started = Date.now();
  const name = `base.${account}.${slice}.${started.toString(36)}${nonce().slice(0, 3)}`;
  const depth = Number(env.BASELINE_DEPTH || 0) || undefined;
  await env.ARTIFACTS.import({
    source: { url: `${env.GITSLICE_GIT}/${account}/${slice}.git`, ...(depth ? { depth } : {}) },
    target: { name, opts: { readOnly: true, description: `Baseline of ${account}/${slice}` } },
  });
  for (let attempt = 0; attempt < 40; attempt++) {
    try {
      const repo = await env.ARTIFACTS.get(name);
      try {
        const [head] = await repo.log({ limit: 1 });
        if (head) {
          const nativeCommit = nativeCommitOf(head.message);
          if (!nativeCommit) throw new Error(`baseline head ${head.hash} has no Gitslice-Commit trailer`);
          return { repo: name, gitCommit: head.hash, nativeCommit, tree: head.treeHash, createdAt: started };
        }
      } finally {
        disposeRepo(repo);
      }
    } catch (err) {
      if (String(err).includes("Gitslice-Commit")) throw err;
    }
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  throw new Error(`baseline ${name} did not become ready`);
}

export async function adoptHubBaseline(host: BaselineHost, repoName: string): Promise<Baseline> {
  const repo = await host.env.ARTIFACTS.get(repoName);
  try {
    const [head] = await repo.log({ limit: 1 });
    if (!head) throw new Error(`${repoName} has no commits`);
    const nativeCommit = nativeCommitOf(head.message);
    if (!nativeCommit) throw new Error(`${repoName} head ${head.hash} has no Gitslice-Commit trailer`);
    return useHubBaseline(
      host,
      { repo: repoName, gitCommit: head.hash, nativeCommit, tree: head.treeHash, createdAt: Date.now() },
      host.baselineGen,
      "adopted",
    );
  } finally {
    disposeRepo(repo);
  }
}

export function refreshHubBaseline(host: BaselineHost, account: string, slice: string): Promise<Baseline> {
  if (host.refreshing) return host.refreshing;
  if (host.baseline && Date.now() - host.lastImportAt < MIN_IMPORT_INTERVAL_MS) {
    return Promise.resolve(host.baseline);
  }
  const gen = host.baselineGen;
  host.lastImportAt = Date.now();
  host.refreshing = importHubBaseline(host.env, account, slice)
    .then((baseline) => useHubBaseline(host, baseline, gen, "imported"))
    .catch((err) => {
      if (!host.baseline) throw err;
      return importFailedBaseline(host, err, gen);
    })
    .finally(() => {
      host.refreshing = null;
    });
  return host.refreshing;
}

// selectBaselineToFork picks the baseline a new session forks. New sessions
// never wait for an import when a baseline exists: a slightly stale one only
// means the bridge merges more, and the import runs in the background. A
// rework waits for a fresh import, because it has to see the change it
// conflicted with.
export async function selectBaselineToFork(
  host: BaselineHost,
  account: string,
  slice: string,
  fresh: boolean,
): Promise<Baseline> {
  if (fresh) {
    const gen = host.baselineGen;
    try {
      return await useHubBaseline(host, await importHubBaseline(host.env, account, slice), gen, "imported");
    } catch (err) {
      if (!host.baseline) throw err;
      return importFailedBaseline(host, err, gen);
    }
  }
  if (host.baseline && host.baselineFor === host.baselineGen) return host.baseline;
  const refresh = refreshHubBaseline(host, account, slice);
  return host.baseline && host.baseline.createdAt >= host.floor ? host.baseline : refresh;
}

export async function forkHubSession(
  host: BaselineHost,
  input: OpenSessionInput,
): Promise<{ session: Session; token: string }> {
  const previous = input.resume ? host.sessions.get(input.resume) : undefined;
  const baseline = await selectBaselineToFork(host, input.account, input.slice, Boolean(previous));
  const name = sessionRepoName(input.account, input.slice, input.agent, nonce());

  let remote = "";
  let token = "";
  const base = await host.env.ARTIFACTS.get(baseline.repo);
  try {
    const forked = await base.fork(name, {
      defaultBranchOnly: true,
      readOnly: false,
      description: `${input.agent}: ${input.task}`.slice(0, 200),
    });
    remote = forked.remote;
    token = forked.token ?? "";
    if (!token) {
      const repo = await host.env.ARTIFACTS.get(name);
      try {
        token = (await repo.createToken("write", 4 * 3600)).plaintext;
      } finally {
        disposeRepo(repo);
      }
    }
  } finally {
    disposeRepo(base);
  }

  const now = Date.now();
  const session: Session = {
    id: name,
    agent: input.agent,
    task: input.task,
    account: input.account,
    slice: input.slice,
    remote,
    baseline,
    intent: (input.intent ?? []).map(globalPath),
    touched: [],
    status: "forked",
    pushes: 0,
    signals: [],
    createdAt: now,
    updatedAt: now,
  };
  if (previous) {
    session.resumedFrom = previous.id;
    session.changesetId = previous.changesetId;
    session.handle = previous.handle;
    previous.supersededBy = session.id;
    await host.saveSession(previous);
    host.broadcast({ type: "session", session: previous });
  }
  for (const other of findOverlappingSessions(host.sessions.values(), session, session.intent)) {
    pushSignal(session, overlapSignal(other, intersect(session.intent, pathsOf(other))));
  }
  host.counters.forks++;
  await host.saveSession(session);
  await host.storage.put(HUB_KEYS.counters, host.counters);
  host.recordEvent({
    kind: "forked",
    session: session.id,
    agent: session.agent,
    message: `forked ${shortRepo(baseline.repo)} → ${shortRepo(name)}`,
  });
  host.broadcast({ type: "session", session });
  host.broadcastStats();
  return { session, token };
}
