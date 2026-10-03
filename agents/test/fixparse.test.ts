import { test } from "node:test";
import assert from "node:assert/strict";
import { parseFix } from "../src/fixparse.ts";

const files = [{ path: "demo/store/src/i18n/messages.gd.json", before: null, after: "{ broken" }];
const reply = (body: string) => `SUMMARY: removed the trailing comma\n=== FILE: ${body} ===\n`;

test("accepts a repaired JSON file and adds the final newline", () => {
  const out = parseFix(`${reply("demo/store/src/i18n/messages.gd.json")}{\n  "a": "b"\n}\n=== END ===`, files);
  assert.ok(typeof out !== "string");
  assert.equal(out.summary, "removed the trailing comma");
  assert.equal(out.files[0].content, '{\n  "a": "b"\n}\n');
});

test("rejects JSON that still does not parse", () => {
  const out = parseFix(`${reply("demo/store/src/i18n/messages.gd.json")}{ "a": "b", }\n=== END ===`, files);
  assert.match(String(out), /still not valid JSON/);
});

test("rejects files the author did not change", () => {
  const out = parseFix(`${reply("demo/store/src/cart/totals.ts")}export {}\n=== END ===`, files);
  assert.match(String(out), /not one of the changed files/);
});

test("rejects replies without a summary or a file", () => {
  assert.match(String(parseFix("=== FILE: x ===\n{}\n=== END ===", files)), /SUMMARY/);
  assert.match(String(parseFix("SUMMARY: nothing", files)), /FILE block/);
});

test("accepts a leading slash on the path", () => {
  const out = parseFix(`${reply("/demo/store/src/i18n/messages.gd.json")}{}\n=== END ===`, files);
  assert.ok(typeof out !== "string");
});
