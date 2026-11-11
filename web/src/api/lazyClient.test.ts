import { beforeEach, describe, expect, it, vi } from "vitest";

const created = vi.fn();

vi.mock("./client", () => ({
  createApiClient: (options: unknown) => {
    created(options);
    return {
      getAuthStatus: async (request: unknown) => ({ echoed: request }),
      resolveSlice: async () => {
        throw new Error("no such slice");
      },
      streamCheckRun: async function* (request: { runId: string }) {
        yield { text: `${request.runId}:1` };
        yield { text: `${request.runId}:2` };
      }
    };
  }
}));

import { createLazyApiClient } from "./lazyClient";

const options = { getToken: async () => null };

describe("createLazyApiClient", () => {
  beforeEach(() => {
    created.mockClear();
  });

  it("builds the client on the first call, once, and passes calls through", async () => {
    const api = createLazyApiClient(options);
    expect(created).not.toHaveBeenCalled();

    await expect(api.getAuthStatus({})).resolves.toEqual({ echoed: {} });
    await api.getAuthStatus({});
    expect(created).toHaveBeenCalledTimes(1);
    expect(created).toHaveBeenCalledWith(options);
  });

  it("rejects as the client does", async () => {
    const api = createLazyApiClient(options);
    await expect(api.resolveSlice({ ref: { account: "a", slice: "b" } })).rejects.toThrow("no such slice");
  });

  it("streams through the client's async generators", async () => {
    const api = createLazyApiClient(options);
    const seen: string[] = [];
    for await (const log of api.streamCheckRun({ runId: "run_1" }, new AbortController().signal)) {
      seen.push((log as { text: string }).text);
    }
    expect(seen).toEqual(["run_1:1", "run_1:2"]);
  });

  it("keeps one function per method and is not a thenable", async () => {
    const api = createLazyApiClient(options);
    expect(api.getAuthStatus).toBe(api.getAuthStatus);
    expect((api as unknown as { then?: unknown }).then).toBeUndefined();
    // Resolving a promise with the client must hand back the client itself.
    await expect(Promise.resolve(api)).resolves.toBe(api);
  });
});
