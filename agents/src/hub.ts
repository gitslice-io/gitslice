// Hub: one Durable Object per slice. It owns the slice's agent sessions, the
// Artifacts baseline they fork, the path index agents coordinate through, and
// the live stream the dashboard renders.

import { DurableObject } from "cloudflare:workers";
import type { Env } from "./env.ts";
import {
  adoptHubBaseline,
  forkHubSession,
  importHubBaseline,
  retireBaselineRepo,
  useHubBaseline,
  type BaselineHost,
  type OpenSessionInput,
} from "./hubBaseline.ts";
import { HUB_KEYS, sessionStorageKey } from "./hubKeys.ts";
import {
  appendHubEvent,
  broadcastToSockets,
  buildHubSnapshot,
  computeHubStats,
  evictStaleSockets,
  parseStreamSliceFromUrl,
  resetHubState,
  type HubCounters,
  type ResettableHubHost,
} from "./hubDashboard.ts";
import {
  findSessionForChange,
  isNativeEnabled,
  registerNativePatchset,
  runHubAlarmSweep,
  watchNativeSlice,
  type NativeHost,
  type RegisterNativeInput,
} from "./hubNative.ts";
import {
  followFixerSession,
  markSessionLanded,
  openFixerSession,
  pushSignal,
  touchSessionPaths,
  type SessionLifecycleHost,
} from "./hubOverlap.ts";
import { retryEscalated } from "./land.ts";
import { computeNextAlarmMs, FIX_AFTER_MS, pruneSessionsMap } from "./policy.ts";
import type { Baseline, HubEvent, Session, Signal, Stats } from "./types.ts";

export type { OpenSessionInput };

type HubHost = BaselineHost & NativeHost & SessionLifecycleHost & ResettableHubHost;

export class Hub extends DurableObject<Env> {
  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    // Answer WebSocket heartbeats at the edge without waking a hibernating DO.
    this.ctx.setWebSocketAutoResponse(new WebSocketRequestResponsePair("ping", "pong"));
  }
  private readonly host: HubHost = {
    env: this.env,
    storage: this.ctx.storage,
    sessions: new Map<string, Session>(),
    events: [] as HubEvent[],
    baseline: null as Baseline | null,
    baselineGen: 0,
    baselineFor: -1,
    refreshing: null as Promise<Baseline> | null,
    lastImportAt: 0,
    floor: 0,
    ident: null as { account: string; slice: string } | null,
    nativeActiveAt: 0,
    nativeQueued: new Map<string, number>(),
    nativePollAt: 0,
    nativeSince: 0,
    retired: [] as string[],
    counters: { forks: 0, baselines: 0, pushes: 0 } as HubCounters,
    saveSession: (session: Session) => this.save(session),
    landed: (id: string, commit: string) => this.landed(id, commit),
    recordEvent: (event: Omit<HubEvent, "at">) => this.event(event),
    broadcast: (message: unknown) => this.broadcast(message),
    broadcastStats: () => this.broadcastStats(),
    scheduleAlarm: () => this.scheduleAlarm(),
  };
  private loaded: Promise<void> | null = null;

  private load(): Promise<void> {
    if (!this.loaded) {
      this.loaded = (async () => {
        const h = this.host;
        const stored = await this.ctx.storage.list<Session>({ prefix: HUB_KEYS.sessionPrefix });
        for (const session of stored.values()) h.sessions.set(session.id, session);
        h.events = (await this.ctx.storage.get<HubEvent[]>(HUB_KEYS.events)) ?? [];
        h.baseline = (await this.ctx.storage.get<Baseline>(HUB_KEYS.baseline)) ?? null;
        h.baselineGen = (await this.ctx.storage.get<number>(HUB_KEYS.baselineGen)) ?? 0;
        h.baselineFor = (await this.ctx.storage.get<number>(HUB_KEYS.baselineFor)) ?? -1;
        h.retired = (await this.ctx.storage.get<string[]>(HUB_KEYS.retired)) ?? [];
        h.floor = (await this.ctx.storage.get<number>(HUB_KEYS.floor)) ?? 0;
        h.ident = (await this.ctx.storage.get<{ account: string; slice: string }>(HUB_KEYS.ident)) ?? null;
        h.nativeSince = (await this.ctx.storage.get<number>(HUB_KEYS.nativeSince)) ?? 0;
        h.counters = (await this.ctx.storage.get<HubCounters>(HUB_KEYS.counters)) ?? h.counters;
      })();
    }
    return this.loaded;
  }

  // ---- sessions -----------------------------------------------------------

  async openSession(input: OpenSessionInput): Promise<{ session: Session; token: string }> {
    await this.load();
    return forkHubSession(this.host, input);
  }

  async getSession(id: string): Promise<Session | null> {
    await this.load();
    return this.host.sessions.get(id) ?? null;
  }

  async listSessions(): Promise<Session[]> {
    await this.load();
    return [...this.host.sessions.values()].sort((a, b) => b.createdAt - a.createdAt);
  }

  async claimPush(id: string, commit: string): Promise<Session | null> {
    await this.load();
    const session = this.host.sessions.get(id);
    if (!session || session.lastCommit === commit || session.supersededBy) return null;
    session.lastCommit = commit;
    delete session.fixAt;
    session.pushes++;
    session.pushedAt = Date.now();
    session.status = "pushed";
    session.updatedAt = Date.now();
    this.host.counters.pushes++;
    await this.save(session);
    await this.ctx.storage.put(HUB_KEYS.counters, this.host.counters);
    this.event({ kind: "pushed", session: id, agent: session.agent, message: `pushed ${commit.slice(0, 7)}` });
    this.broadcast({ type: "session", session });
    this.broadcastStats();
    return session;
  }

  async patch(id: string, partial: Partial<Session>, signal?: Omit<Signal, "at">, eventMessage?: string): Promise<Session | null> {
    await this.load();
    const session = this.host.sessions.get(id);
    if (!session) return null;
    Object.assign(session, partial, { updatedAt: Date.now() });
    if (signal) pushSignal(session, signal);
    if (session.status === "changes-requested" && !session.fixerFor && !session.fixedBy && !session.fixAt && this.env.FIXER !== "off") {
      session.fixAt = Date.now() + FIX_AFTER_MS;
    } else if (session.status !== "changes-requested") {
      delete session.fixAt;
    }
    await this.save(session);
    if (eventMessage) this.event({ kind: partial.status ?? "update", session: id, agent: session.agent, message: eventMessage });
    this.broadcast({ type: "session", session });
    this.broadcastStats();
    if (session.fixerFor) await followFixerSession(this.host, session, partial);
    await this.scheduleAlarm();
    return session;
  }

  async touch(id: string, paths: string[]): Promise<void> {
    await this.load();
    await touchSessionPaths(this.host, id, paths);
  }

  async landed(id: string, commit: string): Promise<void> {
    await this.load();
    await markSessionLanded(this.host, id, commit);
  }

  async expectPush(id: string): Promise<void> {
    await this.load();
    const session = this.host.sessions.get(id);
    if (!session?.fixAt) return;
    session.fixAt = Date.now() + FIX_AFTER_MS;
    await this.save(session);
    await this.scheduleAlarm();
  }

  async openFix(failingId: string): Promise<{ session: Session; token: string } | null> {
    await this.load();
    return openFixerSession(this.host, failingId);
  }

  async stats(): Promise<Stats> {
    await this.load();
    return computeHubStats(this.host.sessions.values(), this.host.counters);
  }

  async reset(): Promise<{ deleted: number }> {
    await this.load();
    return resetHubState(this.host);
  }

  // ---- baselines, native & alarm ------------------------------------------

  async importNow(account: string, slice: string): Promise<Baseline> {
    await this.load();
    return useHubBaseline(this.host, await importHubBaseline(this.env, account, slice), this.host.baselineGen, "imported");
  }

  async adoptBaseline(repoName: string): Promise<Baseline> {
    await this.load();
    return adoptHubBaseline(this.host, repoName);
  }

  async watchNative(account: string, slice: string, active = false): Promise<void> {
    await this.load();
    await watchNativeSlice(this.host, account, slice, active);
  }

  async sessionForChange(idOrHandle: string): Promise<Session | null> {
    await this.load();
    return findSessionForChange(this.host.sessions.values(), idOrHandle);
  }

  async registerNative(input: RegisterNativeInput): Promise<Session | null> {
    await this.load();
    return registerNativePatchset(this.host, input);
  }

  async alarm(): Promise<void> {
    await this.load();
    await runHubAlarmSweep(this.host, (session) => retryEscalated(this.env, this, session));
  }

  private async scheduleAlarm(): Promise<void> {
    const now = Date.now();
    const target = computeNextAlarmMs({
      now,
      sessions: this.host.sessions.values(),
      nativeEnabled: isNativeEnabled(this.env, this.host.ident),
      nativeActiveAt: this.host.nativeActiveAt,
      openWebSockets: this.ctx.getWebSockets().length,
    });
    const existing = await this.ctx.storage.getAlarm();
    if (target === null) {
      if (existing !== null) await this.ctx.storage.deleteAlarm();
      return;
    }
    if (existing === null || existing <= now || existing > target) {
      await this.ctx.storage.setAlarm(target);
    }
  }

  // ---- dashboard stream ---------------------------------------------------

  async fetch(request: Request): Promise<Response> {
    if (request.headers.get("Upgrade") !== "websocket") return new Response("expected websocket", { status: 426 });
    await this.load();
    evictStaleSockets(this.ctx.getWebSockets());
    const pair = new WebSocketPair();
    this.ctx.acceptWebSocket(pair[1]);
    const where = parseStreamSliceFromUrl(request.url);
    if (where) await this.watchNative(where.account, where.slice, false);
    pair[1].send(JSON.stringify({ type: "snapshot", ...this.snapshot() }));
    return new Response(null, { status: 101, webSocket: pair[0] });
  }

  async webSocketMessage(ws: WebSocket, message: string | ArrayBuffer): Promise<void> {
    if (message === "snapshot") {
      await this.load();
      ws.send(JSON.stringify({ type: "snapshot", ...this.snapshot() }));
    }
  }

  async webSocketClose(ws: WebSocket, code: number): Promise<void> {
    try {
      ws.close(code, "closing");
    } catch {
      // already closed
    }
    await this.load();
    await this.scheduleAlarm();
  }

  async webSocketError(ws: WebSocket): Promise<void> {
    try {
      ws.close(1011, "websocket error");
    } catch {
      // already closed
    }
    await this.load();
    await this.scheduleAlarm();
  }

  // ---- internals ----------------------------------------------------------

  private snapshot() {
    return buildHubSnapshot(this.host.sessions.values(), this.host.events, this.host.counters, this.host.baseline);
  }

  private async save(session: Session): Promise<void> {
    this.host.sessions.set(session.id, session);
    await this.ctx.storage.put(sessionStorageKey(session.id), session);
    const evicted = pruneSessionsMap(this.host.sessions);
    if (evicted.length > 0) {
      await this.ctx.storage.delete(evicted.map((s) => sessionStorageKey(s.id)));
      let retiredChanged = false;
      for (const s of evicted) {
        if (!s.native) {
          retireBaselineRepo(this.host, s.id);
          retiredChanged = true;
        }
      }
      if (retiredChanged) {
        await this.ctx.storage.put(HUB_KEYS.retired, this.host.retired);
      }
    }
  }

  private event(event: Omit<HubEvent, "at">): void {
    const full = appendHubEvent(this.host.events, event);
    this.ctx.storage.put(HUB_KEYS.events, this.host.events).catch(() => {});
    this.broadcast({ type: "event", event: full });
  }

  private broadcastStats(): void {
    this.broadcast({ type: "stats", stats: computeHubStats(this.host.sessions.values(), this.host.counters) });
  }

  private broadcast(message: unknown): void {
    broadcastToSockets(this.ctx.getWebSockets(), message);
  }
}
