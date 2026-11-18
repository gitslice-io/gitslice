import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";

import { RpcError } from "../api/rpcError";
import type { ApiClient } from "../api/useApi";
import { accountProfileQuery, accountSlicesQuery } from "./accounts";
import {
  agentDaemonsKey,
  agentDaemonsQuery,
  sliceConversationsKeyPrefix,
  sliceConversationsQuery,
  sliceDaemonsQuery
} from "./agents";
import {
  authStatusQuery,
  ownedAgentsKey,
  ownedAgentsQuery,
  pendingClaimsKey,
  pendingClaimsQuery,
  sharedSlicesQuery,
  usernameAvailableQuery
} from "./auth";
import { changesetDiffQuery, changesetQuery } from "./changesets";
import {
  checkRunsKeyPrefix,
  checkRunsQuery,
  triggerRunLogsQuery,
  triggerRunsQuery
} from "./checks";
import {
  accountSecretsQuery,
  secretsKey,
  sliceSecretsKeyPrefix,
  sliceSecretsQuery
} from "./secrets";
import {
  globalRefQuery,
  sliceBlameQuery,
  sliceGitMirrorKey,
  sliceGitMirrorQuery,
  sliceRefQuery,
  sliceRenderedFileQueryKey
} from "./slices";

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
      listTriggerRunLogs: vi.fn().mockResolvedValue({ logs: [{ seq: "1", chunk: "ok\n" }] }),
      listSharedSlices: vi.fn().mockResolvedValue({ slices: [{ role: "writer" }] }),
      listOwnedAgents: vi.fn().mockResolvedValue({ agents: [{ account: "bot" }] }),
      listSecrets: vi.fn().mockResolvedValue({ secrets: [{ id: "s1", name: "TOKEN" }] }),
      listSliceSecrets: vi.fn().mockResolvedValue({ secrets: [{ name: "TOKEN" }] })
    } as unknown as ApiClient;
    await expect(run(agentDaemonsQuery(api))).resolves.toEqual([{ id: "d1" }]);
    await expect(run(sliceConversationsQuery(api, { account: "a", slice: "s" }))).resolves.toEqual([{ id: "c1" }]);
    await expect(run(checkRunsQuery(api, "cs_1", "ps_1"))).resolves.toEqual([{ id: "r1" }]);
    await expect(run(triggerRunsQuery(api, "cs_1"))).resolves.toEqual([]);
    expect(api.listTriggerRuns).toHaveBeenCalledWith({ changesetId: "cs_1", limit: 50 });
    await expect(run(triggerRunLogsQuery(api, "run_1"))).resolves.toEqual([{ seq: "1", chunk: "ok\n" }]);
    expect(api.listTriggerRunLogs).toHaveBeenCalledWith({ runId: "run_1", afterSeq: "0" });
    await expect(run(sharedSlicesQuery(api))).resolves.toEqual([{ role: "writer" }]);
    await expect(run(sharedSlicesQuery({}))).resolves.toEqual([]);
    await expect(run(ownedAgentsQuery(api))).resolves.toEqual([{ account: "bot" }]);
    await expect(run(accountSecretsQuery(api, "acme"))).resolves.toEqual([{ id: "s1", name: "TOKEN" }]);
    await expect(run(sliceSecretsQuery(api, { account: "acme", slice: "shop" }))).resolves.toEqual([{ name: "TOKEN" }]);
  });

  it("keeps keys the callers invalidate by prefix", () => {
    const slice = { account: "acme", slice: "shop" };
    expect(sliceConversationsQuery({} as ApiClient, slice).queryKey.slice(0, 1)).toEqual(sliceConversationsKeyPrefix);
    expect(sliceDaemonsQuery({} as ApiClient, slice).queryKey.slice(0, 1)).toEqual(agentDaemonsKey);
    expect(checkRunsQuery({} as ApiClient, "cs_1", "ps_2").queryKey).toEqual([...checkRunsKeyPrefix("cs_1"), "ps_2"]);
    expect(triggerRunLogsQuery({} as ApiClient, "run_7").queryKey).toEqual(["triggerRunLogs", "run_7"]);
    expect(authStatusQuery({} as ApiClient).queryKey).toEqual(["authStatus"]);
    expect(pendingClaimsQuery({} as ApiClient).queryKey).toEqual(pendingClaimsKey);
    expect(ownedAgentsQuery({} as ApiClient).queryKey).toEqual(ownedAgentsKey);
    expect(usernameAvailableQuery({} as ApiClient, "mira").queryKey).toEqual(["usernameAvailable", "mira"]);
    expect(accountProfileQuery({} as ApiClient, "acme").queryKey).toEqual(["accountProfile", "acme"]);
    expect(accountSecretsQuery({} as ApiClient, "acme").queryKey).toEqual(secretsKey("acme"));
    expect(sliceSecretsQuery({} as ApiClient, slice).queryKey).toEqual([...sliceSecretsKeyPrefix, "acme", "shop"]);
    expect(sliceGitMirrorQuery({} as ApiClient, slice).queryKey).toEqual(sliceGitMirrorKey(slice));
    expect(sliceBlameQuery({} as ApiClient, slice, "c1", "/acme/shop/main.go").queryKey).toEqual([
      "blame",
      "acme",
      "shop",
      "c1",
      "/acme/shop/main.go"
    ]);
    expect(accountSlicesQuery({} as ApiClient, "acme", "owner").queryKey).toEqual(["accountSlices", "acme", "owner", "name"]);
    expect(accountSlicesQuery({} as ApiClient, "acme", "owner", "active").queryKey).toEqual([
      "accountSlices",
      "acme",
      "owner",
      "active"
    ]);
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
      resolveSlice: vi.fn().mockResolvedValue({ id: "slice_1" }),
      blameFile: vi.fn().mockResolvedValue({ lineCount: 3 }),
      checkUsernameAvailable: vi.fn().mockResolvedValue({ available: true }),
      listPendingClaims: vi.fn().mockResolvedValue({ claims: [] }),
      getSliceGitMirror: vi
        .fn()
        .mockResolvedValueOnce({ url: "https://github.com/acme/shop.git" })
        .mockRejectedValueOnce(new RpcError(404, { code: "not_found", message: "missing" }))
    } as unknown as ApiClient;
    await run(changesetQuery(api, "cs_1"));
    expect(api.getChangeset).toHaveBeenCalledWith({ changesetId: "cs_1" });
    const diff = changesetDiffQuery(api, "cs_1", "", "ps_2");
    expect(diff.queryKey).toEqual(["changesetDiff", "cs_1", "", "ps_2"]);
    await run(diff);
    expect(api.diffChangeset).toHaveBeenCalledWith({ changesetId: "cs_1", fromPatchset: undefined, toPatchset: "ps_2" });
    await run(sliceRefQuery(api, "acme", "shop"));
    expect(api.resolveSlice).toHaveBeenCalledWith({ ref: { account: "acme", slice: "shop" } });
    await run(sliceBlameQuery(api, { account: "acme", slice: "shop" }, "c1", "/acme/shop/a.go"));
    expect(api.blameFile).toHaveBeenCalledWith({ path: "/acme/shop/a.go", slice: { account: "acme", slice: "shop" } });
    await expect(run(usernameAvailableQuery(api, "mira"))).resolves.toEqual({ available: true });
    await expect(run(pendingClaimsQuery(api))).resolves.toEqual({ claims: [] });
    await expect(run(sliceGitMirrorQuery(api, { account: "acme", slice: "shop" }))).resolves.toEqual({
      url: "https://github.com/acme/shop.git"
    });
    await expect(run(sliceGitMirrorQuery(api, { account: "acme", slice: "shop" }))).resolves.toBeNull();
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
