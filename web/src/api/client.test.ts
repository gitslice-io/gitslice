import { afterEach, describe, expect, it, vi } from "vitest";

import { createApiClient } from "./client";

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
