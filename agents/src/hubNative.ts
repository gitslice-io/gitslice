// Native Gitslice changeset watching, session registration, landing sync, and alarm sweeps for Hub.

import { sessionRepoName } from "./artifacts.ts";
import type { Env } from "./env.ts";
import { Gitslice } from "./gitslice.ts";
import type { HubCounters } from "./hubDashboard.ts";
import { HUB_KEYS } from "./hubKeys.ts";
import {
  findOverlappingSessions,
  hasOverlapSignal,
  intersect,
  mapLimit,
  overlapSignal,
  parseTitle,
  pathsOf,
  pushSignal,
} from "./hubOverlap.ts";
import {
  filterSlicePaths,
  isUnreviewedEscalation,
  PROCESSING_STATUSES,
  pruneNativeQueued,
  REREVIEW_AFTER_MS,
  RETRY_LIMIT,
  STALL_MS,
} from "./policy.ts";
import type { HubEvent, Session } from "./types.ts";

export interface RegisterNativeInput {
  account: string;
  slice: string;
  changeset: { id: string; handle?: string; title: string; author?: string; baseCommitId: string };
  patchset: { id: string; number?: string; createdAtMs: number; changedPaths: string[]; baseTreeId?: string };
  force?: boolean;
}

export interface NativeStorage {
  put(key: string, value: unknown): Promise<void>;
  put(entries: Record<string, unknown>): Promise<void>;
}

export interface NativeHost {
  readonly env: Env;
  readonly storage: NativeStorage;
  readonly sessions: Map<string, Session>;
  ident: { account: string; slice: string } | null;
  nativeActiveAt: number;
  readonly nativeQueued: Map<string, number>;
  nativePollAt: number;
  nativeSince: number;
  counters: HubCounters;
  saveSession(session: Session): Promise<void>;
  landed(id: string, commit: string): Promise<void>;
  recordEvent(event: Omit<HubEvent, "at">): void;
  broadcast(message: unknown): void;
  broadcastStats(): void;
  scheduleAlarm(): Promise<void>;
}

export function isNativeEnabled(
  env: Pick<Env, "NATIVE_SLICES">,
  ident: { account: string; slice: string } | null,
): boolean {
  if (!ident) return false;
  const slices = (env.NATIVE_SLICES || "").split(",").map((s) => s.trim());
  return slices.includes(`${ident.account}/${ident.slice}`);
}

export async function watchNativeSlice(
  host: NativeHost,
  account: string,
  slice: string,
  active = false,
): Promise<void> {
  if (!host.ident) {
    host.ident = { account, slice };
    host.nativeSince = Date.now();
    await host.storage.put({ ident: host.ident, nativeSince: host.nativeSince });
  }
  if (active) {
    host.nativeActiveAt = Date.now();
  }
  if (isNativeEnabled(host.env, host.ident)) {
    await host.scheduleAlarm();
  }
}

export function findSessionForChange(sessions: Iterable<Session>, idOrHandle: string): Session | null {
  const matches = [...sessions].filter(
    (s) => s.changesetId && (s.changesetId === idOrHandle || s.handle === idOrHandle || s.changesetId.startsWith(`cs_${idOrHandle}`)),
  );
  // The fixer's session is the live one while a fix is under way.
  return matches.find((s) => s.fixerFor && s.status !== "landed") ?? matches.find((s) => !s.fixerFor) ?? null;
}

// registerNativePatchset records a patchset an agent pushed to Gitslice. It
// returns the session to review, or null when this patchset is already known.
export async function registerNativePatchset(
  host: NativeHost,
  input: RegisterNativeInput,
): Promise<Session | null> {
  const { changeset: cs, patchset: ps } = input;
  const scopedPaths = filterSlicePaths(ps.changedPaths, input.account, input.slice, {
    allowDotGitslice: true,
    leadingSlash: true,
  });
  const related = [...host.sessions.values()].filter((s) => s.changesetId === cs.id);
  const known = related.find((s) => s.patchsetId === ps.id);
  if (known) return input.force ? known : null;
  const now = Date.now();
  const author = related.find((s) => !s.fixerFor);
  const fixer = related.find((s) => s.fixerFor && s.status === "forked");
  const target = fixer ?? author;
  host.nativeActiveAt = now;
  if (target) {
    target.patchsetId = ps.id;
    target.lastCommit = ps.id;
    target.pushes++;
    target.pushedAt = ps.createdAtMs || now;
    target.status = "pushed";
    target.touched = scopedPaths;
    target.updatedAt = now;
    delete target.fixAt;
    host.counters.pushes++;
    pushSignal(target, { kind: "info", message: `Patchset ${ps.number ?? "?"} pushed to ${cs.handle ?? cs.id}.` });
    await host.saveSession(target);
    await host.storage.put(HUB_KEYS.counters, host.counters);
    host.recordEvent({
      kind: "pushed",
      session: target.id,
      agent: target.agent,
      message: `pushed patchset ${ps.number ?? "?"} of ${cs.handle ?? cs.id}`,
    });
    host.broadcast({ type: "session", session: target });
    host.broadcastStats();
    return target;
  }
  // The first patchset of a change nobody has seen: a new native session.
  const { agent, task } = parseTitle(cs.title, cs.author);
  const id = sessionRepoName(input.account, input.slice, agent, `n${cs.id.slice(-8)}`);
  const session: Session = {
    id,
    agent,
    task,
    account: input.account,
    slice: input.slice,
    remote: `${host.env.GITSLICE_GIT}/${input.account}/${input.slice}.git`,
    baseline: { repo: "gitslice", gitCommit: "", nativeCommit: cs.baseCommitId, tree: ps.baseTreeId ?? "", createdAt: now },
    intent: scopedPaths,
    touched: scopedPaths,
    status: "pushed",
    changesetId: cs.id,
    handle: cs.handle || cs.id,
    patchsetId: ps.id,
    native: true,
    pushes: 1,
    lastCommit: ps.id,
    signals: [],
    createdAt: now,
    updatedAt: now,
    pushedAt: ps.createdAtMs || now,
  };
  for (const other of findOverlappingSessions(host.sessions.values(), session, session.touched)) {
    pushSignal(session, overlapSignal(other, intersect(session.touched, pathsOf(other))));
    if (!hasOverlapSignal(other, session.id)) {
      pushSignal(other, overlapSignal(session, intersect(session.touched, pathsOf(other))));
      await host.saveSession(other);
      host.broadcast({ type: "session", session: other });
    }
  }
  host.counters.forks++;
  host.counters.pushes++;
  await host.saveSession(session);
  await host.storage.put(HUB_KEYS.counters, host.counters);
  host.recordEvent({ kind: "pushed", session: id, agent, message: `pushed ${cs.handle ?? cs.id} straight to Gitslice` });
  host.broadcast({ type: "session", session });
  host.broadcastStats();
  return session;
}

// pollNativeChangesets looks for patchsets agents pushed straight to Gitslice
// and for those that have landed. The Worker is not told about these pushes, so
// it asks while active or watched. Agents that nudge the Worker skip the wait.
export async function pollNativeChangesets(host: NativeHost): Promise<void> {
  if (!host.ident || !isNativeEnabled(host.env, host.ident)) return;
  const now = Date.now();
  if (now - host.nativePollAt < 1000) return;
  host.nativePollAt = now;
  pruneNativeQueued(host.nativeQueued, now);
  const slice = { account: host.ident.account, slice: host.ident.slice };
  const gitslice = new Gitslice(host.env.GITSLICE_API, host.env.GITSLICE_TOKEN);
  const open = await gitslice.listChangesets(slice, "draft", 200);
  for (const cs of open) {
    if (cs.description?.includes("Artifacts session:")) continue; // landed through an Artifacts session
    const ps = cs.patchsets?.find((p) => p.id === cs.currentPatchsetId) ?? cs.patchsets?.at(-1);
    if (!ps) continue;
    if ((Date.parse(ps.createdAt ?? "") || now) < host.nativeSince - 5000) continue; // from before this run
    const key = `${cs.id}:${ps.id}`;
    const queued = host.nativeQueued.get(key);
    if (queued && now - queued < 30_000) continue;
    if ([...host.sessions.values()].some((s) => s.changesetId === cs.id && s.patchsetId === ps.id)) continue;
    host.nativeQueued.set(key, now);
    pruneNativeQueued(host.nativeQueued, now);
    host.nativeActiveAt = now;
    await host.env.EVENTS.send({ kind: "review-native", account: slice.account, slice: slice.slice, changeset: cs.id });
  }
  const waiting = [...host.sessions.values()].filter((s) => s.native && s.status !== "landed" && !s.supersededBy);
  if (waiting.length === 0) return;
  // Ask about each open change, not for a list of submitted ones: the list
  // is capped and not ordered by time, so new landings would go unnoticed.
  const ids = [...new Set(waiting.map((s) => s.changesetId!))];
  const current = await mapLimit(ids, 8, (id) => gitslice.getChangeset(id).catch(() => null));
  for (const done of current) {
    if (!done || done.status !== "submitted" || !done.commitId) continue;
    for (const session of waiting.filter((s) => s.changesetId === done.id && !s.fixerFor)) {
      // A fixer's session lands the change; it carries the author's with it.
      const fixer = [...host.sessions.values()].find((s) => s.fixerFor === session.id && s.status !== "landed");
      await host.landed(fixer ? fixer.id : session.id, done.commitId);
    }
    host.nativeActiveAt = now;
  }
}

export async function runHubAlarmSweep(
  host: NativeHost,
  onRetryEscalated: (session: Session) => Promise<void>,
): Promise<void> {
  const waiting = [...host.sessions.values()].filter((s) => s.status === "needs-human" && !s.supersededBy && !s.native);
  for (const session of waiting) {
    try {
      await onRetryEscalated(session);
    } catch (err) {
      console.warn(JSON.stringify({ msg: "escalation retry failed", session: session.id, error: String(err) }));
    }
  }
  const now = Date.now();
  for (const session of host.sessions.values()) {
    if (session.status !== "changes-requested" || !session.fixAt || session.fixAt > now || session.fixedBy || session.supersededBy) continue;
    session.fixedBy = "pending";
    delete session.fixAt;
    await host.saveSession(session);
    await host.env.EVENTS.send({ kind: "fix", session: session.id });
    host.recordEvent({ kind: "fix", session: session.id, agent: session.agent, message: "no answer to the review; sending a fixer agent" });
    host.broadcast({ type: "session", session });
  }
  for (const session of host.sessions.values()) {
    const stalled = PROCESSING_STATUSES.has(session.status) && now - session.updatedAt >= STALL_MS;
    const rereview = isUnreviewedEscalation(session) && now - session.updatedAt >= REREVIEW_AFTER_MS;
    if (!(stalled || rereview) || session.supersededBy || !session.lastCommit) continue;
    if ((session.retries ?? 0) >= RETRY_LIMIT) {
      if (!stalled) continue;
      session.status = "failed";
      pushSignal(session, { kind: "error", message: "The landing stalled and did not recover after several attempts." });
    } else {
      session.retries = (session.retries ?? 0) + 1;
      session.updatedAt = now;
      if (session.native && host.ident) {
        await host.env.EVENTS.send({ kind: "review-native", account: host.ident.account, slice: host.ident.slice, changeset: session.changesetId, force: true });
      } else {
        await host.env.EVENTS.send({ kind: "land", session: session.id, commit: session.lastCommit, retry: true });
      }
      host.recordEvent({
        kind: "retry",
        session: session.id,
        agent: session.agent,
        message: `landing stalled in ${session.status}; trying again (${session.retries}/${RETRY_LIMIT})`,
      });
    }
    await host.saveSession(session);
    host.broadcast({ type: "session", session });
  }
  try {
    await pollNativeChangesets(host);
  } catch (err) {
    console.warn(JSON.stringify({ msg: "native poll failed", error: String(err) }));
  }
  await host.scheduleAlarm();
}
