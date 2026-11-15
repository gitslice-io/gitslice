import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import { test } from "node:test";

import { sign, verifyRing, verifyToken } from "../src/auth.ts";

function serverMac(secret: string, ...parts: string[]): string {
  return createHmac("sha256", secret).update(parts.join("\0")).digest("hex");
}

test("sign matches the server's HMAC layout", async () => {
  assert.equal(await sign("s", "ring", '{"daemon_id":"agent_1"}'), serverMac("s", "ring", '{"daemon_id":"agent_1"}'));
  assert.ok(await verifyRing("s", "{}", serverMac("s", "ring", "{}")));
  assert.equal(await verifyRing("s", "{}", "00"), false);
  assert.equal(await verifyRing("", "{}", serverMac("", "ring", "{}")), false);
});

test("a token names its daemon until it expires", async () => {
  const exp = String(Math.floor(Date.now() / 1000) + 60);
  const token = `gsdb1.agent_1.${exp}.${serverMac("s", "doorbell-token", "agent_1", exp)}`;
  assert.equal(await verifyToken("s", token), "agent_1");
  assert.equal(await verifyToken("s", token, Date.now() + 120_000), null);
  assert.equal(await verifyToken("other", token), null);
  assert.equal(await verifyToken("s", token.replace("agent_1", "agent_2")), null);
  assert.equal(await verifyToken("s", "gsdb1.agent_1"), null);
  assert.equal(await verifyToken("s", null), null);
});

// The same vector as service/doorbell_test.go.
test("a token the server signs verifies here", async () => {
  const token = "gsdb1.agent_1.2000000000.54f58130863f0a6be9c2a336d3eb4c558e62cba8072aaa80e8c3b8d7f6429e31";
  assert.equal(await verifyToken("s", token, 1_900_000_000_000), "agent_1");
});
