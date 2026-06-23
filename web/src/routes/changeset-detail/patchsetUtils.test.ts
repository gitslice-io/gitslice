import { describe, expect, it } from "vitest";

import type { Changeset, Patchset } from "../../api/types";
import {
  clamp,
  findPatchset,
  handleTransform,
  numericPatchsetNumber,
  patchsetConversationRange,
  patchsetDotLabel,
  patchsetKey,
  patchsetOptionLabel,
  shortCommit,
  shortPatchsetId,
  sortedPatchsets,
  timelineIndexForValue,
  timelinePosition
} from "./patchsetUtils";

describe("sortedPatchsets", () => {
  it("sorts patchsets by number then by key", () => {
    const changeset: Changeset = {
      patchsets: [
        { id: "ps_2", number: "2", createdAt: "2026-06-18T00:02:00Z" },
        { id: "ps_1", number: "1", createdAt: "2026-06-18T00:01:00Z" },
        { id: "ps_3", number: "3", createdAt: "2026-06-18T00:03:00Z" }
      ]
    };

    const result = sortedPatchsets(changeset);
    expect(result).toHaveLength(3);
    expect(result[0].id).toBe("ps_1");
    expect(result[1].id).toBe("ps_2");
    expect(result[2].id).toBe("ps_3");
  });

  it("sorts by key when no numbers", () => {
    const changeset: Changeset = {
      patchsets: [
        { id: "ps_b", createdAt: "2026-06-18T00:02:00Z" },
        { id: "ps_a", createdAt: "2026-06-18T00:01:00Z" }
      ]
    };

    const result = sortedPatchsets(changeset);
    expect(result).toHaveLength(2);
    expect(result[0].id).toBe("ps_a");
    expect(result[1].id).toBe("ps_b");
  });

  it("handles undefined changeset", () => {
    const result = sortedPatchsets(undefined);
    expect(result).toEqual([]);
  });

  it("handles empty patchsets", () => {
    const changeset: Changeset = {};
    const result = sortedPatchsets(changeset);
    expect(result).toEqual([]);
  });
});

describe("numericPatchsetNumber", () => {
  it("returns numeric value for valid numbers", () => {
    const patchset: Patchset = { number: "5" };
    expect(numericPatchsetNumber(patchset)).toBe(5);
  });

  it("returns MAX_SAFE_INTEGER for non-numeric strings", () => {
    const patchset: Patchset = { number: "abc" };
    expect(numericPatchsetNumber(patchset)).toBe(Number.MAX_SAFE_INTEGER);
  });

  it("returns MAX_SAFE_INTEGER for undefined number", () => {
    const patchset: Patchset = {};
    expect(numericPatchsetNumber(patchset)).toBe(Number.MAX_SAFE_INTEGER);
  });
});

describe("patchsetKey", () => {
  it("uses id when available", () => {
    const patchset: Patchset = { id: "ps_123" };
    expect(patchsetKey(patchset)).toBe("ps_123");
  });

  it("constructs key from number, createdAt, and base info when no id", () => {
    const patchset: Patchset = {
      number: "1",
      createdAt: "2026-06-18T00:01:00Z",
      baseCommitId: "commit_abc"
    };
    expect(patchsetKey(patchset)).toBe("1-2026-06-18T00:01:00Z-commit_abc");
  });

  it("handles missing fields gracefully", () => {
    const patchset: Patchset = {};
    expect(patchsetKey(patchset)).toBe("unknown--");
  });
});

describe("findPatchset", () => {
  it("finds patchset by id", () => {
    const patchsets: Patchset[] = [
      { id: "ps_1", number: "1" },
      { id: "ps_2", number: "2" }
    ];
    const result = findPatchset(patchsets, "ps_2");
    expect(result?.id).toBe("ps_2");
  });

  it("returns undefined when not found", () => {
    const patchsets: Patchset[] = [{ id: "ps_1", number: "1" }];
    const result = findPatchset(patchsets, "ps_999");
    expect(result).toBeUndefined();
  });
});

describe("patchsetOptionLabel", () => {
  it("returns label with number when available", () => {
    const patchset: Patchset = { number: "5" };
    expect(patchsetOptionLabel(patchset)).toBe("Patchset 5");
  });

  it("returns label with short id when number not available", () => {
    const patchset: Patchset = { id: "ps_abcdefghij123456789012" };
    expect(patchsetOptionLabel(patchset)).toBe("Patchset abcdefghij12");
  });

  it("returns generic label for undefined patchset", () => {
    expect(patchsetOptionLabel(undefined)).toBe("");
  });

  it("returns generic label when neither number nor id", () => {
    const patchset: Patchset = {};
    expect(patchsetOptionLabel(patchset)).toBe("Patchset");
  });
});

describe("patchsetDotLabel", () => {
  it("returns compact label with number", () => {
    const patchset: Patchset = { number: "3" };
    expect(patchsetDotLabel(patchset)).toBe("P3");
  });

  it("returns compact label with short id", () => {
    const patchset: Patchset = { id: "ps_abcdefghij123456789012" };
    expect(patchsetDotLabel(patchset)).toBe("Pabcdefghij12");
  });

  it("returns just P when neither number nor id", () => {
    const patchset: Patchset = {};
    expect(patchsetDotLabel(patchset)).toBe("P");
  });
});

describe("shortCommit", () => {
  it("returns shortened commit hash", () => {
    expect(shortCommit("abcdefghij1234567890")).toBe("abcdefghij12");
  });

  it("handles short input", () => {
    expect(shortCommit("abc")).toBe("abc");
  });
});

describe("shortPatchsetId", () => {
  it("removes ps_ prefix and truncates", () => {
    expect(shortPatchsetId("ps_abcdefghij123456789012")).toBe("abcdefghij12");
  });

  it("handles id without prefix", () => {
    expect(shortPatchsetId("abcdefghij123456789012")).toBe("abcdefghij12");
  });

  it("returns empty string for empty input", () => {
    expect(shortPatchsetId("")).toBe("");
  });
});

describe("patchsetConversationRange", () => {
  it("returns null when no patchset found", () => {
    const patchsets: Patchset[] = [{ id: "ps_1", number: "1" }];
    const result = patchsetConversationRange(patchsets, "ps_999");
    expect(result).toBeNull();
  });

  it("returns null when selected patchset has no conversation id", () => {
    const patchsets: Patchset[] = [{ id: "ps_1", number: "1" }];
    const result = patchsetConversationRange(patchsets, "ps_1");
    expect(result).toBeNull();
  });

  it("calculates conversation range correctly", () => {
    const patchsets: Patchset[] = [
      {
        id: "ps_1",
        number: "1",
        authoringConversationId: "conv_1",
        authoringConversationSeq: "10"
      },
      {
        id: "ps_2",
        number: "2",
        authoringConversationId: "conv_1",
        authoringConversationSeq: "20"
      }
    ];
    const result = patchsetConversationRange(patchsets, "ps_2");
    expect(result).toEqual({
      conversationId: "conv_1",
      afterSeq: 10,
      beforeSeq: 20
    });
  });

  it("trims to the selected from patchset", () => {
    const patchsets: Patchset[] = [
      {
        id: "ps_1",
        number: "1",
        authoringConversationId: "conv_1",
        authoringConversationSeq: "10"
      },
      {
        id: "ps_2",
        number: "2",
        authoringConversationId: "conv_1",
        authoringConversationSeq: "20"
      },
      {
        id: "ps_3",
        number: "3",
        authoringConversationId: "conv_1",
        authoringConversationSeq: "30"
      }
    ];
    const result = patchsetConversationRange(patchsets, "ps_3", "ps_1");
    expect(result).toEqual({
      conversationId: "conv_1",
      afterSeq: 10,
      beforeSeq: 30
    });
  });

  it("includes the whole conversation when from is the recorded base", () => {
    const patchsets: Patchset[] = [
      {
        id: "ps_1",
        number: "1",
        authoringConversationId: "conv_1",
        authoringConversationSeq: "10"
      },
      {
        id: "ps_2",
        number: "2",
        authoringConversationId: "conv_1",
        authoringConversationSeq: "20"
      }
    ];
    const result = patchsetConversationRange(patchsets, "ps_2", "");
    expect(result).toEqual({
      conversationId: "conv_1",
      afterSeq: 0,
      beforeSeq: 20
    });
  });

  it("ignores a from patchset from a different conversation", () => {
    const patchsets: Patchset[] = [
      {
        id: "ps_1",
        number: "1",
        authoringConversationId: "conv_0",
        authoringConversationSeq: "5"
      },
      {
        id: "ps_2",
        number: "2",
        authoringConversationId: "conv_1",
        authoringConversationSeq: "20"
      }
    ];
    const result = patchsetConversationRange(patchsets, "ps_2", "ps_1");
    expect(result).toEqual({
      conversationId: "conv_1",
      afterSeq: 0,
      beforeSeq: 20
    });
  });
});

describe("timelineIndexForValue", () => {
  const steps = [
    { id: "", label: "Base" },
    { id: "ps_1", label: "P1" },
    { id: "ps_2", label: "P2" }
  ];

  it("returns index for matching value", () => {
    expect(timelineIndexForValue(steps, "ps_1", 0)).toBe(1);
  });

  it("returns 0 for empty value", () => {
    expect(timelineIndexForValue(steps, "", 999)).toBe(0);
  });

  it("returns fallback when value not found", () => {
    expect(timelineIndexForValue(steps, "ps_999", 2)).toBe(2);
  });
});

describe("timelinePosition", () => {
  it("calculates percentage position", () => {
    expect(timelinePosition(1, 4)).toBe("25%");
    expect(timelinePosition(2, 4)).toBe("50%");
    expect(timelinePosition(3, 4)).toBe("75%");
  });

  it("returns 0% for zero maxIndex", () => {
    expect(timelinePosition(5, 0)).toBe("0%");
  });
});

describe("handleTransform", () => {
  it("returns no transform for index 0", () => {
    expect(handleTransform(0, 5)).toBe("translateX(0)");
  });

  it("returns center transform for middle index", () => {
    expect(handleTransform(2, 5)).toBe("translateX(-50%)");
  });

  it("returns right align for max index", () => {
    expect(handleTransform(5, 5)).toBe("translateX(-100%)");
  });
});

describe("clamp", () => {
  it("clamps value within range", () => {
    expect(clamp(5, 0, 10)).toBe(5);
    expect(clamp(-5, 0, 10)).toBe(0);
    expect(clamp(15, 0, 10)).toBe(10);
  });

  it("handles edge cases", () => {
    expect(clamp(0, 0, 0)).toBe(0);
    expect(clamp(5, 5, 5)).toBe(5);
  });
});