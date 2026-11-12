import { describe, expect, it } from "vitest";

import { llmsAliasResponse } from "./llmsAlias";

describe("llmsAliasResponse", () => {
  it("redirects llama.txt and friends to llms.txt", () => {
    for (const path of ["/llama.txt", "/LLAMA.txt", "/llama.md", "/llm.txt", "/llms.md"]) {
      const res = llmsAliasResponse(new Request(`https://gitslice.io${path}`));
      expect(res?.status).toBe(301);
      expect(res?.headers.get("location")).toBe("https://gitslice.io/llms.txt");
    }
  });

  it("leaves other paths and methods alone", () => {
    expect(llmsAliasResponse(new Request("https://gitslice.io/llms.txt"))).toBeUndefined();
    expect(llmsAliasResponse(new Request("https://gitslice.io/docs"))).toBeUndefined();
    expect(llmsAliasResponse(new Request("https://gitslice.io/llama.txt", { method: "POST" }))).toBeUndefined();
  });
});
