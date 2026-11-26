import { describe, expect, it } from "vitest";

import {
  validateAccountSearch,
  validateChangesetDetailSearch,
  validateChangesetsSearch,
  validateSliceCreateSearch,
  validateSliceDetailSearch
} from "./searchSchemas";

describe("validateSliceDetailSearch", () => {
  it("keeps a normalized path and the on/off views, however the address spelled them", () => {
    expect(validateSliceDetailSearch({ path: "/nic/app/", history: 1, policy: "1", triggers: "1", blame: true, blameAt: "abc123" })).toEqual({
      path: "/nic/app",
      history: true,
      policy: true,
      triggers: true,
      blame: true,
      blameAt: "abc123"
    });
    expect(validateSliceDetailSearch({ history: "true", policy: "true" })).toEqual({ history: true, policy: true });
    expect(validateSliceDetailSearch({ tags: "" })).toEqual({});
    expect(validateSliceDetailSearch({ tags: "1" })).toEqual({ tags: true });
    expect(validateSliceDetailSearch({ at: "sha256:abc" })).toEqual({ at: "sha256:abc" });
    expect(validateSliceDetailSearch({ at: " " })).toEqual({});
  });

  it("drops what is blank, off or of the wrong type", () => {
    expect(validateSliceDetailSearch({ path: "  ", history: 0, policy: "false", triggers: "false", blame: "", blameAt: 7 })).toEqual({});
    expect(validateSliceDetailSearch({ path: 42, unknown: "x" })).toEqual({});
  });
});

describe("validateChangesetDetailSearch", () => {
  it("keeps the compare handles, file, view and the known modes", () => {
    expect(
      validateChangesetDetailSearch({
        from: "ps_1",
        to: "ps_2",
        file: "/a/b.go",
        conversation: "1",
        view: "/a/b.go",
        side: "original",
        diff: "split"
      })
    ).toEqual({ from: "ps_1", to: "ps_2", file: "/a/b.go", conversation: true, view: "/a/b.go", side: "original", diff: "split" });
  });

  it("ignores unknown sides and diff modes", () => {
    expect(validateChangesetDetailSearch({ side: "both", diff: "unified", from: "", to: 3 })).toEqual({});
  });
});

describe("validateChangesetsSearch", () => {
  it("keeps the slice, a known status and the text as typed", () => {
    expect(validateChangesetsSearch({ slice: " acme:payment ", status: "landed", q: "fix bug " })).toEqual({
      slice: "acme:payment",
      status: "landed",
      q: "fix bug "
    });
  });

  it("reads a numeric search as text and drops unknown statuses and blanks", () => {
    expect(validateChangesetsSearch({ q: 2024 })).toEqual({ q: "2024" });
    expect(validateChangesetsSearch({ status: "bogus", q: "  ", slice: 5 })).toEqual({ status: "open" });
    expect(validateChangesetsSearch({})).toEqual({});
  });
});

describe("validateSliceCreateSearch", () => {
  it("keeps the account asked for", () => {
    expect(validateSliceCreateSearch({ account: " acme " })).toEqual({ account: "acme" });
    expect(validateSliceCreateSearch({ account: 1 })).toEqual({});
  });
});

describe("validateAccountSearch", () => {
  it("keeps known non-default tabs and drops overview or unknown values", () => {
    expect(validateAccountSearch({ tab: "agents" })).toEqual({ tab: "agents" });
    expect(validateAccountSearch({ tab: "runners" })).toEqual({ tab: "runners" });
    expect(validateAccountSearch({ tab: "people" })).toEqual({ tab: "people" });
    expect(validateAccountSearch({ tab: "overview" })).toEqual({});
    expect(validateAccountSearch({ tab: "unknown" })).toEqual({});
    expect(validateAccountSearch({ tab: 1 })).toEqual({});
  });
});
