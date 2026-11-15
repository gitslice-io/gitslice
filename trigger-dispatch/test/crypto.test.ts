import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import { test } from "node:test";

import { loaderIsolateKey, triggerScope } from "../src/api.ts";
import { openPlaceholders, PlaceholderError, seal, sealKey, sign, verifyDispatch } from "../src/crypto.ts";
import {
  appendLogLine,
  entrySource,
  MAX_RUN_LOG_LINE_CHARS,
  MAX_RUN_LOG_LINES,
  type LogBuffer,
} from "../src/entry.ts";
import { GatePolicyError, hostAllowed, prepareGateRequest, textual } from "../src/gate.ts";

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

test("sealKey caches derived CryptoKey per secret", async () => {
  const k1 = await sealKey("cached-secret");
  const k2 = await sealKey("cached-secret");
  const k3 = await sealKey("different-secret");
  assert.equal(k1, k2);
  assert.notEqual(k1, k3);
});

test("placeholders open only for their tenant/folder/script scope and before expiry", async () => {
  const key = await sealKey("secret");
  const scopeA = triggerScope({ codeHash: "abc", run: { account: "acme", folder: "/billing" } });
  const scopeB = triggerScope({ codeHash: "abc", run: { account: "evil", folder: "/billing" } });
  const scopeFolderB = triggerScope({ codeHash: "abc", run: { account: "acme", folder: "/other" } });
  assert.notEqual(scopeA, scopeB);
  assert.notEqual(scopeA, scopeFolderB);
  assert.notEqual(await loaderIsolateKey(scopeA, ["api.github.com"]), await loaderIsolateKey(scopeB, ["api.github.com"]));
  assert.notEqual(await loaderIsolateKey(scopeA, ["api.github.com"]), await loaderIsolateKey(scopeA, ["evil.example.com"]));

  const p = await seal(key, { v: "tok3n", code: scopeA, exp: Date.now() + 60_000 });
  assert.match(p, /^gs-secret:v1:[A-Za-z0-9_-]+$/);
  assert.equal(await openPlaceholders(key, `Bearer ${p} and ${p}`, scopeA), "Bearer tok3n and tok3n");
  await assert.rejects(openPlaceholders(key, p, scopeB), PlaceholderError);
  await assert.rejects(openPlaceholders(key, p, scopeFolderB), PlaceholderError);
  await assert.rejects(openPlaceholders(key, p, scopeA, Date.now() + 120_000), PlaceholderError);
  const otherKey = await sealKey("other secret");
  await assert.rejects(openPlaceholders(otherKey, p, scopeA), PlaceholderError);
  assert.equal(await openPlaceholders(key, "no secrets here", scopeA), "no secrets here");
});

test("hosts block loopback, internal suffixes, single-label names, and IP forms", () => {
  const network = ["api.github.com", "*.example.com", "localhost", "*.localhost", "*.internal", "127.1", "2130706433"];
  assert.ok(hostAllowed("api.github.com", network));
  assert.ok(hostAllowed("API.GitHub.com.", network));
  assert.ok(hostAllowed("a.b.example.com", network));
  assert.ok(!hostAllowed("example.com", network));
  assert.ok(!hostAllowed("evil-example.com", network));
  assert.ok(!hostAllowed("github.com", network));
  assert.ok(!hostAllowed("10.0.0.1", ["10.0.0.1"]));
  assert.ok(!hostAllowed("127.1", network));
  assert.ok(!hostAllowed("2130706433", network));
  assert.ok(!hostAllowed("0x7f000001", ["0x7f000001"]));
  assert.ok(!hostAllowed("[::1]", ["[::1]"]));
  assert.ok(!hostAllowed("localhost", network));
  assert.ok(!hostAllowed("sub.localhost", network));
  assert.ok(!hostAllowed("metadata.google.internal", network));
  assert.ok(!hostAllowed("printer.local", ["*.local"]));
  assert.ok(!hostAllowed("singlelabel", ["singlelabel"]));
  assert.ok(!hostAllowed("anything", []));
  assert.ok(textual("application/json; charset=utf-8"));
  assert.ok(!textual("application/octet-stream"));
});

test("prepareGateRequest blocks URL credential, origin, and host-header smuggling via placeholders", async () => {
  const key = await sealKey("gate-secret");
  const scope = "acme:/app:hash1";
  const network = ["*.example.com", "api.github.com"];

  // Valid placeholder in query/header/body on allowed host succeeds.
  const validTok = await seal(key, { v: "s3cr3t", code: scope, exp: Date.now() + 60_000 });
  const okReq = new Request(`https://sub.example.com/hook?token=${validTok}`, {
    method: "POST",
    headers: { authorization: `Bearer ${validTok}`, "content-type": "application/json" },
    body: JSON.stringify({ key: validTok }),
  });
  const prepared = await prepareGateRequest(okReq, key, scope, network);
  assert.equal(prepared.target, "https://sub.example.com/hook?token=s3cr3t");
  assert.equal(prepared.headers.get("authorization"), "Bearer s3cr3t");
  assert.equal(prepared.body, '{"key":"s3cr3t"}');

  // Pre-expansion credentials in URL (permitted by workerd Request) are rejected.
  await assert.rejects(
    prepareGateRequest(
      { url: "https://user:pass@api.github.com/repos", method: "GET", headers: new Headers(), body: null, text: async () => "" },
      key,
      scope,
      network,
    ),
    GatePolicyError,
  );

  // Post-expansion credentials injected via a placeholder (`user:pass@sub` inside `https://<placeholder>.example.com`)
  // or authority smuggling (`evil.com#@foo`) are rejected.
  // Note: replace ":" in placeholder not needed when testing workerd URL string where placeholder is in host/userinfo:
  const credSecret = await seal(key, { v: "user:pass@sub", code: scope, exp: Date.now() + 60_000 });
  await assert.rejects(
    prepareGateRequest(
      { url: `https://sub.example.com/path`, method: "GET", headers: new Headers({ host: "evil.example.com" }), body: null, text: async () => "" },
      key,
      scope,
      network,
    ),
    GatePolicyError,
  );
  await assert.rejects(
    prepareGateRequest(
      { url: `https://${credSecret}@api.github.com/repos`, method: "GET", headers: new Headers(), body: null, text: async () => "" },
      key,
      scope,
      network,
    ),
    GatePolicyError,
  );

  // Placeholder in query/fragment or host smuggling authority/origin redirect is rejected!
  const smuggleOrigin = await seal(key, { v: "evil.com#@foo", code: scope, exp: Date.now() + 60_000 });
  await assert.rejects(
    prepareGateRequest(
      { url: `https://${smuggleOrigin}.example.com/exfil`, method: "GET", headers: new Headers(), body: null, text: async () => "" },
      key,
      scope,
      network,
    ),
    GatePolicyError,
  );

  // Placeholder injecting CRLF into header is rejected.
  const crlfSecret = await seal(key, { v: "ok\r\nX-Injected: pwn", code: scope, exp: Date.now() + 60_000 });
  await assert.rejects(
    prepareGateRequest(
      new Request("https://api.github.com/user", { headers: { authorization: `Bearer ${crlfSecret}` } }),
      key,
      scope,
      network,
    ),
    GatePolicyError,
  );
});

test("log buffer caps line length and line count and entrySource catches gs.log errors", () => {
  const buf: LogBuffer = { lines: [], chars: 0, truncated: false };
  const longLine = "x".repeat(MAX_RUN_LOG_LINE_CHARS + 500);
  appendLogLine(buf, "stdout", longLine);
  assert.equal(buf.lines[0].text.length, MAX_RUN_LOG_LINE_CHARS);

  for (let i = 0; i < MAX_RUN_LOG_LINES + 50; i++) {
    appendLogLine(buf, "stdout", `line-${i}`);
  }
  assert.equal(buf.truncated, true);
  assert.equal(buf.lines.length, MAX_RUN_LOG_LINES + 1);
  assert.match(buf.lines[buf.lines.length - 1].text, /truncated/);

  const src = entrySource("trigger.js");
  assert.match(src, /MAX_LINES = 500/);
  assert.match(src, /await gs\.log\(store\.lines\)/);
});
