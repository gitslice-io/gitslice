import assert from "node:assert/strict";
import { test } from "node:test";

import {
  evictOldestSockets,
  handleBellFrame,
  MAX_BELL_SOCKETS,
  runnersToStart,
  startRunnerPool,
  type ClosableSocket,
  type RunnerSlot,
} from "../src/runners.ts";

test("one runner per queued run, up to the free slots", () => {
  assert.equal(runnersToStart(0, 0, 4), 0);
  assert.equal(runnersToStart(2, 0, 4), 2);
  assert.equal(runnersToStart(6, 0, 4), 4);
  assert.equal(runnersToStart(6, 3, 4), 1);
  assert.equal(runnersToStart(1, 4, 4), 0);
});

test("startRunnerPool checks all slots in parallel and starts only needed idle slots", async () => {
  let inFlightChecks = 0;
  let maxConcurrentChecks = 0;
  const startedSlots: number[] = [];

  const initialRunning = [false, true, false, true];
  const slots: RunnerSlot[] = initialRunning.map((running, idx) => ({
    async isRunning(): Promise<boolean> {
      inFlightChecks++;
      maxConcurrentChecks = Math.max(maxConcurrentChecks, inFlightChecks);
      await new Promise((r) => setTimeout(r, 10));
      inFlightChecks--;
      return running;
    },
    async begin(): Promise<boolean> {
      startedSlots.push(idx);
      return true;
    },
  }));

  // 2 running (slots 1, 3), 1 queued unclaimed -> start 1 idle slot (slot 0), total 3 active.
  const active = await startRunnerPool(slots, 1);
  assert.equal(maxConcurrentChecks, 4, "expected all slot checks to run concurrently via Promise.all");
  assert.deepEqual(startedSlots, [0]);
  assert.equal(active, 3);
});

test("evictOldestSockets caps open sockets per Bell DO at MAX_BELL_SOCKETS", () => {
  assert.equal(MAX_BELL_SOCKETS, 16);
  const closed: number[] = [];
  const sockets = Array.from({ length: 18 }, (_, i) => ({
    close() {
      closed.push(i);
    },
  }));
  evictOldestSockets(sockets);
  // Accepting 1 new socket requires keeping at most 15 existing -> evicts 0, 1, 2.
  assert.deepEqual(closed, [0, 1, 2]);
});

test("handleBellFrame answers ping and closes sockets sending unexpected frames", () => {
  const events: string[] = [];
  const makeSocket = (): ClosableSocket => ({
    send(data: string) {
      events.push(`send:${data}`);
    },
    close(code?: number, reason?: string) {
      events.push(`close:${code}:${reason}`);
    },
  });

  const ws1 = makeSocket();
  handleBellFrame(ws1, "ping");
  assert.deepEqual(events, ["send:pong"]);

  events.length = 0;
  const ws2 = makeSocket();
  handleBellFrame(ws2, "unexpected-frame");
  assert.deepEqual(events, ["close:1008:unexpected message"]);

  events.length = 0;
  const ws3 = makeSocket();
  handleBellFrame(ws3, new Uint8Array([1, 2, 3]).buffer);
  assert.deepEqual(events, ["close:1008:unexpected message"]);
});
