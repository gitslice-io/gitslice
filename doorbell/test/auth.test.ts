import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { createHmac } from "node:crypto";
import { mkdtempSync, readFileSync, rmSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import { MAX_RING_SKEW_S, sign, validRingBody, verifyRing, verifyToken } from "../src/auth.ts";

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

test("validRingBody enforces 60s skew and required fields", () => {
  assert.equal(MAX_RING_SKEW_S, 60);
  const nowMs = 1_700_000_000_000;
  const nowS = nowMs / 1000;
  assert.deepEqual(validRingBody(JSON.stringify({ daemon_id: "agent_1", ts: nowS - 59, queued: 3 }), nowMs), {
    daemonId: "agent_1",
    queued: 3,
  });
  assert.equal(validRingBody(JSON.stringify({ daemon_id: "agent_1", ts: nowS - 61, queued: 3 }), nowMs), null);
  assert.equal(validRingBody(JSON.stringify({ daemon_id: "agent_1", ts: nowS + 61, queued: 3 }), nowMs), null);
  assert.equal(validRingBody(JSON.stringify({ daemon_id: "", ts: nowS }), nowMs), null);
  assert.equal(validRingBody("not-json", nowMs), null);
});

test("runner start.sh serializes config.json safely without JSON injection and sets 0600 permissions", () => {
  const dir = mkdtempSync(join(tmpdir(), "doorbell-cfg-"));
  try {
    const startSh = fileURLToPath(new URL("../runner/start.sh", import.meta.url));
    const startContents = readFileSync(startSh, "utf8");
    assert.match(startContents, /export GITSLICE_CHECKS_REQUIRE_CONTAINER=1/);
    assert.match(startContents, /exec su-exec runner gs agent start/);

    const cfgPath = join(dir, ".gitslice", "config.json");
    const addr = 'api.gitslice.io:443","injected":"evil';
    const tok = 'tok"\\\n\t$(echo pwned)`id`';
    const sub = 'subj_"quote"\\slash';

    // Test both default (jq if present) and minimal PATH (awk fallback).
    for (const pathEnv of [process.env.PATH ?? "/usr/bin:/bin", "/usr/bin:/bin"]) {
      execFileSync("/bin/sh", [startSh, "--write-config-only", cfgPath], {
        env: {
          PATH: pathEnv,
          GITSLICE_SERVER_ADDR: addr,
          GITSLICE_RUNNER_TOKEN: tok,
          GITSLICE_RUNNER_SUBJECT: sub,
        },
      });
      const parsed = JSON.parse(readFileSync(cfgPath, "utf8")) as Record<string, unknown>;
      assert.deepEqual(parsed, {
        server_addr: addr,
        token: tok,
        subject_id: sub,
      });
      assert.equal(statSync(cfgPath).mode & 0o777, 0o600);
    }
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
