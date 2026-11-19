import assert from "node:assert/strict";
import { describe, it } from "node:test";

import type { StartResponse } from "../src/api.ts";
import { handleBridgeRequest, inputEnvName, prepareBashRun, type BashBootstrapPayload } from "../src/bash_config.ts";
import { openPlaceholders, sealKey } from "../src/crypto.ts";
import { expandSecretVariants, maskSecrets } from "../src/mask.ts";

const DISPATCH_SECRET = "dispatch-test-secret-0123456789";

function sampleStart(overrides: Partial<StartResponse> = {}): StartResponse {
  return {
    run: {
      id: "trun_bash_1",
      account: "acme",
      triggerId: "infra/deploy",
      folder: "/acme/infra",
      status: "running",
      eventKind: "manual",
      definitionCommitId: "sha256:def",
      targetRef: "refs/global/main",
    },
    action: "bash",
    eventJson: JSON.stringify({ event: "manual", inputs: { version: "v1.4.2" } }),
    modules: {},
    mainModule: "",
    codeHash: "abc123",
    secrets: { CF_TOKEN: "cf-real-token-xyz", SIGN_KEY: "sign-raw-secret-789" },
    reveal: ["SIGN_KEY"],
    network: ["api.cloudflare.com"],
    egress: "http-gate",
    runToken: "gstr_run_token_secret",
    deadlineUnixMs: Date.now() + 300_000,
    bashScript: "echo hi",
    bashRuntime: "cloud",
    bashSize: "basic",
    bashCheckout: "folder",
    bashWorkingDir: "deploy",
    bashEnv: { APP_ENV: "staging" },
    inputs: { version: "v1.4.2", "dry-run": "true" },
    timeoutMs: 600_000,
    ...overrides,
  };
}

describe("prepareBashRun", () => {
  it("seals non-revealed secrets for Gate and exposes revealed secrets verbatim", async () => {
    const scope = "acme:/acme/infra:abc123";
    const prepared = await prepareBashRun(DISPATCH_SECRET, sampleStart(), scope);

    assert.equal(prepared.egress, "http-gate");
    assert.equal(prepared.enableInternet, false);
    assert.equal(prepared.runtime, "cloud");
    assert.equal(prepared.size, "basic");
    assert.equal(prepared.bootstrap.folder_rel, "infra");
    assert.equal(prepared.bootstrap.working_dir, "deploy");
    assert.equal(prepared.bootstrap.env.INPUT_VERSION, "v1.4.2");
    assert.equal(prepared.bootstrap.env.INPUT_DRY_RUN, "true");
    assert.equal(prepared.bootstrap.env.APP_ENV, "staging");
    assert.equal(prepared.bootstrap.env.GITSLICE_AUTOMATION_ID, "infra/deploy");

    // CF_TOKEN is sealed (never plaintext in container env or secret file),
    // and unseals cleanly inside Gate with matching scope.
    assert.match(prepared.bootstrap.env.CF_TOKEN, /^gs-secret:v1:/);
    assert.equal(prepared.bootstrap.secret_files.CF_TOKEN, prepared.bootstrap.env.CF_TOKEN);
    const key = await sealKey(DISPATCH_SECRET);
    assert.equal(await openPlaceholders(key, prepared.bootstrap.env.CF_TOKEN, scope), "cf-real-token-xyz");

    // SIGN_KEY was explicitly listed in reveal -> plaintext in env + secret_files.
    assert.equal(prepared.bootstrap.env.SIGN_KEY, "sign-raw-secret-789");
    assert.equal(prepared.bootstrap.secret_files.SIGN_KEY, "sign-raw-secret-789");
    assert.equal(prepared.bootstrap.env.RUN_TOKEN, undefined);
  });

  it("enables direct internet only for egress=direct and stays offline when network is empty", async () => {
    const direct = await prepareBashRun(
      DISPATCH_SECRET,
      sampleStart({ egress: "direct", network: [], secrets: { DB: "pg://secret" }, reveal: ["DB"] }),
      "s",
    );
    assert.equal(direct.egress, "direct");
    assert.equal(direct.enableInternet, true);

    const offline = await prepareBashRun(
      DISPATCH_SECRET,
      sampleStart({ egress: "", network: [], secrets: {}, reveal: [] }),
      "s",
    );
    assert.equal(offline.egress, "offline");
    assert.equal(offline.enableInternet, false);
  });

  it("maps input names to INPUT_<UPPER_SNAKE>", () => {
    assert.equal(inputEnvName("dry-run"), "INPUT_DRY_RUN");
    assert.equal(inputEnvName("region"), "INPUT_REGION");
  });
});

describe("maskSecrets", () => {
  it("masks raw, multi-line, base64, and URL-encoded secret variants", () => {
    const secret = "top/secret+value";
    const variants = expandSecretVariants([secret, "abc"]); // "abc" < 4 chars ignored
    const b64 = btoa(secret);
    const urlEnc = encodeURIComponent(secret);
    const input = `raw=${secret} b64=${b64} url=${urlEnc} short=abc`;
    const masked = maskSecrets(input, variants);
    assert.equal(masked, "raw=*** b64=*** url=*** short=abc");
  });
});

describe("handleBridgeRequest", () => {
  it("burns bootstrap nonce after first read, masks logs, and destroys container on finish", async () => {
    let payload: BashBootstrapPayload | null = {
      script: "echo ok",
      event_json: "{}",
      env: { API_KEY: "real-secret-value" },
      secret_files: {},
      checkout: "none",
      folder_rel: "infra",
      working_dir: ".",
      timeout_ms: 60_000,
    };
    const expectedNonce = "nonce-123456";
    let destroyed = false;
    const calls: { method: string; body: unknown }[] = [];

    const ctx = {
      api: {
        async call<T>(method: string, body: unknown): Promise<T> {
          calls.push({ method, body });
          return { ok: true } as T;
        },
        async fetchArchive() {
          return new Response("tarbytes", { status: 200, headers: { "content-type": "application/gzip" } });
        },
      },
      runToken: "gstr_tok",
      secretValues: ["real-secret-value"],
      backend: {
        async consumeBootstrap(nonce: string) {
          if (!payload || nonce !== expectedNonce) return null;
          const out = payload;
          payload = null;
          return out;
        },
        async finishContainer() {
          destroyed = true;
        },
      },
    };

    // Wrong nonce rejected.
    const bad = await handleBridgeRequest(
      new Request("http://gitslice.internal/bootstrap", { headers: { "x-bootstrap-nonce": "wrong" } }),
      ctx,
    );
    assert.equal(bad.status, 403);

    // First valid bootstrap succeeds.
    const first = await handleBridgeRequest(
      new Request("http://gitslice.internal/bootstrap", { headers: { "x-bootstrap-nonce": expectedNonce } }),
      ctx,
    );
    assert.equal(first.status, 200);
    assert.equal(((await first.json()) as BashBootstrapPayload).script, "echo ok");

    // Second attempt with same nonce is rejected (already burned).
    const replay = await handleBridgeRequest(
      new Request("http://gitslice.internal/bootstrap", { headers: { "x-bootstrap-nonce": expectedNonce } }),
      ctx,
    );
    assert.equal(replay.status, 403);

    // Logs scrub secret before forwarding to AppendRunLog.
    const logRes = await handleBridgeRequest(
      new Request("http://gitslice.internal/logs", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ lines: [{ stream: "stdout", text: "leaked real-secret-value here" }] }),
      }),
      ctx,
    );
    assert.equal(logRes.status, 200);
    assert.equal(calls[0].method, "AppendRunLog");
    assert.deepEqual((calls[0].body as { lines: unknown[] }).lines, [
      { stream: "stdout", text: "leaked *** here" },
    ]);

    // Finish reports to FinishRun and triggers container destruction.
    const finRes = await handleBridgeRequest(
      new Request("http://gitslice.internal/finish", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ outcome: "succeeded", exit_code: 0, error: "" }),
      }),
      ctx,
    );
    assert.equal(finRes.status, 200);
    assert.equal(destroyed, true);
    assert.equal(calls[1].method, "FinishRun");
  });
});
