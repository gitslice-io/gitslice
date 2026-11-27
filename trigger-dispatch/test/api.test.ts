import assert from "node:assert/strict";
import { test } from "node:test";

import { Api, ApiError, parseConnectErrorEnvelope } from "../src/api.ts";

test("ApiError and parseConnectErrorEnvelope parse Connect JSON error details, code, message, and status", async () => {
  const body = JSON.stringify({
    code: "failed_precondition",
    message: "trigger run lease expired",
    details: [
      {
        type: "gitslice.core.v1.TriggerRunDetail",
        value: "eyJydW5faWQiOiJ0cnVuXzEifQ==",
        debug: { runId: "trun_1" },
      },
    ],
  });

  const env = parseConnectErrorEnvelope(body, "Start: HTTP 400");
  assert.equal(env.code, "failed_precondition");
  assert.equal(env.message, "trigger run lease expired");
  assert.equal(env.details.length, 1);
  assert.equal(env.details[0].type, "gitslice.core.v1.TriggerRunDetail");

  const fromRes = ApiError.fromResponse(400, body, "Start: HTTP 400");
  assert.equal(fromRes.code, "failed_precondition");
  assert.equal(fromRes.status, 400);
  assert.equal(fromRes.message, "trigger run lease expired");
  assert.equal(fromRes.details.length, 1);

  const emptyJson = ApiError.fromResponse(503, "{}", "Start: HTTP 503");
  assert.equal(emptyJson.code, "unknown");
  assert.equal(emptyJson.message, "Start: HTTP 503");
  assert.equal(emptyJson.status, 503);
  assert.deepEqual(emptyJson.details, []);

  const nonJson = ApiError.fromResponse(502, "upstream gateway timeout", "Start: HTTP 502");
  assert.equal(nonJson.code, "unknown");
  assert.equal(nonJson.message, "upstream gateway timeout");
  assert.equal(nonJson.status, 502);
  assert.deepEqual(nonJson.details, []);

  const origFetch = globalThis.fetch;
  try {
    globalThis.fetch = async () =>
      new Response(body, {
        status: 409,
        headers: { "content-type": "application/json" },
      });
    const api = new Api("https://api.gitslice.io/");
    await assert.rejects(
      () => api.call("Start", { runId: "trun_1" }),
      (thrown: unknown) => {
        assert.ok(thrown instanceof ApiError);
        assert.equal(thrown.code, "failed_precondition");
        assert.equal(thrown.status, 409);
        assert.equal(thrown.message, "trigger run lease expired");
        assert.equal(thrown.details.length, 1);
        return true;
      },
    );
  } finally {
    globalThis.fetch = origFetch;
  }
});

test("Api.call retries transient 502/503 responses with backoff and does not retry 4xx", async () => {
  const origFetch = globalThis.fetch;
  try {
    let attempts = 0;
    globalThis.fetch = async () => {
      attempts++;
      if (attempts < 3) {
        return new Response("upstream unavailable", { status: attempts === 1 ? 502 : 503 });
      }
      return new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    };
    const api = new Api("https://api.gitslice.io", { retryDelaysMs: [0, 0] });
    const res = await api.call<{ ok: boolean }>("Heartbeat", { runId: "trun_1" });
    assert.deepEqual(res, { ok: true });
    assert.equal(attempts, 3);

    attempts = 0;
    globalThis.fetch = async () => {
      attempts++;
      return new Response(JSON.stringify({ code: "invalid_argument", message: "bad input" }), { status: 400 });
    };
    await assert.rejects(() => api.call("Heartbeat", { runId: "trun_1" }), ApiError);
    assert.equal(attempts, 1);
  } finally {
    globalThis.fetch = origFetch;
  }
});
