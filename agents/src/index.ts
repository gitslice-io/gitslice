// gitslice-agents: give every agent its own Cloudflare Artifacts repository,
// and land what they push in one Gitslice codebase.
//
//   POST /v1/sessions                     open a session: fork the slice baseline
//   GET  /v1/sessions/:id                 status, changeset and coordination signals
//   POST /v1/sessions/:id/pushed          optional nudge after `git push`
//   GET  /v1/slices/:account/:slice       stats and sessions
//   GET  /v1/slices/:account/:slice/stream  live dashboard stream (WebSocket)
//   POST /v1/slices/:account/:slice/restore  put a demo slice back to its seed
//   GET  /slices/:account/:slice          the dashboard
//   queue: Artifacts repo.pushed events → landing pipeline

import { parseSessionRepo, slugify } from "./artifacts";
import { dashboardHtml } from "./dashboard";
import type { Env } from "./env";
import { Hub, type OpenSessionInput } from "./hub";
import { landPush, type HubPort } from "./land";
import { findChange, reviewNativeChange, type NativeHub } from "./native";
import { authorized, corsHeaders, readAuthRequired, redactSession, redactSignals } from "./policy";
import { restoreSeed } from "./restore";
import { extractText, withTimeout } from "./review";
import type { Baseline, Session, Stats } from "./types";

// HubApi is the Hub's RPC surface as the Worker uses it. Typing the stub
// through it keeps TypeScript from expanding the recursive RPC types.
interface HubApi extends HubPort, NativeHub {
  watchNative(account: string, slice: string, active?: boolean): Promise<void>;
  sessionForChange(idOrHandle: string): Promise<Session | null>;
  fetch(request: Request): Promise<Response>;
  openSession(input: OpenSessionInput): Promise<{ session: Session; token: string }>;
  getSession(id: string): Promise<Session | null>;
  listSessions(): Promise<Session[]>;
  stats(): Promise<Stats>;
  reset(): Promise<{ deleted: number }>;
  adoptBaseline(repo: string): Promise<Baseline>;
  importNow(account: string, slice: string): Promise<Baseline>;
  openFix(failingId: string): Promise<{ session: Session; token: string } | null>;
  expectPush(id: string): Promise<void>;
}

export { Hub };

export default {
  async fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    try {
      return await route(request, env, ctx);
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      return json({ error: message }, 500, request, env);
    }
  },

  async queue(batch: MessageBatch<unknown>, env: Env): Promise<void> {
    await Promise.all(
      batch.messages.map(async (message) => {
        const fix = message.body as { kind?: string; session?: string };
        if (fix?.kind === "fix" && fix.session) {
          // A rejected change whose author went quiet: send a fixer agent.
          const where = parseSessionRepo(fix.session);
          if (where && isAllowed(env, where.account, where.slice)) {
            try {
              const { fixSession } = await import("./fixer");
              await fixSession(env, hubStub(env, where.account, where.slice), fix.session);
            } catch (err) {
              console.error(JSON.stringify({ msg: "fixer crashed", session: fix.session, error: String(err) }));
            }
          }
          message.ack();
          return;
        }
        const native = message.body as { kind?: string; account?: string; slice?: string; changeset?: string; force?: boolean };
        if (native?.kind === "review-native" && native.account && native.slice && native.changeset) {
          // A changeset an agent pushed straight to Gitslice.
          if (isAllowed(env, native.account, native.slice)) {
            try {
              await reviewNativeChange(env, hubStub(env, native.account, native.slice), { account: native.account, slice: native.slice }, native.changeset, Boolean(native.force));
            } catch (err) {
              console.error(JSON.stringify({ msg: "native review crashed", changeset: native.changeset, error: String(err) }));
            }
          }
          message.ack();
          return;
        }
        const target = landingTarget(message.body, env);
        if (!target) {
          message.ack();
          return;
        }
        try {
          await landPush(env, hubPort(env, target.account, target.slice), target.session, target.commit, target.retry);
          message.ack();
        } catch (err) {
          console.error(JSON.stringify({ msg: "landing crashed", target, error: String(err) }));
          message.retry({ delaySeconds: 2 });
        }
      }),
    );
  },
} satisfies ExportedHandler<Env, unknown>;

async function route(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
  if (request.method === "OPTIONS") {
    return new Response(null, {
      status: 204,
      headers: {
        ...corsHeaders(request, env),
        "access-control-allow-methods": "GET, POST, OPTIONS",
        "access-control-allow-headers": "authorization, content-type",
      },
    });
  }
  const url = new URL(request.url);
  const parts = url.pathname.split("/").filter(Boolean);
  const defaultSlice = allowedSlices(env)[0] ?? "demo/storefront";

  if (request.method === "GET" && parts.length === 0) {
    return Response.redirect(`${url.origin}/slices/${defaultSlice}`, 302);
  }
  if (request.method === "GET" && parts[0] === "slices" && parts.length === 3) {
    if (!isAllowed(env, parts[1], parts[2])) return json({ error: "unknown slice" }, 404, request, env);
    if (readAuthRequired(env) && !(await authorized(request, env))) {
      return json({ error: "unauthorized" }, 401, request, env);
    }
    return html(dashboardHtml({ account: parts[1], slice: parts[2], gitsliceWeb: env.GITSLICE_WEB, gitsliceGit: env.GITSLICE_GIT }));
  }
  if (parts[0] !== "v1") return json({ error: "not found" }, 404, request, env);

  // GET /v1/slices/:account/:slice[/stream]
  if (parts[1] === "slices" && parts.length >= 4) {
    const [account, slice] = [parts[2], parts[3]];
    if (!isAllowed(env, account, slice)) return json({ error: "unknown slice" }, 404, request, env);
    const hub = hubStub(env, account, slice);
    const activeNativeRequest = parts[4] === "changes";
    if (nativeOn(env, account, slice)) {
      ctx.waitUntil(hub.watchNative(account, slice, activeNativeRequest).catch(() => {}));
    }
    // Agents that push straight to Gitslice (refs/changes/new) ask here how
    // their change fared, and nudge when they have pushed a new patchset.
    if (parts[4] === "changes" && parts[5]) {
      if (request.method === "GET" && parts.length === 6) {
        const authed = await authorized(request, env);
        if (readAuthRequired(env) && !authed) return json({ error: "unauthorized" }, 401, request, env);
        const found = await hub.sessionForChange(parts[5]);
        if (!found) return json({ status: "pending", message: "not reviewed yet" }, 202, request, env);
        const rawSignals = found.signals.slice(-6);
        return json(
          {
            id: found.id,
            agent: found.agent,
            status: found.status,
            patchsetId: found.patchsetId,
            review: found.review ?? null,
            signals: authed ? rawSignals : redactSignals(rawSignals),
            fixer: found.fixedBy ?? null,
          },
          200,
          request,
          env,
        );
      }
      if (request.method === "POST" && parts[6] === "pushed") {
        if (!(await authorized(request, env))) return json({ error: "unauthorized" }, 401, request, env);
        ctx.waitUntil(
          reviewNativeChange(env, hub, { account, slice }, parts[5]).catch((err) => console.error(JSON.stringify({ msg: "native nudge failed", changeset: parts[5], error: String(err) }))),
        );
        return json({ reviewing: parts[5] }, 202, request, env);
      }
    }
    if (parts[4] === "stream") {
      if (readAuthRequired(env) && !(await authorized(request, env))) {
        return json({ error: "unauthorized" }, 401, request, env);
      }
      return hub.fetch(request);
    }
    if (parts[4] === "baseline" && request.method === "POST") {
      if (!(await authorized(request, env))) return json({ error: "unauthorized" }, 401, request, env);
      const body = (await request.json().catch(() => ({}))) as { action?: string; repo?: string };
      if (body.action === "create") {
        // An empty repo the caller pushes the slice's projection to.
        const created = await env.ARTIFACTS.create(`base.${account}.${slice}.${Date.now().toString(36)}`, {
          description: `Baseline of ${account}/${slice}`,
        });
        return json({ repo: created.name, remote: created.remote, token: created.token }, 201, request, env);
      }
      if (body.action === "adopt" && body.repo) return json(await hub.adoptBaseline(body.repo), 200, request, env);
      if (body.action === "import") return json(await hub.importNow(account, slice), 200, request, env);
      return json({ error: 'action must be "create", "adopt" or "import"' }, 400, request, env);
    }
    if (parts[4] === "reset" && request.method === "POST") {
      if (!(await authorized(request, env))) return json({ error: "unauthorized" }, 401, request, env);
      return json(await hub.reset(), 200, request, env);
    }
    // A new run on a clean codebase: restore the seed, then clear the board.
    if (parts[4] === "restore" && request.method === "POST") {
      if (!(await authorized(request, env))) return json({ error: "unauthorized" }, 401, request, env);
      const restored = await restoreSeed(env, { account, slice });
      const cleared = await hub.reset();
      // Import the restored codebase now, so the first agent does not wait.
      const baseline = await hub.importNow(account, slice).then(
        (b) => ({ baseline: b.repo }),
        (err) => ({ baselineError: String(err instanceof Error ? err.message : err) }),
      );
      return json({ ...restored, ...cleared, ...baseline }, 200, request, env);
    }
    if (request.method === "GET" && parts.length === 4) {
      const authed = await authorized(request, env);
      if (readAuthRequired(env) && !authed) return json({ error: "unauthorized" }, 401, request, env);
      const [stats, sessions] = await Promise.all([hub.stats(), hub.listSessions()]);
      return json(
        { account, slice, stats, sessions: authed ? sessions : sessions.map(redactSession) },
        200,
        request,
        env,
      );
    }
  }

  // POST /v1/assist: a Workers AI completion for agents that write their
  // changes with a model (the demo's docs agents).
  if (parts[1] === "assist" && request.method === "POST") {
    if (!(await authorized(request, env))) return json({ error: "unauthorized" }, 401, request, env);
    const body = (await request.json().catch(() => ({}))) as { instructions?: string; input?: string };
    if (!body.input) return json({ error: "input is required" }, 400, request, env);
    const started = Date.now();
    const raw = await withTimeout(
      env.AI.run("@cf/openai/gpt-oss-120b" as keyof AiModels, {
        instructions: String(body.instructions ?? "").slice(0, 4000),
        input: String(body.input).slice(0, 24000),
        reasoning: { effort: "low" },
      } as never),
      45_000,
      "model",
    );
    return json({ text: extractText(raw), model: "openai/gpt-oss-120b", ms: Date.now() - started }, 200, request, env);
  }

  // POST /v1/sessions
  if (parts[1] === "sessions" && parts.length === 2 && request.method === "POST") {
    if (!(await authorized(request, env))) return json({ error: "unauthorized" }, 401, request, env);
    const body = (await request.json().catch(() => ({}))) as {
      slice?: string;
      agent?: string;
      task?: string;
      intent?: string[];
      resume?: string;
    };
    const [account, slice] = (body.slice ?? defaultSlice).split("/");
    if (!account || !slice || !isAllowed(env, account, slice)) return json({ error: "unknown slice" }, 404, request, env);
    if (!body.agent || !body.task) return json({ error: "agent and task are required" }, 400, request, env);
    const { session, token } = await hubStub(env, account, slice).openSession({
      account,
      slice,
      agent: slugify(body.agent),
      task: String(body.task).slice(0, 300),
      intent: Array.isArray(body.intent) ? body.intent.map(String).slice(0, 50) : [],
      resume: body.resume,
    });
    return json(
      {
        id: session.id,
        remote: session.remote,
        token,
        git: {
          clone: `git -c http.extraHeader="Authorization: Bearer <token>" clone ${session.remote}`,
          push: "git push origin HEAD:main",
          workdir: `${account}/${slice}`,
        },
        baseline: { repo: session.baseline.repo, commit: session.baseline.gitCommit, gitslice: session.baseline.nativeCommit },
        signals: session.signals,
        status: `${new URL(request.url).origin}/v1/sessions/${session.id}`,
      },
      201,
      request,
      env,
    );
  }

  // GET /v1/sessions/:id, POST /v1/sessions/:id/pushed
  if (parts[1] === "sessions" && parts.length >= 3) {
    const id = parts[2];
    const where = parseSessionRepo(id);
    if (!where || !isAllowed(env, where.account, where.slice)) return json({ error: "unknown session" }, 404, request, env);
    const hub = hubStub(env, where.account, where.slice);
    if (request.method === "GET" && parts.length === 3) {
      const authed = await authorized(request, env);
      if (readAuthRequired(env) && !authed) return json({ error: "unauthorized" }, 401, request, env);
      const session = await hub.getSession(id);
      if (!session) return json({ error: "unknown session" }, 404, request, env);
      const visible = authed ? session : redactSession(session);
      return json({ ...visible, changesetUrl: visible.handle ? `${env.GITSLICE_WEB}/cs/${visible.handle}` : undefined }, 200, request, env);
    }
    if (request.method === "POST" && parts[3] === "pushed") {
      if (!(await authorized(request, env))) return json({ error: "unauthorized" }, 401, request, env);
      // Artifacts sends repo.pushed events on its own; this only saves the
      // wait. Landing dedupes by commit, so both paths are safe.
      using repo = await env.ARTIFACTS.get(id);
      const [head] = await repo.log({ limit: 1 });
      if (!head) return json({ error: "nothing pushed yet" }, 409, request, env);
      await hub.expectPush(id);
      // Land it right here instead of waiting for a queue consumer: the queue
      // scales its consumers up gradually, and a burst of pushes waited up to
      // a minute. The repo.pushed event still arrives as a safety net, and the
      // Hub's claim makes the second arrival a no-op.
      ctx.waitUntil(
        landPush(env, hubPort(env, where.account, where.slice), id, head.hash).catch((err) =>
          console.error(JSON.stringify({ msg: "direct landing failed", session: id, error: String(err) })),
        ),
      );
      return json({ landing: head.hash }, 202, request, env);
    }
  }
  return json({ error: "not found" }, 404, request, env);
}

interface LandingTarget {
  retry: boolean;
  session: string;
  commit: string;
  account: string;
  slice: string;
}

function landingTarget(body: unknown, env: Env): LandingTarget | null {
  const b = body as {
    type?: string;
    kind?: string;
    retry?: boolean;
    session?: string;
    commit?: string;
    source?: { namespace?: string; repoName?: string };
    payload?: { ref?: string; after?: string };
  };
  let session: string | undefined;
  let commit: string | undefined;
  if (b?.type === "cf.artifacts.repo.pushed") {
    if (b.source?.namespace !== env.ARTIFACTS_NAMESPACE) return null;
    if (b.payload?.ref !== "refs/heads/main") return null;
    session = b.source?.repoName;
    commit = b.payload?.after;
  } else if (b?.kind === "land") {
    session = b.session;
    commit = b.commit;
  }
  if (!session || !commit || /^0+$/.test(commit)) return null;
  const where = parseSessionRepo(session);
  if (!where || !isAllowed(env, where.account, where.slice)) return null;
  return { session, commit, retry: Boolean(b?.retry), ...where };
}

function hubStub(env: Env, account: string, slice: string): HubApi {
  return env.HUB.get(env.HUB.idFromName(`${account}/${slice}`)) as unknown as HubApi;
}

function hubPort(env: Env, account: string, slice: string): HubPort {
  return hubStub(env, account, slice);
}

function nativeOn(env: Env, account: string, slice: string): boolean {
  return (env.NATIVE_SLICES || "").split(",").map((s) => s.trim()).includes(`${account}/${slice}`);
}

function allowedSlices(env: Env): string[] {
  return (env.SLICES || "")
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
}

function isAllowed(env: Env, account: string, slice: string): boolean {
  return allowedSlices(env).includes(`${account}/${slice}`);
}

function json(body: unknown, status = 200, request?: Request, env?: Env): Response {
  const cors = request && env ? corsHeaders(request, env) : {};
  return new Response(JSON.stringify(body, null, 2), {
    status,
    headers: { "content-type": "application/json; charset=utf-8", ...cors },
  });
}

function html(body: string): Response {
  return new Response(body, {
    headers: {
      "content-type": "text/html; charset=utf-8",
      "x-content-type-options": "nosniff",
    },
  });
}
