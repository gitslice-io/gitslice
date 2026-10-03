// Hub: one Durable Object per slice. It owns the slice's agent sessions, the
// Artifacts baseline they fork, the path index agents coordinate through, and
// the live stream the dashboard renders.

import { DurableObject } from "cloudflare:workers";
import { nativeCommitOf, sessionRepoName } from "./artifacts";
import type { Env } from "./env";
import { mapLimit, retryEscalated } from "./land";
import type { Baseline, HubEvent, Session, Signal, Stats } from "./types";

const MAX_EVENTS = 300;
const MIN_IMPORT_INTERVAL_MS = 4000;
// How long an agent has to answer a review before a fixer agent takes over.
const FIX_AFTER_MS = 15_000;
// A landing that has not moved for this long probably died with its Worker
// invocation: it is queued again, up to RETRY_LIMIT times.
const STALL_MS = 90_000;
const WATCHDOG_EVERY_MS = 30_000;
const RETRY_LIMIT = 3;
const PROCESSING: ReadonlySet<string> = new Set(["pushed", "reviewing", "approved", "landing", "merging"]);
// An escalation only because no review model answered is reviewed again
// after this long, before it bothers a person.
const REREVIEW_AFTER_MS = 20_000;

// unreviewed: the review agent escalated only because no model answered.
const unreviewed = (s: Session): boolean => s.status === "needs-human" && s.review?.model === "none";
const MAX_SIGNALS = 40;
const ACTIVE: ReadonlySet<string> = new Set(["forked", "pushed", "reviewing", "approved", "landing", "merging", "needs-human", "changes-requested"]);

export interface OpenSessionInput {
  account: string;
  slice: string;
  agent: string;
  task: string;
  intent?: string[];
  resume?: string;
}

export class Hub extends DurableObject<Env> {
  private sessions = new Map<string, Session>();
  private events: HubEvent[] = [];
  private baseline: Baseline | null = null;
  private baselineGen = 0; // bumped whenever something lands
  private baselineFor = -1; // generation the current baseline reflects
  private refreshing: Promise<Baseline> | null = null;
  private lastImportAt = 0;
  private floor = 0; // baselines imported before this may not be forked (set on reset)
  private retired: string[] = []; // replaced baselines, deleted on reset
  private counters = { forks: 0, baselines: 0, pushes: 0 };
  private loaded: Promise<void> | null = null;

  private load(): Promise<void> {
    if (!this.loaded) {
      this.loaded = (async () => {
        const stored = await this.ctx.storage.list<Session>({ prefix: "s:" });
        for (const session of stored.values()) this.sessions.set(session.id, session);
        this.events = (await this.ctx.storage.get<HubEvent[]>("events")) ?? [];
        this.baseline = (await this.ctx.storage.get<Baseline>("baseline")) ?? null;
        this.baselineGen = (await this.ctx.storage.get<number>("baselineGen")) ?? 0;
        this.baselineFor = (await this.ctx.storage.get<number>("baselineFor")) ?? -1;
        this.retired = (await this.ctx.storage.get<string[]>("retired")) ?? [];
        this.floor = (await this.ctx.storage.get<number>("floor")) ?? 0;
        this.counters = (await this.ctx.storage.get<typeof this.counters>("counters")) ?? this.counters;
      })();
    }
    return this.loaded;
  }

  // ---- sessions -----------------------------------------------------------

  async openSession(input: OpenSessionInput): Promise<{ session: Session; token: string }> {
    await this.load();
    const previous = input.resume ? this.sessions.get(input.resume) : undefined;
    const baseline = await this.baselineToFork(input.account, input.slice, Boolean(previous));
    const name = sessionRepoName(input.account, input.slice, input.agent, nonce());

    using base = await this.env.ARTIFACTS.get(baseline.repo);
    const forked = await base.fork(name, {
      defaultBranchOnly: true,
      readOnly: false,
      description: `${input.agent}: ${input.task}`.slice(0, 200),
    });
    let token = forked.token;
    if (!token) {
      using repo = await this.env.ARTIFACTS.get(name);
      token = (await repo.createToken("write", 4 * 3600)).plaintext;
    }

    const now = Date.now();
    const session: Session = {
      id: name,
      agent: input.agent,
      task: input.task,
      account: input.account,
      slice: input.slice,
      remote: forked.remote,
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
      await this.save(previous);
      this.broadcast({ type: "session", session: previous });
    }
    // Coordination starts at the fork: tell the agent who already works on
    // the paths it intends to change.
    for (const other of this.overlapping(session, session.intent)) {
      pushSignal(session, overlapSignal(other, intersect(session.intent, pathsOf(other))));
    }
    this.counters.forks++;
    await this.save(session);
    await this.ctx.storage.put("counters", this.counters);
    this.event({ kind: "forked", session: session.id, agent: session.agent, message: `forked ${shortRepo(baseline.repo)} → ${shortRepo(name)}` });
    this.broadcast({ type: "session", session });
    this.broadcastStats();
    return { session, token };
  }

  async getSession(id: string): Promise<Session | null> {
    await this.load();
    return this.sessions.get(id) ?? null;
  }

  async listSessions(): Promise<Session[]> {
    await this.load();
    return [...this.sessions.values()].sort((a, b) => b.createdAt - a.createdAt);
  }

  // claimPush records a push once. Artifacts events and explicit notifications
  // can both report the same commit; only the first one lands it.
  async claimPush(id: string, commit: string): Promise<Session | null> {
    await this.load();
    const session = this.sessions.get(id);
    if (!session || session.lastCommit === commit || session.supersededBy) return null;
    session.lastCommit = commit;
    delete session.fixAt;
    session.pushes++;
    session.pushedAt = Date.now();
    session.status = "pushed";
    session.updatedAt = Date.now();
    this.counters.pushes++;
    await this.save(session);
    await this.ctx.storage.put("counters", this.counters);
    this.event({ kind: "pushed", session: id, agent: session.agent, message: `pushed ${commit.slice(0, 7)}` });
    this.broadcast({ type: "session", session });
    this.broadcastStats();
    return session;
  }

  async patch(id: string, partial: Partial<Session>, signal?: Omit<Signal, "at">, eventMessage?: string): Promise<Session | null> {
    await this.load();
    const session = this.sessions.get(id);
    if (!session) return null;
    Object.assign(session, partial, { updatedAt: Date.now() });
    if (signal) pushSignal(session, signal);
    // A rejected change gets a fixer agent if its author stays quiet. Fixers
    // do not get fixers: when one is rejected too, a person takes over.
    if (session.status === "changes-requested" && !session.fixerFor && !session.fixedBy && !session.fixAt && this.env.FIXER !== "off") {
      session.fixAt = Date.now() + FIX_AFTER_MS;
    } else if (session.status !== "changes-requested") {
      delete session.fixAt;
    }
    await this.save(session);
    if (eventMessage) this.event({ kind: partial.status ?? "update", session: id, agent: session.agent, message: eventMessage });
    this.broadcast({ type: "session", session });
    this.broadcastStats();
    if (session.fixerFor) await this.followFixer(session, partial);
    await this.scheduleAlarm();
    return session;
  }

  // touch records the paths a push changed and signals overlaps both ways.
  async touch(id: string, paths: string[]): Promise<void> {
    await this.load();
    const session = this.sessions.get(id);
    if (!session) return;
    session.touched = paths;
    for (const other of this.overlapping(session, paths)) {
      const shared = intersect(paths, pathsOf(other));
      if (!hasOverlapSignal(session, other.id)) pushSignal(session, overlapSignal(other, shared));
      if (!hasOverlapSignal(other, session.id)) {
        pushSignal(other, overlapSignal(session, shared));
        await this.save(other);
        this.broadcast({ type: "session", session: other });
      }
      this.event({ kind: "overlap", session: id, agent: session.agent, message: `overlaps ${other.agent} on ${shared.map(shortPath).join(", ")}` });
    }
    await this.save(session);
    this.broadcast({ type: "session", session });
  }

  async landed(id: string, commit: string): Promise<void> {
    await this.load();
    const session = this.sessions.get(id);
    if (!session) return;
    session.status = "landed";
    session.landedCommit = commit;
    session.landedAt = Date.now();
    session.updatedAt = Date.now();
    pushSignal(session, { kind: "landed", message: `Landed as ${commit.slice(0, 19)}.` });
    // New sessions should start from a baseline that includes this change.
    this.baselineGen++;
    await this.ctx.storage.put("baselineGen", this.baselineGen);
    await this.save(session);
    this.event({ kind: "landed", session: id, agent: session.agent, message: `landed ${session.handle ?? ""}${session.autoMerged ? " (auto-merged)" : ""}` });
    this.broadcast({ type: "session", session });
    const original = session.fixerFor ? this.sessions.get(session.fixerFor) : undefined;
    if (original && original.status !== "landed") {
      original.status = "landed";
      original.landedCommit = commit;
      original.landedAt = session.landedAt;
      original.updatedAt = Date.now();
      pushSignal(original, { kind: "landed", message: `Landed by ${session.agent} as ${commit.slice(0, 19)}.` });
      await this.save(original);
      this.broadcast({ type: "session", session: original });
    }
    this.broadcastStats();
  }

  // expectPush is the author saying "I pushed an answer" before the push has
  // made its way through the landing queue: the fixer waits for it.
  async expectPush(id: string): Promise<void> {
    await this.load();
    const session = this.sessions.get(id);
    if (!session?.fixAt) return;
    session.fixAt = Date.now() + FIX_AFTER_MS;
    await this.save(session);
    await this.scheduleAlarm();
  }

  // openFix forks the failing session's repository for a fixer agent. The
  // fork starts from the author's exact commits and shares its changeset, so
  // the fix lands as another patchset of the same change.
  async openFix(failingId: string): Promise<{ session: Session; token: string } | null> {
    await this.load();
    const failing = this.sessions.get(failingId);
    if (!failing || failing.status !== "changes-requested" || failing.supersededBy) return null;
    const name = sessionRepoName(failing.account, failing.slice, `fixer-${failing.agent}`, nonce());
    using source = await this.env.ARTIFACTS.get(failing.id);
    const forked = await source.fork(name, {
      defaultBranchOnly: true,
      readOnly: false,
      description: `Fixer for ${failing.agent}`.slice(0, 200),
    });
    let token = forked.token;
    if (!token) {
      using repo = await this.env.ARTIFACTS.get(name);
      token = (await repo.createToken("write", 3600)).plaintext;
    }
    const now = Date.now();
    const session: Session = {
      id: name,
      agent: `fixer-${failing.agent}`,
      task: `Fix: ${failing.task}`.slice(0, 300),
      account: failing.account,
      slice: failing.slice,
      remote: forked.remote,
      baseline: failing.baseline,
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
      message: `No answer to the review after ${FIX_AFTER_MS / 1000} s, so ${session.agent} forked your repository to repair the change. You need not act.`,
    });
    this.counters.forks++;
    await this.save(failing);
    await this.save(session);
    await this.ctx.storage.put("counters", this.counters);
    this.event({ kind: "forked", session: name, agent: session.agent, message: `fixer forked ${shortRepo(failing.id)} → ${shortRepo(name)}` });
    this.broadcast({ type: "session", session: failing });
    this.broadcast({ type: "session", session });
    this.broadcastStats();
    return { session, token };
  }

  // followFixer carries a fixer's progress back to the session it repairs.
  private async followFixer(fixer: Session, partial: Partial<Session>): Promise<void> {
    const original = this.sessions.get(fixer.fixerFor!);
    if (!original) return;
    if (partial.patchsetId) original.patchsetId = partial.patchsetId;
    if (fixer.status === "changes-requested" || fixer.status === "failed") {
      original.status = "needs-human";
      pushSignal(original, {
        kind: "escalated",
        message: `${fixer.agent} could not repair the change either: ${fixer.signals.at(-1)?.message ?? "see its session"}. A person needs to look.`,
      });
    }
    await this.save(original);
    this.broadcast({ type: "session", session: original });
  }

  async stats(): Promise<Stats> {
    await this.load();
    return this.computeStats();
  }

  // reset clears the board for a new run and deletes the previous run's
  // Artifacts repos. The current baseline stays.
  async reset(): Promise<{ deleted: number }> {
    await this.load();
    const repos = [...this.sessions.keys(), ...this.retired.filter((name) => name !== this.baseline?.repo)];
    await this.ctx.storage.deleteAll();
    this.sessions.clear();
    this.events = [];
    this.retired = [];
    this.counters = { forks: 0, baselines: 0, pushes: 0 };
    this.baselineGen++;
    // The codebase may have been restored: the next session waits for a
    // baseline imported after now.
    this.floor = Date.now();
    this.lastImportAt = 0;
    await this.ctx.storage.put({ baselineGen: this.baselineGen, floor: this.floor });
    if (this.baseline) await this.ctx.storage.put({ baseline: this.baseline, baselineFor: this.baselineFor });
    this.broadcast({ type: "snapshot", ...this.snapshot() });
    const deleted = await mapLimit(repos, 8, (name) => this.env.ARTIFACTS.delete(name).catch(() => false));
    return { deleted: deleted.filter(Boolean).length };
  }

  // ---- baselines ----------------------------------------------------------

  // baselineToFork picks the baseline a new session forks. New sessions never
  // wait for an import when a baseline exists: a slightly stale one only
  // means the bridge merges more, and the import runs in the background. A
  // rework waits for a fresh import, because it has to see the change it
  // conflicted with.
  private async baselineToFork(account: string, slice: string, fresh: boolean): Promise<Baseline> {
    if (fresh) {
      const gen = this.baselineGen;
      try {
        return await this.useBaseline(await this.importBaseline(account, slice), gen, "imported");
      } catch (err) {
        if (!this.baseline) throw err;
        return this.importFailed(err, gen);
      }
    }
    if (this.baseline && this.baselineFor === this.baselineGen) return this.baseline;
    const refresh = this.refresh(account, slice);
    return this.baseline && this.baseline.createdAt >= this.floor ? this.baseline : refresh;
  }

  private refresh(account: string, slice: string): Promise<Baseline> {
    if (this.refreshing) return this.refreshing;
    if (this.baseline && Date.now() - this.lastImportAt < MIN_IMPORT_INTERVAL_MS) return Promise.resolve(this.baseline);
    const gen = this.baselineGen;
    this.lastImportAt = Date.now();
    this.refreshing = this.importBaseline(account, slice)
      .then((baseline) => this.useBaseline(baseline, gen, "imported"))
      .catch((err) => {
        if (!this.baseline) throw err;
        return this.importFailed(err, gen);
      })
      .finally(() => {
        this.refreshing = null;
      });
    return this.refreshing;
  }

  // importFailed keeps agents working on the last baseline: a stale baseline
  // only means more merging.
  private importFailed(err: unknown, gen: number): Baseline {
    console.warn(JSON.stringify({ msg: "baseline import failed", error: String(err) }));
    this.baselineFor = Math.max(this.baselineFor, gen);
    this.event({ kind: "baseline", message: `baseline import failed; reusing ${shortRepo(this.baseline!.repo)}` });
    return this.baseline!;
  }

  // importNow imports a fresh baseline straight away, so the next session
  // does not wait for one (after a restore, say).
  async importNow(account: string, slice: string): Promise<Baseline> {
    await this.load();
    return this.useBaseline(await this.importBaseline(account, slice), this.baselineGen, "imported");
  }

  // adoptBaseline uses a repo someone pushed the slice's projection to, for
  // slices Artifacts cannot import directly (private slices).
  async adoptBaseline(repoName: string): Promise<Baseline> {
    await this.load();
    using repo = await this.env.ARTIFACTS.get(repoName);
    const [head] = await repo.log({ limit: 1 });
    if (!head) throw new Error(`${repoName} has no commits`);
    const nativeCommit = nativeCommitOf(head.message);
    if (!nativeCommit) throw new Error(`${repoName} head ${head.hash} has no Gitslice-Commit trailer`);
    return this.useBaseline({ repo: repoName, gitCommit: head.hash, nativeCommit, tree: head.treeHash, createdAt: Date.now() }, this.baselineGen, "adopted");
  }

  private async useBaseline(baseline: Baseline, gen: number, how: string): Promise<Baseline> {
    if (this.baseline && baseline.createdAt < this.baseline.createdAt) {
      // An import that started earlier finished later: keep the newer one.
      this.retire(baseline.repo);
      await this.ctx.storage.put("retired", this.retired);
      return this.baseline;
    }
    if (this.baseline && this.baseline.repo !== baseline.repo) this.retire(this.baseline.repo);
    this.baseline = baseline;
    this.baselineFor = Math.max(this.baselineFor, gen);
    this.counters.baselines++;
    await this.ctx.storage.put({ baseline, baselineFor: this.baselineFor, counters: this.counters, retired: this.retired });
    this.event({ kind: "baseline", message: `${how} baseline ${shortRepo(baseline.repo)} at ${baseline.nativeCommit.slice(7, 19)}` });
    this.broadcast({ type: "baseline", baseline });
    this.broadcastStats();
    return baseline;
  }

  // importBaseline asks Artifacts to import the slice straight from Gitslice's
  // Git endpoint. The projection is deterministic, so the baseline's commits
  // are the same commits every Gitslice clone sees.
  private async importBaseline(account: string, slice: string): Promise<Baseline> {
    const started = Date.now();
    const name = `base.${account}.${slice}.${started.toString(36)}${nonce().slice(0, 3)}`;
    const depth = Number(this.env.BASELINE_DEPTH || 0) || undefined;
    await this.env.ARTIFACTS.import({
      source: { url: `${this.env.GITSLICE_GIT}/${account}/${slice}.git`, ...(depth ? { depth } : {}) },
      target: { name, opts: { readOnly: true, description: `Baseline of ${account}/${slice}` } },
    });
    for (let attempt = 0; attempt < 40; attempt++) {
      try {
        using repo = await this.env.ARTIFACTS.get(name);
        const [head] = await repo.log({ limit: 1 });
        if (head) {
          const nativeCommit = nativeCommitOf(head.message);
          if (!nativeCommit) throw new Error(`baseline head ${head.hash} has no Gitslice-Commit trailer`);
          return { repo: name, gitCommit: head.hash, nativeCommit, tree: head.treeHash, createdAt: started };
        }
      } catch (err) {
        if (String(err).includes("Gitslice-Commit")) throw err;
      }
      await new Promise((resolve) => setTimeout(resolve, 500));
    }
    throw new Error(`baseline ${name} did not become ready`);
  }

  // ---- escalations --------------------------------------------------------

  async alarm(): Promise<void> {
    await this.load();
    const waiting = [...this.sessions.values()].filter((s) => s.status === "needs-human" && !s.supersededBy);
    for (const session of waiting) {
      try {
        await retryEscalated(this.env, this, session);
      } catch (err) {
        console.warn(JSON.stringify({ msg: "escalation retry failed", session: session.id, error: String(err) }));
      }
    }
    const now = Date.now();
    for (const session of this.sessions.values()) {
      if (session.status !== "changes-requested" || !session.fixAt || session.fixAt > now || session.fixedBy || session.supersededBy) continue;
      session.fixedBy = "pending";
      delete session.fixAt;
      await this.save(session);
      await this.env.EVENTS.send({ kind: "fix", session: session.id });
      this.event({ kind: "fix", session: session.id, agent: session.agent, message: "no answer to the review; sending a fixer agent" });
      this.broadcast({ type: "session", session });
    }
    for (const session of this.sessions.values()) {
      const stalled = PROCESSING.has(session.status) && now - session.updatedAt >= STALL_MS;
      const rereview = unreviewed(session) && now - session.updatedAt >= REREVIEW_AFTER_MS;
      if (!(stalled || rereview) || session.supersededBy || !session.lastCommit) continue;
      if ((session.retries ?? 0) >= RETRY_LIMIT) {
        if (!stalled) continue; // still unreviewed: it stays with a person
        session.status = "failed";
        pushSignal(session, { kind: "error", message: "The landing stalled and did not recover after several attempts." });
      } else {
        session.retries = (session.retries ?? 0) + 1;
        session.updatedAt = now;
        await this.env.EVENTS.send({ kind: "land", session: session.id, commit: session.lastCommit, retry: true });
        this.event({ kind: "retry", session: session.id, agent: session.agent, message: `landing stalled in ${session.status}; trying again (${session.retries}/${RETRY_LIMIT})` });
      }
      await this.save(session);
      this.broadcast({ type: "session", session });
    }
    await this.scheduleAlarm();
  }

  // scheduleAlarm wakes the Hub for the next thing that needs a timer:
  // escalations a person may have approved, and fixers that are due.
  private async scheduleAlarm(): Promise<void> {
    const now = Date.now();
    let next = Infinity;
    for (const s of this.sessions.values()) {
      if (s.supersededBy) continue;
      if (s.status === "needs-human") next = Math.min(next, now + (unreviewed(s) ? REREVIEW_AFTER_MS : 3000));
      if (PROCESSING.has(s.status)) next = Math.min(next, now + WATCHDOG_EVERY_MS);
      if (s.fixAt && !s.fixedBy) next = Math.min(next, s.fixAt);
    }
    if (next === Infinity) return;
    const target = Math.max(next, now + 250);
    // Keep an alarm that is already due sooner: patches arrive constantly and
    // must not push it back.
    const existing = await this.ctx.storage.getAlarm();
    if (existing === null || existing <= now || existing > target) await this.ctx.storage.setAlarm(target);
  }

  // ---- dashboard stream ---------------------------------------------------

  async fetch(request: Request): Promise<Response> {
    if (request.headers.get("Upgrade") !== "websocket") return new Response("expected websocket", { status: 426 });
    await this.load();
    const pair = new WebSocketPair();
    this.ctx.acceptWebSocket(pair[1]);
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
  }

  // ---- internals ----------------------------------------------------------

  private snapshot() {
    return {
      sessions: [...this.sessions.values()].sort((a, b) => a.createdAt - b.createdAt),
      events: this.events.slice(-120),
      stats: this.computeStats(),
      baseline: this.baseline,
    };
  }

  private computeStats(): Stats {
    const all = [...this.sessions.values()];
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
      forks: this.counters.forks,
      baselines: this.counters.baselines,
      pushes: this.counters.pushes,
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

  private overlapping(session: Session, paths: string[]): Session[] {
    if (paths.length === 0) return [];
    return [...this.sessions.values()].filter(
      (other) =>
        other.id !== session.id &&
        !other.supersededBy &&
        other.id !== session.resumedFrom &&
        other.id !== session.fixerFor &&
        other.fixerFor !== session.id &&
        ACTIVE.has(other.status) &&
        intersect(paths, pathsOf(other)).length > 0,
    );
  }

  private retire(repo: string): void {
    if (!this.retired.includes(repo)) this.retired.push(repo);
  }

  private async save(session: Session): Promise<void> {
    this.sessions.set(session.id, session);
    await this.ctx.storage.put(`s:${session.id}`, session);
  }

  private event(event: Omit<HubEvent, "at">): void {
    const full = { at: Date.now(), ...event };
    this.events.push(full);
    if (this.events.length > MAX_EVENTS) this.events.splice(0, this.events.length - MAX_EVENTS);
    this.ctx.storage.put("events", this.events).catch(() => {});
    this.broadcast({ type: "event", event: full });
  }

  private broadcastStats(): void {
    this.broadcast({ type: "stats", stats: this.computeStats() });
  }

  private broadcast(message: unknown): void {
    const text = JSON.stringify(message);
    for (const ws of this.ctx.getWebSockets()) {
      try {
        ws.send(text);
      } catch {
        // the dashboard reconnects
      }
    }
  }
}

function pathsOf(session: Session): string[] {
  return session.touched.length ? session.touched : session.intent;
}

// intersect matches paths exactly, and directory intents ("…/") by prefix.
function intersect(a: string[], b: string[]): string[] {
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

function overlapSignal(other: Session, paths: string[]): Omit<Signal, "at"> {
  return {
    kind: "overlap",
    message: `${other.agent} is also working on ${paths.map(shortPath).join(", ")} (${other.status}).`,
    data: { session: other.id, agent: other.agent, paths, status: other.status, changeset: other.handle ?? null },
  };
}

function hasOverlapSignal(session: Session, otherId: string): boolean {
  return session.signals.some((s) => s.kind === "overlap" && (s.data as { session?: string } | undefined)?.session === otherId);
}

function pushSignal(session: Session, signal: Omit<Signal, "at">): void {
  session.signals.push({ at: Date.now(), ...signal });
  if (session.signals.length > MAX_SIGNALS) session.signals.splice(0, session.signals.length - MAX_SIGNALS);
}

export function globalPath(path: string): string {
  return path.startsWith("/") ? path : `/${path}`;
}

function shortPath(path: string): string {
  return path.split("/").slice(3).join("/") || path;
}

function shortRepo(name: string): string {
  return name.length > 36 ? `${name.slice(0, 33)}…` : name;
}

function nonce(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(4));
  return [...bytes].map((b) => b.toString(36).padStart(2, "0")).join("").slice(0, 6);
}
