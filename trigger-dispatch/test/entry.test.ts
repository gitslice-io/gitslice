import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { pathToFileURL } from "node:url";

import { normalizeAnnotations } from "../src/api.ts";
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
      setOutput: async (key: string, value: string) => record("setOutput")(key, value),
      readFile: async (path: string, at: string) => {
        record("readFile")(path, at);
        return `content:${at}:${path}`;
      },
      listDirectory: async (path: string, at: string) => {
        record("listDirectory")(path, at);
        return [{ name: "a.md", path: `${path}/a.md`, kind: "file" }];
      },
      diffChangeset: async (opts: unknown) => {
        record("diffChangeset")(opts);
        return { changesetId: "cs_1", fromPatchsetId: "", toPatchsetId: "ps_1", changedPaths: ["/a/b.go"], diff: "+hi\n" };
      },
    },
  };
}

test("gs.fail, gs.setDetailsUrl, and gs.setOutput reach the dispatcher's gs", async () => {
  const entry = await loadEntry(`export default {
    async onEvent(event, gs) {
      await gs.setDetailsUrl("https://ci.example.com/builds/7");
      await gs.setOutput("image_tag", "v1.2.3");
      gs.fail("tests failed: 3 of 120");
    },
  };`);
  const { gs, calls } = fakeGs();
  await entry.run("event", "{}", gs, { runId: "trun_1", secrets: {}, callbackUrl: "" });
  assert.deepEqual(
    calls.filter((c) => c.method !== "log"),
    [
      { method: "setDetailsUrl", args: ["https://ci.example.com/builds/7"] },
      { method: "setOutput", args: ["image_tag", "v1.2.3"] },
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

test("gs.files.readText({ ref }) and gs.changeset.diff() forward ref/options to the dispatcher", async () => {
  const entry = await loadEntry(`export default {
    async onEvent(event, gs) {
      const text = await gs.files.readText("design/42.md", { ref: "main" });
      await gs.files.list("design", { ref: "patchset" });
      const d = await gs.changeset.diff({ paths: ["service/a.go"], fromPatchset: "1" });
      await gs.setOutput("read", text);
      await gs.setOutput("diff", d.diff.trim());
    },
  };`);
  const { gs, calls } = fakeGs();
  await entry.run("event", "{}", gs, { runId: "trun_2", secrets: {}, callbackUrl: "" });
  assert.deepEqual(
    calls.filter((c) => c.method !== "log"),
    [
      { method: "readFile", args: ["design/42.md", "main"] },
      { method: "listDirectory", args: ["design", "patchset"] },
      { method: "diffChangeset", args: [{ paths: ["service/a.go"], fromPatchset: "1" }] },
      { method: "setOutput", args: ["read", "content:main:design/42.md"] },
      { method: "setOutput", args: ["diff", "+hi"] },
    ],
  );
});

test("normalizeAnnotations maps line shorthand to startLine and endLine", () => {
  assert.deepEqual(
    normalizeAnnotations([
      { path: "a.go", line: 42, level: "failure", message: "boom" },
      { path: "b.go", startLine: 10, endLine: 15, message: "range" },
    ]),
    [
      { path: "a.go", startLine: 42, endLine: 42, level: "failure", message: "boom" },
      { path: "b.go", startLine: 10, endLine: 15, level: undefined, message: "range" },
    ],
  );
});
