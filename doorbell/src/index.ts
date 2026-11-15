// The CI doorbell (design/32_ci_dispatch_on_cloudflare.md, layer 1). A
// detached CI daemon opens a WebSocket at /wait with its doorbell token, and
// the Gitslice server POSTs /ring when a check is queued for it; the daemon's
// Bell sends "wake" down every socket it holds, and the daemon connects to
// the server for its runs. Messages carry ids only: runs, specs and secrets
// travel between the daemon and the server as before.

import { Container } from "@cloudflare/containers";
import { DurableObject } from "cloudflare:workers";

import { verifyRing, verifyToken } from "./auth.ts";
import { runnersToStart } from "./runners.ts";

export interface Env {
  BELL: DurableObjectNamespace<Bell>;
  RUNNER: DurableObjectNamespace<Runner>;
  DOORBELL_SECRET: string;
  // Cloudflare-hosted runners (design/32 layer 2).
  GITSLICE_SERVER_ADDR: string;
  RUNNER_DAEMON_ID: string;
  RUNNER_NAME: string;
  MAX_RUNNERS: string;
  RUNNER_TOKEN: string;
  RUNNER_SUBJECT: string;
}

// A runner that has run this long is stopped, whatever it is doing; a check
// times out well before (30 m at most in the gitslice checks).
const MAX_RUNNER_MS = 2 * 60 * 60 * 1000;

// Runner is one Cloudflare-hosted CI runner (runner/): a container running a
// checks-only daemon that takes queued runs one at a time and exits once
// idle (gs agent start --idle-timeout). Its processes register as the
// daemon RUNNER_NAME of the RUNNER_TOKEN agent account.
export class Runner extends Container<Env> {
  sleepAfter = "10m";
  enableInternet = true;

  // begin starts the runner unless it is running; it says whether it did.
  async begin(): Promise<boolean> {
    const state = await this.getState();
    if (state.status === "running" || state.status === "healthy") return false;
    await this.ctx.storage.put("startedAt", Date.now());
    await this.start({
      envVars: {
        GITSLICE_SERVER_ADDR: this.env.GITSLICE_SERVER_ADDR,
        GITSLICE_RUNNER_TOKEN: this.env.RUNNER_TOKEN,
        GITSLICE_RUNNER_SUBJECT: this.env.RUNNER_SUBJECT,
        GITSLICE_RUNNER_NAME: this.env.RUNNER_NAME || "cf-ci",
      },
    });
    return true;
  }

  // The runner gets no requests, so its activity timer always runs out: it
  // keeps running (the daemon exits by itself) unless it ran too long.
  override async onActivityExpired(): Promise<void> {
    const startedAt = (await this.ctx.storage.get<number>("startedAt")) ?? 0;
    if (Date.now() - startedAt > MAX_RUNNER_MS) {
      console.warn("stopping a runner that ran too long");
      await this.stop();
    }
  }

  override onStop(params: { exitCode?: number; reason?: string }): void {
    console.log("runner stopped", JSON.stringify(params));
  }
}

// startRunners starts up to queued runners, at most MAX_RUNNERS running, and
// returns how many are running or starting.
async function startRunners(env: Env, queued: number): Promise<number> {
  const slots = Math.max(1, Number(env.MAX_RUNNERS) || 1);
  let started = 0;
  let running = 0;
  for (let i = 0; i < slots; i++) {
    const runner = env.RUNNER.get(env.RUNNER.idFromName(`runner-${i}`));
    if (started >= runnersToStart(queued, running, slots)) {
      break;
    }
    if (await runner.begin()) {
      started++;
    } else {
      running++;
    }
  }
  return started + running;
}

// A ring for a daemon with no socket open waits this long for it.
const PENDING_FOR_MS = 60 * 60 * 1000;
const MAX_RING_SKEW_S = 300;

// Bell holds one daemon's waiting sockets. It hibernates between rings:
// "ping" is answered without waking it.
export class Bell extends DurableObject<Env> {
  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    ctx.setWebSocketAutoResponse(new WebSocketRequestResponsePair("ping", "pong"));
  }

  // fetch takes a daemon's WebSocket. A ring that came while it had none
  // open is delivered at once.
  async fetch(_request: Request): Promise<Response> {
    const pair = new WebSocketPair();
    const [client, server] = Object.values(pair);
    this.ctx.acceptWebSocket(server);
    const pending = await this.ctx.storage.get<number>("pending");
    if (pending !== undefined) {
      await this.ctx.storage.delete("pending");
      if (Date.now() - pending < PENDING_FOR_MS) server.send("wake");
    }
    return new Response(null, { status: 101, webSocket: client });
  }

  // ring wakes every waiting socket and returns how many there were, or for
  // the runners' daemon starts runners for the queued runs. With none, the
  // ring waits for the daemon's next socket.
  async ring(daemon: string, queued: number): Promise<number> {
    if (daemon && daemon === this.env.RUNNER_DAEMON_ID) {
      return startRunners(this.env, queued);
    }
    let listening = 0;
    for (const ws of this.ctx.getWebSockets()) {
      try {
        ws.send("wake");
        listening++;
      } catch {
        // closing already
      }
    }
    if (listening === 0) await this.ctx.storage.put("pending", Date.now());
    return listening;
  }

  async webSocketMessage(_ws: WebSocket, _message: string | ArrayBuffer): Promise<void> {
    // Daemons only ping, which the auto-response answers.
  }

  async webSocketClose(ws: WebSocket, code: number, reason: string): Promise<void> {
    try {
      ws.close(code, reason);
    } catch {
      // closed already
    }
  }
}

function bearer(request: Request): string | null {
  const header = request.headers.get("authorization") ?? "";
  return header.startsWith("Bearer ") ? header.slice(7).trim() : null;
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);
    if (url.pathname === "/wait") {
      if (request.headers.get("upgrade")?.toLowerCase() !== "websocket") {
        return new Response("expected a WebSocket", { status: 426 });
      }
      const daemon = await verifyToken(env.DOORBELL_SECRET, bearer(request));
      if (!daemon) return new Response("bad or expired doorbell token", { status: 401 });
      return env.BELL.get(env.BELL.idFromName(daemon)).fetch(request);
    }
    if (url.pathname === "/ring" && request.method === "POST") {
      const body = await request.text();
      if (!(await verifyRing(env.DOORBELL_SECRET, body, request.headers.get("x-gitslice-doorbell-signature")))) {
        return new Response("bad signature", { status: 401 });
      }
      let msg: { daemon_id?: string; ts?: number; queued?: number };
      try {
        msg = JSON.parse(body);
      } catch {
        return new Response("bad body", { status: 400 });
      }
      if (!msg.daemon_id || !msg.ts || Math.abs(Date.now() / 1000 - msg.ts) > MAX_RING_SKEW_S) {
        return new Response("stale or incomplete ring", { status: 400 });
      }
      const listening = await env.BELL.get(env.BELL.idFromName(msg.daemon_id)).ring(msg.daemon_id, Math.max(0, Number(msg.queued) || 0));
      return Response.json({ listening });
    }
    if (url.pathname === "/") return new Response("Gitslice CI doorbell\n");
    return new Response("not found", { status: 404 });
  },
} satisfies ExportedHandler<Env>;
