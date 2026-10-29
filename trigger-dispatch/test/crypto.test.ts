import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import { test } from "node:test";

import { openPlaceholders, PlaceholderError, seal, sealKey, sign, verifyDispatch } from "../src/crypto.ts";
import { hostAllowed, textual } from "../src/gate.ts";

test("sign matches the server's HMAC layout", async () => {
  const mac = createHmac("sha256", "secret");
  mac.update("dispatch");
  mac.update(Buffer.from([0]));
  mac.update('{"run_id":"trun_1"}');
  const want = mac.digest("hex");
  assert.equal(await sign("secret", "dispatch", '{"run_id":"trun_1"}'), want);
  assert.ok(await verifyDispatch("secret", '{"run_id":"trun_1"}', want));
  assert.equal(await verifyDispatch("secret", "{}", "00"), false);
  assert.equal(await verifyDispatch("", "{}", "00"), false);
});

test("placeholders open only for their script, before they expire", async () => {
  const key = await sealKey("secret");
  const p = await seal(key, { v: "tok3n", code: "abc", exp: Date.now() + 60_000 });
  assert.match(p, /^gs-secret:v1:[A-Za-z0-9_-]+$/);
  assert.equal(await openPlaceholders(key, `Bearer ${p} and ${p}`, "abc"), "Bearer tok3n and tok3n");
  await assert.rejects(openPlaceholders(key, p, "other"), PlaceholderError);
  await assert.rejects(openPlaceholders(key, p, "abc", Date.now() + 120_000), PlaceholderError);
  const otherKey = await sealKey("other secret");
  await assert.rejects(openPlaceholders(otherKey, p, "abc"), PlaceholderError);
  assert.equal(await openPlaceholders(key, "no secrets here", "abc"), "no secrets here");
});

test("hosts", () => {
  const network = ["api.github.com", "*.example.com"];
  assert.ok(hostAllowed("api.github.com", network));
  assert.ok(hostAllowed("API.GitHub.com.", network));
  assert.ok(hostAllowed("a.b.example.com", network));
  assert.ok(!hostAllowed("example.com", network));
  assert.ok(!hostAllowed("evil-example.com", network));
  assert.ok(!hostAllowed("github.com", network));
  assert.ok(!hostAllowed("10.0.0.1", ["10.0.0.1"]));
  assert.ok(!hostAllowed("anything", []));
  assert.ok(textual("application/json; charset=utf-8"));
  assert.ok(!textual("application/octet-stream"));
});
