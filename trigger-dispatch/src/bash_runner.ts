// BashRunner Durable Object & GsBridge WorkerEntrypoint for executing
// `action: { bash: ... }` automations inside single-run Cloudflare Containers
// (`scheduling_policy: "durable_object"`).

import { DurableObject, WorkerEntrypoint } from "cloudflare:workers";

import { Api, ApiError, triggerScope, type StartResponse } from "./api.ts";
import {
  handleBridgeRequest,
  prepareBashRun,
  verifyBootstrapNonce,
  type BashBootstrapPayload,
} from "./bash_config.ts";
import type { Env } from "./index.ts";

interface BridgeProps {
  runId: string;
  runToken: string;
  secretValues: string[];
}

// Raw Durable Object container interface for `scheduling_policy: "durable_object"`.
interface DynamicContainer {
  readonly running: boolean;
  readonly images?: Record<string, unknown>;
  start(options?: {
    image?: unknown;
    instance?: string;
    enableInternet?: boolean;
    env?: Record<string, string>;
  }): Promise<void> | void;
  destroy(error?: unknown): Promise<void>;
  interceptOutboundHttp?(host: string, entrypoint: Fetcher): Promise<void>;
  interceptOutboundHttps?(host: string, entrypoint: Fetcher): Promise<void>;
}

interface ActiveBashState {
  runId: string;
  runToken: string;
  bootstrap: BashBootstrapPayload | null;
  nonce: string;
  consumed: boolean;
  finished: boolean;
}

function randomHex(bytesLen: number): string {
  const buf = new Uint8Array(bytesLen);
  crypto.getRandomValues(buf);
  return [...buf].map((b) => b.toString(16).padStart(2, "0")).join("");
}

// GsBridge intercepts http://gitslice.internal/* calls from inside a bash
// container, handling one-time secret bootstrap, checkout archive streaming,
// secret-masked log flushing, gs-auto helper calls, and teardown.
export class GsBridge extends WorkerEntrypoint<Env, BridgeProps> {
  async fetch(request: Request): Promise<Response> {
    const { runId, runToken, secretValues } = this.ctx.props;
    const api = new Api(this.env.GITSLICE_API);
    const runner = this.env.BASH_RUNNER.get(this.env.BASH_RUNNER.idFromName(runId));
    return handleBridgeRequest(request, {
      api,
      runToken,
      secretValues,
      backend: {
        consumeBootstrap: (nonce) => runner.consumeBootstrap(nonce),
        finishContainer: () => runner.finishAndDestroy(),
      },
    });
  }
}

// BashRunner owns one ephemeral Cloudflare Container per automation runId.
// Secrets and bootstrap payloads stay strictly in RAM (never DO SQLite).
export class BashRunner extends DurableObject<Env> {
  private active: ActiveBashState | null = null;

  private get container(): DynamicContainer {
    return (this.ctx as unknown as { container: DynamicContainer }).container;
  }

  async startRun(runId: string): Promise<void> {
    const api = new Api(this.env.GITSLICE_API);
    let start: StartResponse;
    try {
      start = await api.call<StartResponse>("StartRun", {
        runId,
        dispatcherKey: this.env.DISPATCH_SECRET,
      });
    } catch (err) {
      if (err instanceof ApiError && err.code === "failed_precondition") {
        return; // already claimed or canceled
      }
      console.error("bash StartRun failed", runId, err);
      return;
    }

    const scope = triggerScope(start);
    const prepared = await prepareBashRun(this.env.DISPATCH_SECRET, start, scope);
    const nonce = randomHex(32);

    this.active = {
      runId,
      runToken: start.runToken,
      bootstrap: prepared.bootstrap,
      nonce,
      consumed: false,
      finished: false,
    };

    const container = this.container;
    const exports = (
      this.ctx as unknown as {
        exports: {
          Gate: (opts: { props: { network: string[]; scope: string } }) => Fetcher;
          GsBridge: (opts: { props: BridgeProps }) => Fetcher;
        };
      }
    ).exports;

    try {
      // Always intercept http://gitslice.internal for one-time bootstrap,
      // archive streaming, masked logs, and gs-auto helpers.
      if (container.interceptOutboundHttp) {
        await container.interceptOutboundHttp(
          "gitslice.internal",
          exports.GsBridge({
            props: {
              runId,
              runToken: start.runToken,
              secretValues: prepared.secretValues,
            },
          }),
        );
      }

      // In http-gate mode, disable direct internet and route all outbound HTTPS
      // through Gate so sealed placeholders unwrap only for allowlisted hosts.
      if (prepared.egress === "http-gate" && container.interceptOutboundHttps) {
        await container.interceptOutboundHttps(
          "*",
          exports.Gate({ props: { network: prepared.network, scope: prepared.scope } }),
        );
      }

      // Hard watchdog alarm guarantees container teardown even if gs-exec hangs.
      await this.ctx.storage.setAlarm(Date.now() + prepared.timeoutMs + 15_000);

      const image = container.images?.[prepared.runtime] ?? container.images?.base;
      await container.start({
        image,
        instance: prepared.size,
        enableInternet: prepared.enableInternet,
        // Never pass secrets in the container's start env — only the one-time
        // nonce. (The option is `env`; an unknown option such as `envVars` is
        // ignored, and gs-exec then exits for want of BOOTSTRAP_NONCE.)
        env: {
          BOOTSTRAP_NONCE: nonce,
          GITSLICE_BRIDGE_URL: "http://gitslice.internal",
        },
      });
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      console.error("start bash container", runId, msg);
      await this.failAndDestroy(api, start.runToken, `failed to start container: ${msg}`);
    }
  }

  // consumeBootstrap verifies and atomically burns the single-use nonce so
  // user bash code can never re-fetch bootstrap secrets.
  async consumeBootstrap(nonce: string): Promise<BashBootstrapPayload | null> {
    const state = this.active;
    if (!state || state.consumed || !state.bootstrap) {
      return null;
    }
    if (!verifyBootstrapNonce(state.nonce, nonce)) {
      return null;
    }
    const payload = state.bootstrap;
    state.consumed = true;
    state.bootstrap = null;
    state.nonce = "";
    return payload;
  }

  async finishAndDestroy(): Promise<void> {
    if (this.active) {
      this.active.finished = true;
      this.active.bootstrap = null;
      this.active.nonce = "";
    }
    await this.ctx.storage.deleteAlarm().catch(() => {});
    await this.destroyQuietly();
    this.active = null;
  }

  async cancelRun(): Promise<void> {
    if (this.active) {
      this.active.finished = true;
      this.active.bootstrap = null;
      this.active.nonce = "";
    }
    await this.ctx.storage.deleteAlarm().catch(() => {});
    await this.destroyQuietly();
    this.active = null;
  }

  override async alarm(): Promise<void> {
    const state = this.active;
    if (state && !state.finished) {
      const api = new Api(this.env.GITSLICE_API);
      await this.failAndDestroy(api, state.runToken, "bash automation exceeded container watchdog timeout");
      return;
    }
    await this.destroyQuietly();
    this.active = null;
  }

  private async failAndDestroy(api: Api, runToken: string, error: string): Promise<void> {
    if (this.active) {
      this.active.finished = true;
      this.active.bootstrap = null;
      this.active.nonce = "";
    }
    try {
      await api.call("FinishRun", {
        runToken,
        outcome: "errored",
        error,
        wait: false,
        finish: true,
        callback: false,
      });
    } catch {
      // Server lease sweep handles unreachable API.
    }
    await this.ctx.storage.deleteAlarm().catch(() => {});
    await this.destroyQuietly();
    this.active = null;
  }

  private async destroyQuietly(): Promise<void> {
    try {
      await this.container.destroy();
    } catch {
      // Container already stopped or never started.
    }
  }
}
