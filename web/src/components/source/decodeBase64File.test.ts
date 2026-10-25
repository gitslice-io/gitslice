import { describe, expect, it } from "vitest";

import { base64ToBytes } from "../../lib/bytes";
import { decodeBase64File } from "./sourceUtils";

// This file runs without a window, as the server render does: a file view is
// rendered on the server from the base64 the API returns, and decoding it
// there must give the file's text, not the base64 itself.
describe("decoding a file's base64 where there is no window", () => {
  it("really has no window", () => {
    expect(typeof window).toBe("undefined");
  });

  it("decodes text, multi-byte characters and the empty file", () => {
    expect(decodeBase64File(btoa("module gitslice.io/gitslice\n\ngo 1.24\n"))).toBe("module gitslice.io/gitslice\n\ngo 1.24\n");
    // "héllo — 世界" as UTF-8 bytes.
    const bytes = new TextEncoder().encode("héllo — 世界");
    expect(decodeBase64File(btoa(String.fromCharCode(...bytes)))).toBe("héllo — 世界");
    expect(decodeBase64File("")).toBe("");
    expect(decodeBase64File(undefined)).toBe("");
  });

  it("gives back what is not base64 as it is", () => {
    expect(decodeBase64File("not base64 !!")).toBe("not base64 !!");
  });

  it("turns base64 into bytes", () => {
    expect([...base64ToBytes(btoa("ABÿ"))]).toEqual([65, 66, 255]);
  });
});
