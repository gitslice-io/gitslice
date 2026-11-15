import assert from "node:assert/strict";
import { test } from "node:test";

import { runnersToStart } from "../src/runners.ts";

test("one runner per queued run, up to the free slots", () => {
  assert.equal(runnersToStart(0, 0, 4), 0);
  assert.equal(runnersToStart(2, 0, 4), 2);
  assert.equal(runnersToStart(6, 0, 4), 4);
  assert.equal(runnersToStart(6, 3, 4), 1);
  assert.equal(runnersToStart(1, 4, 4), 0);
});
