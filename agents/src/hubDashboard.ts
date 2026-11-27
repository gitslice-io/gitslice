// Dashboard WebSocket stream, snapshot assembly, bounded event ring, reset, and aggregate stats for Hub.

import type { Env } from "./env.ts";
import { HUB_KEYS } from "./hubKeys.ts";
import { ACTIVE, mapLimit } from "./hubOverlap.ts";
import type { Baseline, HubEvent, Session, Stats } from "./types.ts";

export const MAX_EVENTS = 300;
export const MAX_HUB_SOCKETS = 64;

export interface HubCounters {
  forks: number;
  baselines: number;
  pushes: number;
}

export interface HubSnapshot {
  sessions: Session[];
  events: HubEvent[];
  stats: Stats;
  baseline: Baseline | null;
}

export interface ResettableHubHost {
  readonly env: Env;
  readonly storage: {
    deleteAll(): Promise<void>;
    put(key: string, value: unknown): Promise<void>;
    put(entries: Record<string, unknown>): Promise<void>;
  };
  readonly sessions: Map<string, Session>;
  events: HubEvent[];
  retired: string[];
  counters: HubCounters;
  baselineGen: number;
  baselineFor: number;
  floor: number;
  lastImportAt: number;
  nativeSince: number;
  readonly nativeQueued: Map<string, number>;
  ident: { account: string; slice: string } | null;
  baseline: Baseline | null;
  broadcast(message: unknown): void;
}

export function computeHubStats(sessions: Iterable<Session>, counters: HubCounters): Stats {
  const all = [...sessions];
  const live = all.filter((s) => !s.supersededBy);
  const landed = all.filter((s) => s.status === "landed");
  const landedChangesets = new Set(landed.map((s) => s.changesetId ?? s.id)).size;
  const times = landed
    .map((s) => (s.landedAt ?? 0) - (s.pushedAt ?? s.createdAt))
    .filter((ms) => ms > 0)
    .sort((a, b) => a - b);
  return {
    agents: new Set(all.map((s) => s.agent)).size,
    sessions: all.length,
    forks: counters.forks,
    baselines: counters.baselines,
    pushes: counters.pushes,
    changesets: new Set(all.map((s) => s.changesetId).filter(Boolean)).size,
    reviewed: all.filter((s) => s.review).length,
    landed: landedChangesets,
    autoMerged: landed.filter((s) => s.autoMerged).length,
    conflicts: all.filter((s) => s.status === "conflict" || s.resumedFrom).length,
    escalated: all.filter((s) => s.signals.some((sig) => sig.kind === "escalated")).length,
    fixes: all.filter((s) => s.fixerFor && s.status === "landed").length,
    inFlight: live.filter((s) => ACTIVE.has(s.status) && !s.fixedBy).length,
    medianLandMs: times.length ? times[Math.floor(times.length / 2)] : null,
  };
}

export function buildHubSnapshot(
  sessions: Iterable<Session>,
  events: readonly HubEvent[],
  counters: HubCounters,
  baseline: Baseline | null,
): HubSnapshot {
  return {
    sessions: [...sessions].sort((a, b) => a.createdAt - b.createdAt),
    events: events.slice(-120),
    stats: computeHubStats(sessions, counters),
    baseline,
  };
}

export function appendHubEvent(events: HubEvent[], event: Omit<HubEvent, "at">, now = Date.now()): HubEvent {
  const full: HubEvent = { at: now, ...event };
  events.push(full);
  if (events.length > MAX_EVENTS) {
    events.splice(0, events.length - MAX_EVENTS);
  }
  return full;
}

export function evictStaleSockets(open: readonly Pick<WebSocket, "close">[], maxSockets = MAX_HUB_SOCKETS): void {
  if (open.length < maxSockets) return;
  for (const stale of open.slice(0, open.length - maxSockets + 1)) {
    try {
      stale.close(1008, "too many connections");
    } catch {
      // already closed
    }
  }
}

export function broadcastToSockets(sockets: readonly Pick<WebSocket, "send">[], message: unknown): void {
  const text = JSON.stringify(message);
  for (const ws of sockets) {
    try {
      ws.send(text);
    } catch {
      // the dashboard reconnects
    }
  }
}

export function parseStreamSliceFromUrl(url: string): { account: string; slice: string } | null {
  const where = /\/v1\/slices\/([^/]+)\/([^/]+)\/stream/.exec(new URL(url).pathname);
  if (!where) return null;
  return { account: where[1], slice: where[2] };
}

export async function resetHubState(host: ResettableHubHost): Promise<{ deleted: number }> {
  const repos = [...host.sessions.keys(), ...host.retired.filter((name) => name !== host.baseline?.repo)];
  await host.storage.deleteAll();
  host.sessions.clear();
  host.events = [];
  host.retired = [];
  host.counters = { forks: 0, baselines: 0, pushes: 0 };
  host.baselineGen++;
  host.floor = Date.now();
  host.lastImportAt = 0;
  host.nativeSince = host.floor;
  host.nativeQueued.clear();
  await host.storage.put({ baselineGen: host.baselineGen, floor: host.floor, nativeSince: host.nativeSince });
  if (host.ident) await host.storage.put(HUB_KEYS.ident, host.ident);
  if (host.baseline) await host.storage.put({ baseline: host.baseline, baselineFor: host.baselineFor });
  host.broadcast({
    type: "snapshot",
    ...buildHubSnapshot(host.sessions.values(), host.events, host.counters, host.baseline),
  });
  const deleted = await mapLimit(repos, 8, (name) => host.env.ARTIFACTS.delete(name).catch(() => false));
  return { deleted: deleted.filter(Boolean).length };
}
