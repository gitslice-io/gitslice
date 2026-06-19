import { cleanup, render, waitFor } from "@testing-library/react";
import { useEffect } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useApi } from "./useApi";

const authMock = vi.hoisted(() => ({
  current: {
    getToken: vi.fn<() => Promise<string | null>>(),
    isLoaded: true,
    isSignedIn: false
  }
}));

vi.mock("@clerk/clerk-react", () => ({
  useAuth: () => authMock.current
}));

describe("useApi", () => {
  beforeEach(() => {
    authMock.current = {
      getToken: vi.fn<() => Promise<string | null>>(),
      isLoaded: true,
      isSignedIn: false
    };
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("does not request a token or send authorization when signed out", async () => {
    const fetchMock = mockFetch({ id: "slice_public" });
    render(<InvokeResolveSlice />);

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    expect(authMock.current.getToken).not.toHaveBeenCalled();
    expect(requestHeaders(fetchMock).Authorization).toBeUndefined();
  });

  it("sends a bearer token when signed in", async () => {
    authMock.current = {
      getToken: vi.fn<() => Promise<string | null>>().mockResolvedValue("token_123"),
      isLoaded: true,
      isSignedIn: true
    };
    const fetchMock = mockFetch({ id: "slice_private" });
    render(<InvokeResolveSlice />);

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    expect(authMock.current.getToken).toHaveBeenCalledTimes(1);
    expect(requestHeaders(fetchMock).Authorization).toBe("Bearer token_123");
  });
});

function InvokeResolveSlice() {
  const api = useApi("https://api.test");

  useEffect(() => {
    void api.resolveSlice({ ref: { account: "acme", slice: "public" } });
  }, [api]);

  return null;
}

function mockFetch(body: unknown) {
  const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(
    new Response(JSON.stringify(body), {
      headers: { "Content-Type": "application/json" },
      status: 200
    })
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function requestHeaders(fetchMock: ReturnType<typeof mockFetch>) {
  const init = fetchMock.mock.calls[0]?.[1] as RequestInit | undefined;
  return (init?.headers ?? {}) as Record<string, string>;
}
