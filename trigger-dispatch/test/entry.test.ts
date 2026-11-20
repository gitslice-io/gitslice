import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { pathToFileURL } from "node:url";

import { entrySource, WHOLE_URL_SECRET_MESSAGE } from "../src/entry.ts";

// loadEntry writes the generated entry module next to a script, with the
// Workers runtime import replaced by a stub, and returns its entrypoint.
async function loadEntry(script: string) {
  const dir = mkdtempSync(join(tmpdir(), "gs-entry-"));
  writeFileSync(join(dir, "trigger.mjs"), script);
  const src = entrySource("trigger.mjs").replace(
    'import { WorkerEntrypoint } from "cloudflare:workers";',
    "class WorkerEntrypoint {}",
  );
  writeFileSync(join(dir, "entry.mjs"), src);
  const mod = await import(pathToFileURL(join(dir, "entry.mjs")).href);
  return new mod.default();
}

function fakeGs() {
  const calls: { method: string; args: unknown[] }[] = [];
  const record = (method: string) => (...args: unknown[]) => {
    calls.push({ method, args });
  };
  return {
    calls,
    gs: {
      log: async (lines: unknown) => record("log")(lines),
      finish: record("finish"),
      fail: record("fail"),
      waitForCallback: record("waitForCallback"),
      setDetailsUrl: async (url: string) => record("setDetailsUrl")(url),
    },
  };
}

test("gs.fail and gs.setDetailsUrl reach the dispatcher's gs", async () => {
  const entry = await loadEntry(`export default {
    async onEvent(event, gs) {
      await gs.setDetailsUrl("https://ci.example.com/builds/7");
      gs.fail("tests failed: 3 of 120");
    },
  };`);
  const { gs, calls } = fakeGs();
  await entry.run("event", "{}", gs, { runId: "trun_1", secrets: {}, callbackUrl: "" });
  assert.deepEqual(
    calls.filter((c) => c.method !== "log"),
    [
      { method: "setDetailsUrl", args: ["https://ci.example.com/builds/7"] },
      { method: "fail", args: ["tests failed: 3 of 120"] },
    ],
  );
});

test("a secret placeholder as the whole fetch URL fails with a plain explanation", async () => {
  const entry = await loadEntry(`export default {
    async onEvent(event, gs) {
      await fetch(gs.secret("HOOK_URL"), { method: "POST" });
    },
  };`);
  const { gs } = fakeGs();
  const run = { runId: "trun_1", secrets: { HOOK_URL: "gs-secret:v1:abc" }, callbackUrl: "" };
  await assert.rejects(entry.run("event", "{}", gs, run), (err: Error) => {
    assert.equal(err.message, WHOLE_URL_SECRET_MESSAGE);
    assert.doesNotMatch(err.message, /gs-secret:v1:abc/);
    return true;
  });
});
