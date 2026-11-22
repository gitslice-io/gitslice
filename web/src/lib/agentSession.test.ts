import { describe, expect, it } from "vitest";

import {
  decodeAgentSessionCookie,
  encodeAgentSessionCookie,
  isSafeReturnPath,
  readAgentSessionCookie
} from "./agentSession";
import { AGENT_SESSION_COOKIE } from "./sessionCookie";

describe("agentSession cookie codec", () => {
  it("round-trips a valid session payload", () => {
    const payload = {
      token: "gsk_child_123",
      keyId: "key_abc",
      managed: true,
      subjectId: "agent_01",
      account: "release-bot",
      expiresAt: "2026-11-22T12:00:00Z"
    };
    const encoded = encodeAgentSessionCookie(payload);
    const decoded = decodeAgentSessionCookie(encoded, Date.parse("2026-11-21T12:00:00Z"));
    expect(decoded).toEqual(payload);
  });

  it("rejects expired, non-gsk, or corrupted cookies", () => {
    const expired = encodeAgentSessionCookie({
      token: "gsk_child_123",
      subjectId: "agent_01",
      account: "release-bot",
      expiresAt: "2026-11-20T12:00:00Z"
    });
    expect(decodeAgentSessionCookie(expired, Date.parse("2026-11-21T12:00:00Z"))).toBeNull();
    expect(decodeAgentSessionCookie("not-base64!!!")).toBeNull();
    const badPrefix = encodeAgentSessionCookie({
      token: "jwt_not_an_api_key",
      subjectId: "agent_01",
      account: "release-bot"
    });
    expect(decodeAgentSessionCookie(badPrefix)).toBeNull();
  });

  it("extracts gs_agent_session from a multi-cookie header", () => {
    const encoded = encodeAgentSessionCookie({
      token: "gsk_session_1",
      subjectId: "agent_42",
      account: "triage-bot"
    });
    const parsed = readAgentSessionCookie(`theme=dark; ${AGENT_SESSION_COOKIE}=${encoded}; other=1`);
    expect(parsed).toMatchObject({ token: "gsk_session_1", subjectId: "agent_42", account: "triage-bot" });
  });
});

describe("isSafeReturnPath", () => {
  it("accepts internal app paths and rejects external or auth-loop paths", () => {
    expect(isSafeReturnPath("/slices/acme/app")).toBe(true);
    expect(isSafeReturnPath("/accounts/bot?sort=name")).toBe(true);
    expect(isSafeReturnPath("https://evil.example")).toBe(false);
    expect(isSafeReturnPath("//evil.example")).toBe(false);
    expect(isSafeReturnPath("/login")).toBe(false);
    expect(isSafeReturnPath("/agent-login")).toBe(false);
  });
});
