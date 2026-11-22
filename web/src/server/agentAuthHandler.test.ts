import { describe, expect, it, vi } from "vitest";

import {
  AGENT_AUTH_HEADER,
  buildAgentSessionSetCookie
} from "../lib/agentSession";
import { AGENT_SESSION_COOKIE } from "../lib/sessionCookie";
import { handleAgentAuthRequest } from "./agentAuthHandler";

describe("handleAgentAuthRequest", () => {
  const now = () => Date.parse("2026-11-21T12:00:00Z");

  it("returns null for non-agent-auth paths", async () => {
    const res = await handleAgentAuthRequest(new Request("https://gitslice.io/slices/a/b"), "https://api.gitslice.io");
    expect(res).toBeNull();
  });

  it("exchanges a long-lived API key for a short-lived web child key and sets an HttpOnly cookie", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (input) => {
      const url = String(input);
      if (url.endsWith("/GetAuthStatus")) {
        return new Response(JSON.stringify({ subjectId: "agent_01", accounts: ["release-bot"] }), { status: 200 });
      }
      if (url.endsWith("/ListApiKeys")) {
        return new Response(JSON.stringify({ keys: [{ id: "key_root", purpose: "" }] }), { status: 200 });
      }
      if (url.endsWith("/CreateApiKey")) {
        return new Response(
          JSON.stringify({
            secret: "gsk_web_child_secret",
            key: { id: "key_web_1", expiresAt: "2026-11-22T12:00:00Z", purpose: "web" }
          }),
          { status: 200 }
        );
      }
      throw new Error(`unexpected url ${url}`);
    });

    const req = new Request("https://gitslice.io/auth/agent/login", {
      method: "POST",
      headers: {
        Origin: "https://gitslice.io",
        "Content-Type": "application/json"
      },
      body: JSON.stringify({ apiKey: "gsk_root_permanent" })
    });

    const res = await handleAgentAuthRequest(req, "https://api.gitslice.io", fetchMock, now);
    expect(res?.status).toBe(200);
    expect(res?.headers.get("cache-control")).toBe("no-store");
    const setCookie = res?.headers.get("set-cookie") ?? "";
    expect(setCookie).toContain(`${AGENT_SESSION_COOKIE}=`);
    expect(setCookie).toContain("HttpOnly");
    expect(setCookie).toContain("SameSite=Lax");
    expect(setCookie).toContain("Secure");

    const body = (await res?.json()) as { token: string; session: { subjectId: string; account: string; keyId: string } };
    expect(body.token).toBe("gsk_web_child_secret");
    expect(body.session).toMatchObject({
      subjectId: "agent_01",
      account: "release-bot",
      keyId: "key_web_1"
    });
  });

  it("reuses a dedicated expiring web-purpose key directly without minting a grandchild key", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (input) => {
      const url = String(input);
      if (url.endsWith("/GetAuthStatus")) {
        return new Response(JSON.stringify({ subjectId: "agent_01", accounts: ["release-bot"] }), { status: 200 });
      }
      if (url.endsWith("/ListApiKeys")) {
        return new Response(
          JSON.stringify({
            keys: [{ id: "key_web_pre", purpose: "web", expiresAt: "2026-11-22T00:00:00Z" }]
          }),
          { status: 200 }
        );
      }
      throw new Error(`CreateApiKey should not be called: ${url}`);
    });

    const req = new Request("https://gitslice.io/auth/agent/login", {
      method: "POST",
      headers: {
        Origin: "https://gitslice.io",
        "Content-Type": "application/json"
      },
      body: JSON.stringify({ apiKey: "gsk_pre_minted_web" })
    });

    const res = await handleAgentAuthRequest(req, "https://api.gitslice.io", fetchMock, now);
    expect(res?.status).toBe(200);
    const body = (await res?.json()) as { token: string; session: { keyId: string } };
    expect(body.token).toBe("gsk_pre_minted_web");
    expect(body.session.keyId).toBe("key_web_pre");
  });

  it("returns the token from /auth/agent/token and revokes managed web keys on /auth/agent/logout", async () => {
    const cookieHeader = buildAgentSessionSetCookie(
      {
        token: "gsk_web_child_secret",
        keyId: "key_web_1",
        managed: true,
        subjectId: "agent_01",
        account: "release-bot",
        expiresAt: "2026-11-22T12:00:00Z"
      },
      true,
      now()
    ).split(";")[0];

    const tokenRes = await handleAgentAuthRequest(
      new Request("https://gitslice.io/auth/agent/token", {
        method: "POST",
        headers: {
          Cookie: cookieHeader,
          [AGENT_AUTH_HEADER]: "1"
        }
      }),
      "https://api.gitslice.io",
      vi.fn(),
      now
    );
    expect(tokenRes?.status).toBe(200);
    expect(await tokenRes?.json()).toMatchObject({
      token: "gsk_web_child_secret",
      session: { subjectId: "agent_01", account: "release-bot" }
    });

    const revokeCalls: Array<{ url: string; body: string }> = [];
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async (input, init) => {
      revokeCalls.push({ url: String(input), body: String(init?.body ?? "") });
      return new Response(JSON.stringify({}), { status: 200 });
    });

    const logoutRes = await handleAgentAuthRequest(
      new Request("https://gitslice.io/auth/agent/logout", {
        method: "POST",
        headers: {
          Cookie: cookieHeader,
          Origin: "https://gitslice.io"
        }
      }),
      "https://api.gitslice.io",
      fetchMock,
      now
    );
    expect(logoutRes?.status).toBe(200);
    expect(logoutRes?.headers.get("set-cookie")).toContain("Max-Age=0");
    expect(revokeCalls).toEqual([
      {
        url: "https://api.gitslice.io/gitslice.core.v1.AuthService/RevokeApiKey",
        body: JSON.stringify({ keyId: "key_web_1" })
      }
    ]);
  });

  it("rejects cross-origin POST requests", async () => {
    const res = await handleAgentAuthRequest(
      new Request("https://gitslice.io/auth/agent/token", {
        method: "POST",
        headers: { Origin: "https://evil.example" }
      }),
      "https://api.gitslice.io"
    );
    expect(res?.status).toBe(403);
  });
});
