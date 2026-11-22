import { describe, expect, it, vi } from "vitest";

import { handleMCPProxyRequest, mcpProxyTarget } from "./mcpProxy";

describe("mcpProxy", () => {
  it("matches /mcp and preserves query parameters", () => {
    const req = new Request("https://gitslice.io/mcp?slice=alice/app", { method: "POST" });
    expect(mcpProxyTarget(req, "https://api.gitslice.io/")).toBe(
      "https://api.gitslice.io/mcp?slice=alice/app"
    );
  });

  it("ignores non-mcp paths and empty api base", () => {
    expect(mcpProxyTarget(new Request("https://gitslice.io/slices"), "https://api.gitslice.io")).toBeUndefined();
    expect(mcpProxyTarget(new Request("https://gitslice.io/mcp"), "")).toBeUndefined();
  });

  it("forwards auth and MCP headers to the upstream API", async () => {
    const mockFetch = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ jsonrpc: "2.0", id: 1, result: {} }), {
        status: 200,
        headers: { "Content-Type": "application/json" }
      })
    );
    const req = new Request("https://gitslice.io/mcp?slice=alice/app", {
      method: "POST",
      headers: {
        Authorization: "Bearer gsk_test",
        "Content-Type": "application/json",
        "MCP-Protocol-Version": "2025-03-26",
        "X-Gitslice-Slice": "alice/app",
        Cookie: "session=secret_should_not_forward"
      },
      body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "initialize" })
    });

    const resp = await handleMCPProxyRequest(req, "https://api.gitslice.io", mockFetch);
    expect(resp?.status).toBe(200);
    expect(mockFetch).toHaveBeenCalledTimes(1);
    const [target, init] = mockFetch.mock.calls[0] as [string, RequestInit];
    expect(target).toBe("https://api.gitslice.io/mcp?slice=alice/app");
    const forwarded = new Headers(init.headers);
    expect(forwarded.get("authorization")).toBe("Bearer gsk_test");
    expect(forwarded.get("mcp-protocol-version")).toBe("2025-03-26");
    expect(forwarded.get("x-gitslice-slice")).toBe("alice/app");
    expect(forwarded.get("cookie")).toBeNull();
  });
});
