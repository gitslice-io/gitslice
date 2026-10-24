import { describe, expect, it, vi } from "vitest";

import {
  changesetsListKeyPrefix,
  changesetsListQuery,
  changesetsListRequest,
  parseSearch,
  parseStatusTab,
  statusTabs
} from "./filters";

const slice = { account: "gitslice", slice: "gitslice" };

describe("changesets list filters", () => {
  it("opens on the open changesets unless the address says otherwise", () => {
    expect(parseStatusTab(undefined)).toBe("open");
    expect(parseStatusTab("nonsense")).toBe("open");
    expect(parseStatusTab("landed")).toBe("landed");
    expect(parseStatusTab("all")).toBe("all");
  });

  it("splits typed text into text and an author", () => {
    expect(parseSearch("")).toEqual({ query: "", author: "" });
    expect(parseSearch("  web: diff  ")).toEqual({ query: "web: diff", author: "" });
    expect(parseSearch("author:Sunny")).toEqual({ query: "", author: "sunny" });
    expect(parseSearch("blame author:@sunny  pictures")).toEqual({ query: "blame pictures", author: "sunny" });
    // The last author wins; a bare "author:" is no author.
    expect(parseSearch("author:a author:b")).toEqual({ query: "", author: "b" });
    expect(parseSearch("author: fix")).toEqual({ query: "fix", author: "" });
  });

  it("asks for each tab's statuses, with only the filters that are set", () => {
    expect(changesetsListRequest(slice, "open", "", "")).toEqual({
      authoringSlice: slice,
      summary: true,
      limit: 50,
      statuses: ["draft", "open", "published", "pending_publish"]
    });
    expect(changesetsListRequest(slice, "all", "", "")).toEqual({ authoringSlice: slice, summary: true, limit: 50 });
    expect(changesetsListRequest(slice, "landed", "blame author:sunny", "tok")).toEqual({
      authoringSlice: slice,
      summary: true,
      limit: 50,
      statuses: ["submitted", "merged"],
      query: "blame",
      author: "sunny",
      pageToken: "tok"
    });
  });

  it("covers every status a changeset can have in exactly one of open, landed and abandoned", () => {
    const grouped = statusTabs.filter((tab) => tab.value !== "all").flatMap((tab) => tab.statuses);
    expect(new Set(grouped).size).toBe(grouped.length);
    for (const status of ["draft", "pending_publish", "submitted", "abandoned"]) {
      expect(grouped).toContain(status);
    }
  });

  it("keys the query by slice, tab and text, and continues with the next page's token", async () => {
    expect(changesetsListKeyPrefix(slice)).toEqual(["changesets", "gitslice", "gitslice"]);
    const listChangesets = vi.fn().mockResolvedValue({ changesets: [], nextPageToken: "next" });
    const query = changesetsListQuery({ listChangesets } as never, slice, "landed", " fix ");
    expect(query.queryKey).toEqual(["changesets", "gitslice", "gitslice", "landed", "fix"]);
    const page = await query.queryFn({ pageParam: "" });
    expect(listChangesets).toHaveBeenCalledWith(expect.not.objectContaining({ pageToken: expect.anything() }));
    expect(query.getNextPageParam(page)).toBe("next");
    expect(query.getNextPageParam({ changesets: [] })).toBeUndefined();
    await query.queryFn({ pageParam: "next" });
    expect(listChangesets).toHaveBeenLastCalledWith(expect.objectContaining({ pageToken: "next" }));
  });
});
