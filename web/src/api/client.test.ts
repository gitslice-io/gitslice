import { afterEach, describe, expect, it, vi } from "vitest";

import { API_SERVICES, createApiClient } from "./client";
import { SERVER_STREAMING_METHODS } from "./streamingMethods";

describe("createApiClient integer request encoding", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("keeps repository and changeset list limits as int32 numbers", async () => {
    const fetchMock = mockFetch();
    const api = createApiClient({
      baseUrl: "https://api.test",
      fetch: fetchMock,
      getToken: async () => null
    });

    await api.listCommits({
      refName: "refs/global/main",
      limit: 50,
      slice: { account: "slices", slice: "home" }
    });
    await api.listChangesets({
      authoringSlice: { account: "slices", slice: "home" },
      limit: 200
    });

    expect(requestBody(fetchMock, 0)).toMatchObject({ limit: 50 });
    expect(requestBody(fetchMock, 1)).toMatchObject({ limit: 200 });
  });

  it("encodes the conversation event limit as an int64 string", async () => {
    const fetchMock = mockFetch();
    const api = createApiClient({
      baseUrl: "https://api.test",
      fetch: fetchMock,
      getToken: async () => null
    });

    await api.getConversationEvents({
      conversationId: "conversation_1",
      limit: 200
    });

    expect(requestBody(fetchMock, 0)).toMatchObject({ limit: "200" });
  });
});

describe("createApiClient built from the service descriptors", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  const options = (fetch: typeof globalThis.fetch) => ({ baseUrl: "https://api.test", fetch, getToken: async () => null });

  it("offers every unary and server-streaming RPC of the services, and no other", () => {
    const api = createApiClient(options(mockFetch())) as unknown as Record<string, unknown>;
    const offered = new Set<string>();
    for (const service of API_SERVICES) {
      for (const method of service.methods) {
        const streams = method.methodKind === "server_streaming";
        if (method.methodKind === "unary" || streams) {
          offered.add(method.localName);
          expect(typeof api[method.localName], method.localName).toBe("function");
        } else {
          expect(api[method.localName], method.localName).toBeUndefined();
        }
      }
    }
    expect(Object.keys(api).sort()).toEqual([...offered].sort());
  });

  it("names every server-streaming method for the lazy client", () => {
    const streaming = API_SERVICES.flatMap((service) =>
      service.methods.filter((method) => method.methodKind === "server_streaming").map((method) => method.localName)
    );
    expect([...SERVER_STREAMING_METHODS].sort()).toEqual(streaming.sort());
  });

  it("calls RPCs the hand-written client never listed, with their fields typed by the proto", async () => {
    const fetchMock = mockFetch();
    const api = createApiClient(options(fetchMock));

    await api.createGroup({ account: "acme", name: "qa", description: "testers" });
    await api.handoffChangeset({ changesetId: "cs_1", username: "bob" });
    await api.allowPatchsetSecrets({ changesetId: "cs_1", patchsetId: "ps_1" });
    await api.createPathIntent({ slice: { account: "acme", slice: "shop" }, pattern: "web/**", ttlSeconds: "7200" });
    await api.createDelegatedAgent({ username: "release-bot", slice: { account: "acme", slice: "shop" }, role: "writer" });

    expect(requestUrls(fetchMock)).toEqual([
      "https://api.test/gitslice.core.v1.AuthService/CreateGroup",
      "https://api.test/gitslice.core.v1.ChangesetService/HandoffChangeset",
      "https://api.test/gitslice.core.v1.CheckService/AllowPatchsetSecrets",
      "https://api.test/gitslice.core.v1.ChangesetService/CreatePathIntent",
      "https://api.test/gitslice.core.v1.AuthService/CreateDelegatedAgent"
    ]);
    expect(requestBody(fetchMock, 0)).toEqual({ account: "acme", name: "qa", description: "testers" });
    expect(requestBody(fetchMock, 3)).toMatchObject({ pattern: "web/**", ttlSeconds: "7200" });
  });

  it("sends an avatar image as bytes whatever the field is called", async () => {
    const fetchMock = mockFetch();
    const api = createApiClient(options(fetchMock));
    await api.setAccountAvatar({ account: "nic", image: new Uint8Array([1, 2, 3]) });
    expect(requestBody(fetchMock, 0)).toEqual({ account: "nic", image: "AQID" });
  });

  it("reads int64 as strings and enums as names from a response", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(
      async () =>
        new Response(JSON.stringify({ entries: [{ name: "a.go", kind: "ENTRY_KIND_FILE", size: "9007199254740993" }] }), {
          headers: { "Content-Type": "application/json" },
          status: 200
        })
    );
    const api = createApiClient(options(fetchMock));
    await expect(api.listDirectory({ commitId: "c", path: "/", pageSize: 10 })).resolves.toMatchObject({
      entries: [{ name: "a.go", kind: "ENTRY_KIND_FILE", size: "9007199254740993" }]
    });
  });
});

function requestUrls(fetchMock: ReturnType<typeof mockFetch>) {
  return fetchMock.mock.calls.map((call) => String(call[0] instanceof Request ? call[0].url : call[0]));
}

function mockFetch() {
  return vi.fn<typeof fetch>().mockImplementation(async () =>
    new Response("{}", {
      headers: { "Content-Type": "application/json" },
      status: 200
    })
  );
}

function requestBody(fetchMock: ReturnType<typeof mockFetch>, call: number) {
  const init = fetchMock.mock.calls[call]?.[1] as RequestInit | undefined;
  if (!ArrayBuffer.isView(init?.body)) {
    throw new Error("expected a Uint8Array request body");
  }
  const bytes = new Uint8Array(
    init.body.buffer,
    init.body.byteOffset,
    init.body.byteLength
  );
  return JSON.parse(new TextDecoder().decode(bytes)) as Record<
    string,
    unknown
  >;
}
