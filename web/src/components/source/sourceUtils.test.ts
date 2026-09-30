import { afterEach, describe, expect, it, vi } from "vitest";

import { decodeBase64File } from "./sourceUtils";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("decodeBase64File", () => {
  it("decodes ASCII text", () => {
    expect(decodeBase64File("SGVsbG8sIHdvcmxkIQ==")).toBe("Hello, world!");
  });

  it("decodes multi-byte UTF-8 text", () => {
    expect(decodeBase64File("44GT44KT44Gr44Gh44GvIPCfjI0=")).toBe("こんにちは 🌍");
  });

  it("returns an empty string for empty input", () => {
    expect(decodeBase64File("")).toBe("");
    expect(decodeBase64File(undefined)).toBe("");
  });

  it("returns the raw value for invalid base64", () => {
    expect(decodeBase64File("not valid base64!!!")).toBe("not valid base64!!!");
  });

  it("uses globalThis.atob when window is unavailable", () => {
    vi.stubGlobal("window", undefined);

    expect(decodeBase64File("V29ya2VyIHNhZmU=")).toBe("Worker safe");
  });
});
