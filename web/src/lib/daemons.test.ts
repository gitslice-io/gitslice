import { describe, expect, it } from "vitest";

import { canHostConversations } from "./daemons";

describe("canHostConversations", () => {
  it("excludes CI-only daemons while preserving the ownership rule", () => {
    expect(canHostConversations({ mine: true, runtime: "ci" })).toBe(false);
    expect(canHostConversations({ mine: true, runtime: "codex" })).toBe(true);
    expect(canHostConversations({ mine: false, runtime: "codex" })).toBe(false);
  });
});
