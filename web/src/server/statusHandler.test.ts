import { describe, expect, it, vi } from "vitest";

import { handleStatusRequest, type ServiceHealthPayload } from "./statusHandler";

describe("handleStatusRequest", () => {
  it("ignores non-status routes", async () => {
    const res = await handleStatusRequest(new Request("https://gitslice.io/status"), "https://api.gitslice.io");
    expect(res).toBeNull();
  });

  it("probes /readyz server-side and returns operational JSON with no-store", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response("ok\n", { status: 200 }));
    const res = await handleStatusRequest(
      new Request("https://gitslice.io/api/status"),
      "https://api.gitslice.io/",
      fetchMock as unknown as typeof fetch
    );
    expect(fetchMock).toHaveBeenCalledWith(
      "https://api.gitslice.io/readyz",
      expect.objectContaining({ method: "GET", cache: "no-store" })
    );
    expect(res?.status).toBe(200);
    expect(res?.headers.get("Cache-Control")).toBe("no-store");
    const body = (await res?.json()) as ServiceHealthPayload;
    expect(body.overall).toBe("operational");
    expect(body.api.status).toBe("operational");
    expect(body.api.httpStatus).toBe(200);
    expect(body.api.latencyMs).toBeGreaterThanOrEqual(1);
  });

  it("reports degraded when /readyz fails", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response("down", { status: 503 }));
    const res = await handleStatusRequest(
      new Request("https://gitslice.io/api/status"),
      "https://api.gitslice.io",
      fetchMock as unknown as typeof fetch
    );
    const body = (await res?.json()) as ServiceHealthPayload;
    expect(body.overall).toBe("degraded");
    expect(body.api.status).toBe("degraded");
    expect(body.api.httpStatus).toBe(503);
  });
});
