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
import { restoreSeed } from "./restore";
import { extractText } from "./review";
import type { Baseline, Session, Stats } from "./types";

// HubApi is the Hub's RPC surface as the Worker uses it. Typing the stub
// through it keeps TypeScript from expanding the recursive RPC types.
interface HubApi extends HubPort {
  fetch(request: Request): Promise<Response>;
  openSession(input: OpenSessionInput): Promise<{ session: Session; token: string }>;
  getSession(id: string): Promise<Session | null>;
  listSessions(): Promise<Session[]>;
  stats(): Promise<Stats>;
  reset(): Promise<{ deleted: number }>;
  adoptBaseline(repo: string): Promise<Baseline>;
  importNow(account: string, slice: string): Promise<Baseline>;
}

export { Hub };

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    try {
      return await route(request, env);
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      return json({ error: message }, 500);
    }
  },

  async queue(batch: MessageBatch<unknown>, env: Env): Promise<void> {
    await Promise.all(
      batch.messages.map(async (message) => {
        const target = landingTarget(message.body, env);
        if (!target) {
          message.ack();
          return;
        }
        try {
          await landPush(env, hubPort(env, target.account, target.slice), target.session, target.commit);
          message.ack();
        } catch (err) {
          console.error(JSON.stringify({ msg: "landing crashed", target, error: String(err) }));
          message.retry({ delaySeconds: 2 });
        }
      }),
    );
  },
} satisfies ExportedHandler<Env, unknown>;

async function route(request: Request, env: Env): Promise<Response> {
  const url = new URL(request.url);
  const parts = url.pathname.split("/").filter(Boolean);
  const defaultSlice = allowedSlices(env)[0] ?? "demo/storefront";

  if (request.method === "GET" && parts.length === 0) {
    return Response.redirect(`${url.origin}/slices/${defaultSlice}`, 302);
  }
  if (request.method === "GET" && parts[0] === "slices" && parts.length === 3) {
    return html(dashboardHtml({ account: parts[1], slice: parts[2], gitsliceWeb: env.GITSLICE_WEB, gitsliceGit: env.GITSLICE_GIT }));
  }
  if (parts[0] !== "v1") return json({ error: "not found" }, 404);

  // GET /v1/slices/:account/:slice[/stream]
  if (parts[1] === "slices" && parts.length >= 4) {
    const [account, slice] = [parts[2], parts[3]];
    if (!isAllowed(env, account, slice)) return json({ error: "unknown slice" }, 404);
    const hub = hubStub(env, account, slice);
    if (parts[4] === "stream") return hub.fetch(request);
    if (parts[4] === "baseline" && request.method === "POST") {
      if (!authorized(request, env)) return json({ error: "unauthorized" }, 401);
      const body = (await request.json().catch(() => ({}))) as { action?: string; repo?: string };
      if (body.action === "create") {
        // An empty repo the caller pushes the slice's projection to.
        const created = await env.ARTIFACTS.create(`base.${account}.${slice}.${Date.now().toString(36)}`, {
          description: `Baseline of ${account}/${slice}`,
        });
        return json({ repo: created.name, remote: created.remote, token: created.token }, 201);
      }
      if (body.action === "adopt" && body.repo) return json(await hub.adoptBaseline(body.repo));
      if (body.action === "import") return json(await hub.importNow(account, slice));
      return json({ error: 'action must be "create", "adopt" or "import"' }, 400);
    }
    if (parts[4] === "reset" && request.method === "POST") {
      if (!authorized(request, env)) return json({ error: "unauthorized" }, 401);
      return json(await hub.reset());
    }
    // A new run on a clean codebase: restore the seed, then clear the board.
    if (parts[4] === "restore" && request.method === "POST") {
      if (!authorized(request, env)) return json({ error: "unauthorized" }, 401);
      const restored = await restoreSeed(env, { account, slice });
      const cleared = await hub.reset();
      // Import the restored codebase now, so the first agent does not wait.
      const baseline = await hub.importNow(account, slice).then(
        (b) => ({ baseline: b.repo }),
        (err) => ({ baselineError: String(err instanceof Error ? err.message : err) }),
      );
      return json({ ...restored, ...cleared, ...baseline });
    }
    if (request.method === "GET" && parts.length === 4) {
      const [stats, sessions] = await Promise.all([hub.stats(), hub.listSessions()]);
      return json({ account, slice, stats, sessions });
    }
  }

  // POST /v1/assist: a Workers AI completion for agents that write their
  // changes with a model (the demo's docs agents).
  if (parts[1] === "assist" && request.method === "POST") {
    if (!authorized(request, env)) return json({ error: "unauthorized" }, 401);
    const body = (await request.json().catch(() => ({}))) as { instructions?: string; input?: string };
    if (!body.input) return json({ error: "input is required" }, 400);
    const started = Date.now();
    const raw = await env.AI.run("@cf/openai/gpt-oss-120b" as keyof AiModels, {
      instructions: String(body.instructions ?? "").slice(0, 4000),
      input: String(body.input).slice(0, 24000),
      reasoning: { effort: "low" },
    } as never);
    return json({ text: extractText(raw), model: "openai/gpt-oss-120b", ms: Date.now() - started });
  }

  // POST /v1/sessions
  if (parts[1] === "sessions" && parts.length === 2 && request.method === "POST") {
    if (!authorized(request, env)) return json({ error: "unauthorized" }, 401);
    const body = (await request.json().catch(() => ({}))) as {
      slice?: string;
      agent?: string;
      task?: string;
      intent?: string[];
      resume?: string;
    };
    const [account, slice] = (body.slice ?? defaultSlice).split("/");
    if (!account || !slice || !isAllowed(env, account, slice)) return json({ error: "unknown slice" }, 404);
    if (!body.agent || !body.task) return json({ error: "agent and task are required" }, 400);
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
    );
  }

  // GET /v1/sessions/:id, POST /v1/sessions/:id/pushed
  if (parts[1] === "sessions" && parts.length >= 3) {
    const id = parts[2];
    const where = parseSessionRepo(id);
    if (!where || !isAllowed(env, where.account, where.slice)) return json({ error: "unknown session" }, 404);
    const hub = hubStub(env, where.account, where.slice);
    if (request.method === "GET" && parts.length === 3) {
      const session = await hub.getSession(id);
      if (!session) return json({ error: "unknown session" }, 404);
      return json({ ...session, changesetUrl: session.handle ? `${env.GITSLICE_WEB}/cs/${session.handle}` : undefined });
    }
    if (request.method === "POST" && parts[3] === "pushed") {
      if (!authorized(request, env)) return json({ error: "unauthorized" }, 401);
      // Artifacts sends repo.pushed events on its own; this only saves the
      // wait. Landing dedupes by commit, so both paths are safe.
      using repo = await env.ARTIFACTS.get(id);
      const [head] = await repo.log({ limit: 1 });
      if (!head) return json({ error: "nothing pushed yet" }, 409);
      await env.EVENTS.send({ kind: "land", session: id, commit: head.hash });
      return json({ queued: head.hash }, 202);
    }
  }
  return json({ error: "not found" }, 404);
}

interface LandingTarget {
  session: string;
  commit: string;
  account: string;
  slice: string;
}

function landingTarget(body: unknown, env: Env): LandingTarget | null {
  const b = body as {
    type?: string;
    kind?: string;
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
  return { session, commit, ...where };
}

function hubStub(env: Env, account: string, slice: string): HubApi {
  return env.HUB.get(env.HUB.idFromName(`${account}/${slice}`)) as unknown as HubApi;
}

function hubPort(env: Env, account: string, slice: string): HubPort {
  return hubStub(env, account, slice);
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

// authorized accepts any of the comma-separated keys in AGENTS_API_KEY, so a
// key handed to one group of agents can be revoked on its own.
function authorized(request: Request, env: Env): boolean {
  const header = request.headers.get("authorization") ?? "";
  if (!header.startsWith("Bearer ")) return false;
  const presented = header.slice("Bearer ".length).trim();
  return (env.AGENTS_API_KEY || "")
    .split(",")
    .map((k) => k.trim())
    .some((k) => k.length >= 16 && k === presented);
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body, null, 2), {
    status,
    headers: { "content-type": "application/json; charset=utf-8", "access-control-allow-origin": "*" },
  });
}

function html(body: string): Response {
  return new Response(body, { headers: { "content-type": "text/html; charset=utf-8" } });
}
