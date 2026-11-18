import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useState } from "react";

import {
  advanceStreamSeq,
  parseStreamSeq,
  useConnectStream
} from "./useConnectStream";

function StreamHarness({
  streamKey,
  openStream,
  reconnectDelayMs = 20
}: {
  streamKey: string;
  openStream: (afterSeq: number, signal: AbortSignal) => AsyncIterable<{ seq: string; text: string }>;
  reconnectDelayMs?: number;
}) {
  const [items, setItems] = useState<string[]>([]);
  const { streamError, isStreamReconnecting, streamClosed, reconnectNow } =
    useConnectStream({
      streamKey,
      openStream,
      getSeq: (item) => item.seq,
      onItem: (item) => setItems((curr) => [...curr, item.text]),
      reconnectDelayMs
    });

  return (
    <div>
      <div data-testid="items">{items.join(",")}</div>
      <div data-testid="error">{streamError}</div>
      <div data-testid="reconnecting">{String(isStreamReconnecting)}</div>
      <div data-testid="closed">{String(streamClosed)}</div>
      <button onClick={reconnectNow} type="button">
        Reconnect
      </button>
    </div>
  );
}

describe("useConnectStream", () => {
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("parses and advances stream sequence cursors monotonically", () => {
    expect(parseStreamSeq(undefined)).toBeUndefined();
    expect(parseStreamSeq("")).toBeUndefined();
    expect(parseStreamSeq("0")).toBeUndefined();
    expect(parseStreamSeq("12")).toBe(12);

    const ref = { current: 5 };
    advanceStreamSeq("3", ref);
    expect(ref.current).toBe(5);
    advanceStreamSeq("9", ref);
    expect(ref.current).toBe(9);
  });

  it("resumes from the last received seq on automatic reconnect", async () => {
    const seenAfterSeqs: number[] = [];
    let attempt = 0;
    const openStream = vi.fn(async function* (afterSeq: number, signal: AbortSignal) {
      seenAfterSeqs.push(afterSeq);
      attempt += 1;
      if (attempt === 1) {
        yield { seq: "4", text: "first" };
        if (!signal.aborted) {
          throw new Error("network blip");
        }
        return;
      }
      yield { seq: "5", text: "second" };
    });

    render(<StreamHarness openStream={openStream} streamKey="s1" />);

    await waitFor(() =>
      expect(screen.getByTestId("items")).toHaveTextContent("first,second")
    );
    expect(seenAfterSeqs[0]).toBe(0);
    expect(seenAfterSeqs).toContain(4);
    expect(screen.getByTestId("closed")).toHaveTextContent("true");
  });

  it("resets cursor to 0 when streamKey changes", async () => {
    const seenAfterSeqs: number[] = [];
    const openStream = vi.fn(async function* (afterSeq: number) {
      seenAfterSeqs.push(afterSeq);
      yield { seq: "10", text: `at-${afterSeq}` };
    });

    const { rerender } = render(
      <StreamHarness openStream={openStream} streamKey="s1" />
    );
    await waitFor(() =>
      expect(screen.getByTestId("items")).toHaveTextContent("at-0")
    );

    fireEvent.click(screen.getByRole("button", { name: "Reconnect" }));
    await waitFor(() => expect(seenAfterSeqs).toContain(10));

    rerender(<StreamHarness openStream={openStream} streamKey="s2" />);
    await waitFor(() =>
      expect(seenAfterSeqs[seenAfterSeqs.length - 1]).toBe(0)
    );
  });
});
