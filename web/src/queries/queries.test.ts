import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";

import type { ApiClient } from "../api/useApi";
import { accountProfileQuery, accountSlicesQuery } from "./accounts";
import {
  agentDaemonsKey,
  agentDaemonsQuery,
  sliceConversationsKeyPrefix,
  sliceConversationsQuery,
  sliceDaemonsQuery
} from "./agents";
import { authStatusQuery, sharedSlicesQuery } from "./auth";
import { changesetDiffQuery, changesetQuery } from "./changesets";
import { checkRunsKeyPrefix, checkRunsQuery, triggerRunsQuery } from "./checks";
import { globalRefQuery, sliceRefQuery, sliceRenderedFileQueryKey } from "./slices";

// The query modules own each key and the shape cached under it; these pin the
// keys that other code invalidates by prefix, and what the fetchers unwrap.

function run<T>(options: { queryFn?: unknown }, context: Record<string, unknown> = {}) {
  return (options.queryFn as (context: Record<string, unknown>) => Promise<T>)(context);
}

describe("query options", () => {
  it("unwraps list responses, so the cache holds the arrays", async () => {
    const api = {
      listDaemons: vi.fn().mockResolvedValue({ daemons: [{ id: "d1" }] }),
      listConversations: vi.fn().mockResolvedValue({ conversations: [{ id: "c1" }] }),
      listCheckRuns: vi.fn().mockResolvedValue({ runs: [{ id: "r1" }] }),
      listTriggerRuns: vi.fn().mockResolvedValue({}),
      listSharedSlices: vi.fn().mockResolvedValue({ slices: [{ role: "writer" }] })
    } as unknown as ApiClient;
    await expect(run(agentDaemonsQuery(api))).resolves.toEqual([{ id: "d1" }]);
    await expect(run(sliceConversationsQuery(api, { account: "a", slice: "s" }))).resolves.toEqual([{ id: "c1" }]);
    await expect(run(checkRunsQuery(api, "cs_1", "ps_1"))).resolves.toEqual([{ id: "r1" }]);
    await expect(run(triggerRunsQuery(api, "cs_1"))).resolves.toEqual([]);
    expect(api.listTriggerRuns).toHaveBeenCalledWith({ changesetId: "cs_1", limit: 50 });
    await expect(run(sharedSlicesQuery(api))).resolves.toEqual([{ role: "writer" }]);
    await expect(run(sharedSlicesQuery({}))).resolves.toEqual([]);
  });

  it("keeps keys the callers invalidate by prefix", () => {
    const slice = { account: "acme", slice: "shop" };
    expect(sliceConversationsQuery({} as ApiClient, slice).queryKey.slice(0, 1)).toEqual(sliceConversationsKeyPrefix);
    expect(sliceDaemonsQuery({} as ApiClient, slice).queryKey.slice(0, 1)).toEqual(agentDaemonsKey);
    expect(checkRunsQuery({} as ApiClient, "cs_1", "ps_2").queryKey).toEqual([...checkRunsKeyPrefix("cs_1"), "ps_2"]);
    expect(authStatusQuery({} as ApiClient).queryKey).toEqual(["authStatus"]);
    expect(accountProfileQuery({} as ApiClient, "acme").queryKey).toEqual(["accountProfile", "acme"]);
    expect(accountSlicesQuery({} as ApiClient, "acme", "owner").queryKey).toEqual(["accountSlices", "acme", "owner"]);
    expect(sliceRenderedFileQueryKey(slice, "c1", "/acme/shop/README.md")).toEqual([
      "sliceRenderedFile",
      "acme",
      "shop",
      "c1",
      "/acme/shop/README.md"
    ]);
  });

  it("asks for the changeset, its diff and the slice as the loaders and pages do", async () => {
    const api = {
      getChangeset: vi.fn().mockResolvedValue({ id: "cs_1" }),
      diffChangeset: vi.fn().mockResolvedValue({ diff: "" }),
      resolveSlice: vi.fn().mockResolvedValue({ id: "slice_1" })
    } as unknown as ApiClient;
    await run(changesetQuery(api, "cs_1"));
    expect(api.getChangeset).toHaveBeenCalledWith({ changesetId: "cs_1" });
    const diff = changesetDiffQuery(api, "cs_1", "", "ps_2");
    expect(diff.queryKey).toEqual(["changesetDiff", "cs_1", "", "ps_2"]);
    await run(diff);
    expect(api.diffChangeset).toHaveBeenCalledWith({ changesetId: "cs_1", fromPatchset: undefined, toPatchset: "ps_2" });
    await run(sliceRefQuery(api, "acme", "shop"));
    expect(api.resolveSlice).toHaveBeenCalledWith({ ref: { account: "acme", slice: "shop" } });
  });

  it("refuses a global ref without a commit, and a cache write goes through the typed key", async () => {
    const api = { getRef: vi.fn().mockResolvedValue({ name: "refs/global/main" }) } as unknown as ApiClient;
    await expect(run(globalRefQuery(api))).rejects.toThrow(/commit id/);

    const client = new QueryClient();
    const options = sliceConversationsQuery({} as ApiClient, { account: "acme", slice: "shop" });
    client.setQueryData(options.queryKey, [{ id: "c1" }]);
    expect(client.getQueryData(options.queryKey)).toEqual([{ id: "c1" }]);
  });
});
