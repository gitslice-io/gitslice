import { describe, expect, it } from "vitest";

import { appendCheckLogChunk, createEmptyCheckLogBuffer } from "./CheckRunLogTail";

describe("CheckRunLogTail log buffer", () => {
  it("deduplicates log chunks by seq and trims to maxChars", () => {
    let buf = createEmptyCheckLogBuffer();
    buf = appendCheckLogChunk(buf, { seq: "1", chunk: "line1\n" }, 20);
    buf = appendCheckLogChunk(buf, { seq: "1", chunk: "duplicate\n" }, 20);
    buf = appendCheckLogChunk(buf, { seq: "2", chunk: "line2\n" }, 20);
    expect(buf.text).toBe("line1\nline2\n");
    expect(buf.chunkCount).toBe(2);
    expect(buf.lastSeq).toBe(2);

    buf = appendCheckLogChunk(buf, { seq: "3", chunk: "0123456789abcdefghij" }, 20);
    expect(buf.text).toBe("0123456789abcdefghij");
    expect(buf.chunkCount).toBe(3);
    expect(buf.lastSeq).toBe(3);
  });
});
