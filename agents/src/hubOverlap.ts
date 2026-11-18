// Session path overlap detection, coordination signals, fixer handoff, and naming helpers for Hub.

import { sessionRepoName, slugify } from "./artifacts.ts";
import type { Env } from "./env.ts";
import type { HubCounters } from "./hubDashboard.ts";
import { FIX_AFTER_MS } from "./policy.ts";
import type { HubEvent, Session, Signal } from "./types.ts";

export const MAX_SIGNALS = 40;

export const ACTIVE: ReadonlySet<string> = new Set([
  "forked",
  "pushed",
  "reviewing",
  "approved",
  "landing",
  "merging",
  "needs-human",
  "changes-requested",
]);

export interface SessionLifecycleHost {
  readonly env: Env;
  readonly storage: {
    put(key: string, value: unknown): Promise<void>;
    put(entries: Record<string, unknown>): Promise<void>;
  };
  readonly sessions: Map<string, Session>;
  baselineGen: number;
  counters: HubCounters;
  saveSession(session: Session): Promise<void>;
  recordEvent(event: Omit<HubEvent, "at">): void;
  broadcast(message: unknown): void;
  broadcastStats(): void;
}

export function disposeRepo(repo: unknown): void {
  const disposable = repo as { [Symbol.dispose]?: () => void } | null | undefined;
  disposable?.[Symbol.dispose]?.();
}

export function pathsOf(session: Session): string[] {
  return session.touched.length ? session.touched : session.intent;
}

// intersect matches paths exactly, and directory intents ("…/") by prefix.
export function intersect(a: string[], b: string[]): string[] {
  const out = new Set<string>();
  for (const x of a) {
    for (const y of b) {
      if (x === y || (x.endsWith("/") && y.startsWith(x)) || (y.endsWith("/") && x.startsWith(y))) {
        out.add(x.length >= y.length ? x : y);
      }
    }
  }
  return [...out];
}

export function findOverlappingSessions(sessions: Iterable<Session>, session: Session, paths: string[]): Session[] {
  if (paths.length === 0) return [];
  const out: Session[] = [];
  for (const other of sessions) {
    if (
      other.id !== session.id &&
      !other.supersededBy &&
      other.id !== session.resumedFrom &&
      other.id !== session.fixerFor &&
      other.fixerFor !== session.id &&
      ACTIVE.has(other.status) &&
      intersect(paths, pathsOf(other)).length > 0
    ) {
      out.push(other);
    }
  }
  return out;
}

export function overlapSignal(other: Session, paths: string[]): Omit<Signal, "at"> {
  return {
    kind: "overlap",
    message: `${other.agent} is also working on ${paths.map(shortPath).join(", ")} (${other.status}).`,
    data: { session: other.id, agent: other.agent, paths, status: other.status, changeset: other.handle ?? null },
  };
}

export function hasOverlapSignal(session: Session, otherId: string): boolean {
  return session.signals.some((s) => s.kind === "overlap" && (s.data as { session?: string } | undefined)?.session === otherId);
}

export function pushSignal(session: Session, signal: Omit<Signal, "at">): void {
  session.signals.push({ at: Date.now(), ...signal });
  if (session.signals.length > MAX_SIGNALS) session.signals.splice(0, session.signals.length - MAX_SIGNALS);
}

export async function touchSessionPaths(host: SessionLifecycleHost, id: string, paths: string[]): Promise<void> {
  const session = host.sessions.get(id);
  if (!session) return;
  session.touched = paths;
  for (const other of findOverlappingSessions(host.sessions.values(), session, paths)) {
    const shared = intersect(paths, pathsOf(other));
    if (!hasOverlapSignal(session, other.id)) pushSignal(session, overlapSignal(other, shared));
    if (!hasOverlapSignal(other, session.id)) {
      pushSignal(other, overlapSignal(session, shared));
      await host.saveSession(other);
      host.broadcast({ type: "session", session: other });
    }
    host.recordEvent({
      kind: "overlap",
      session: id,
      agent: session.agent,
      message: `overlaps ${other.agent} on ${shared.map(shortPath).join(", ")}`,
    });
  }
  await host.saveSession(session);
  host.broadcast({ type: "session", session });
}

export async function markSessionLanded(host: SessionLifecycleHost, id: string, commit: string): Promise<void> {
  const session = host.sessions.get(id);
  if (!session) return;
  session.status = "landed";
  session.landedCommit = commit;
  session.landedAt = Date.now();
  session.updatedAt = Date.now();
  pushSignal(session, { kind: "landed", message: `Landed as ${commit.slice(0, 19)}.` });
  host.baselineGen++;
  await host.storage.put("baselineGen", host.baselineGen);
  await host.saveSession(session);
  host.recordEvent({
    kind: "landed",
    session: id,
    agent: session.agent,
    message: `landed ${session.handle ?? ""}${session.autoMerged ? " (auto-merged)" : ""}`,
  });
  host.broadcast({ type: "session", session });
  const original = session.fixerFor ? host.sessions.get(session.fixerFor) : undefined;
  if (original && original.status !== "landed") {
    original.status = "landed";
    original.landedCommit = commit;
    original.landedAt = session.landedAt;
    original.updatedAt = Date.now();
    pushSignal(original, { kind: "landed", message: `Landed by ${session.agent} as ${commit.slice(0, 19)}.` });
    await host.saveSession(original);
    host.broadcast({ type: "session", session: original });
  }
  host.broadcastStats();
}

export async function openFixerSession(
  host: SessionLifecycleHost,
  failingId: string,
): Promise<{ session: Session; token: string } | null> {
  const failing = host.sessions.get(failingId);
  if (!failing || failing.status !== "changes-requested" || failing.supersededBy) return null;
  const name = sessionRepoName(
    failing.account,
    failing.slice,
    `fixer-${failing.agent}`,
    failing.native ? `n${nonce()}` : nonce(),
  );
  let remote = failing.remote;
  let token = host.env.GITSLICE_TOKEN;
  if (!failing.native) {
    const source = await host.env.ARTIFACTS.get(failing.id);
    try {
      const forked = await source.fork(name, {
        defaultBranchOnly: true,
        readOnly: false,
        description: `Fixer for ${failing.agent}`.slice(0, 200),
      });
      remote = forked.remote;
      token = forked.token ?? "";
      if (!token) {
        const repo = await host.env.ARTIFACTS.get(name);
        try {
          token = (await repo.createToken("write", 3600)).plaintext;
        } finally {
          disposeRepo(repo);
        }
      }
    } finally {
      disposeRepo(source);
    }
  }
  const now = Date.now();
  const session: Session = {
    id: name,
    agent: `fixer-${failing.agent}`,
    task: `Fix: ${failing.task}`.slice(0, 300),
    account: failing.account,
    slice: failing.slice,
    remote,
    baseline: failing.baseline,
    native: failing.native,
    intent: failing.touched.length ? failing.touched : failing.intent,
    touched: [],
    status: "forked",
    changesetId: failing.changesetId,
    handle: failing.handle,
    patchsetId: failing.patchsetId,
    fixerFor: failing.id,
    pushes: 0,
    signals: [],
    createdAt: now,
    updatedAt: now,
  };
  failing.fixedBy = name;
  delete failing.fixAt;
  pushSignal(failing, {
    kind: "info",
    message: failing.native
      ? `No answer to the review after ${FIX_AFTER_MS / 1000} s, so ${session.agent} took over your changeset to repair it. You need not act.`
      : `No answer to the review after ${FIX_AFTER_MS / 1000} s, so ${session.agent} forked your repository to repair the change. You need not act.`,
  });
  host.counters.forks++;
  await host.saveSession(failing);
  await host.saveSession(session);
  await host.storage.put("counters", host.counters);
  host.recordEvent({
    kind: "forked",
    session: name,
    agent: session.agent,
    message: `fixer forked ${shortRepo(failing.id)} → ${shortRepo(name)}`,
  });
  host.broadcast({ type: "session", session: failing });
  host.broadcast({ type: "session", session });
  host.broadcastStats();
  return { session, token };
}

export async function followFixerSession(
  host: Pick<SessionLifecycleHost, "sessions" | "saveSession" | "broadcast">,
  fixer: Session,
  partial: Partial<Session>,
): Promise<void> {
  const original = host.sessions.get(fixer.fixerFor!);
  if (!original) return;
  if (partial.patchsetId) original.patchsetId = partial.patchsetId;
  if (fixer.status === "changes-requested" || fixer.status === "failed") {
    original.status = "needs-human";
    pushSignal(original, {
      kind: "escalated",
      message: `${fixer.agent} could not repair the change either: ${fixer.signals.at(-1)?.message ?? "see its session"}. A person needs to look.`,
    });
  }
  await host.saveSession(original);
  host.broadcast({ type: "session", session: original });
}

export function globalPath(path: string): string {
  return path.startsWith("/") ? path : `/${path}`;
}

export function shortPath(path: string): string {
  return path.split("/").slice(3).join("/") || path;
}

export function shortRepo(name: string): string {
  return name.length > 36 ? `${name.slice(0, 33)}…` : name;
}

export function nonce(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(4));
  return [...bytes].map((b) => b.toString(36).padStart(2, "0")).join("").slice(0, 6);
}

// parseTitle reads "<agent>: <task>" from a changeset title, the convention for
// naming an agent whose changeset is authored by a shared identity.
export function parseTitle(title: string, author?: string): { agent: string; task: string } {
  const m = /^([A-Za-z0-9][A-Za-z0-9-]{2,39}): (.+)$/.exec(title);
  if (m) return { agent: slugify(m[1]), task: m[2].slice(0, 300) };
  return { agent: slugify(author || "agent"), task: title.slice(0, 300) };
}

export async function mapLimit<T, R>(items: T[], limit: number, fn: (item: T) => Promise<R>): Promise<R[]> {
  const out: R[] = new Array(items.length);
  let next = 0;
  const workers = Array.from({ length: Math.min(limit, items.length) }, async () => {
    while (next < items.length) {
      const index = next++;
      out[index] = await fn(items[index]);
    }
  });
  await Promise.all(workers);
  return out;
}
