import { AGENT_SESSION_COOKIE } from "./sessionCookie";

export const AGENT_AUTH_HEADER = "x-gitslice-agent-auth";
export const AGENT_WEB_SESSION_TTL_SECONDS = 24 * 60 * 60;
export const AGENT_API_KEY_PREFIX = "gsk_";
const MAX_COOKIE_VALUE_BYTES = 3800;

export interface AgentSessionPublic {
  account: string;
  expiresAt?: string;
  keyId?: string;
  subjectId: string;
}

export interface AgentSessionCookiePayload extends AgentSessionPublic {
  managed?: boolean;
  token: string;
}

export interface AgentAuthResponseBody {
  session: AgentSessionPublic;
  token: string;
}

function toBase64Url(input: string): string {
  const bytes = new TextEncoder().encode(input);
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function fromBase64Url(input: string): string | null {
  if (!input || !/^[A-Za-z0-9_-]+$/.test(input)) {
    return null;
  }
  const padded = input.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - (input.length % 4)) % 4);
  try {
    const binary = atob(padded);
    const bytes = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i += 1) {
      bytes[i] = binary.charCodeAt(i);
    }
    return new TextDecoder().decode(bytes);
  } catch {
    return null;
  }
}

export function isAgentSessionExpired(expiresAt: string | undefined, nowMs = Date.now()): boolean {
  if (!expiresAt) {
    return false;
  }
  const parsed = Date.parse(expiresAt);
  if (!Number.isFinite(parsed)) {
    return true;
  }
  return parsed <= nowMs;
}

export function toPublicAgentSession(payload: AgentSessionCookiePayload): AgentSessionPublic {
  return {
    subjectId: payload.subjectId,
    account: payload.account,
    ...(payload.expiresAt ? { expiresAt: payload.expiresAt } : {}),
    ...(payload.keyId ? { keyId: payload.keyId } : {})
  };
}

export function encodeAgentSessionCookie(payload: AgentSessionCookiePayload): string {
  return toBase64Url(JSON.stringify(payload));
}

export function decodeAgentSessionCookie(
  rawValue: string,
  nowMs = Date.now()
): AgentSessionCookiePayload | null {
  if (!rawValue || rawValue.length > MAX_COOKIE_VALUE_BYTES) {
    return null;
  }
  const jsonText = fromBase64Url(rawValue);
  if (!jsonText) {
    return null;
  }
  try {
    const parsed = JSON.parse(jsonText) as Partial<AgentSessionCookiePayload>;
    if (
      typeof parsed.token !== "string" ||
      !parsed.token.startsWith(AGENT_API_KEY_PREFIX) ||
      typeof parsed.subjectId !== "string" ||
      parsed.subjectId.trim() === ""
    ) {
      return null;
    }
    const expiresAt = typeof parsed.expiresAt === "string" && parsed.expiresAt ? parsed.expiresAt : undefined;
    if (isAgentSessionExpired(expiresAt, nowMs)) {
      return null;
    }
    return {
      token: parsed.token,
      subjectId: parsed.subjectId,
      account: typeof parsed.account === "string" ? parsed.account : "",
      ...(expiresAt ? { expiresAt } : {}),
      ...(typeof parsed.keyId === "string" && parsed.keyId ? { keyId: parsed.keyId } : {}),
      ...(typeof parsed.managed === "boolean" ? { managed: parsed.managed } : {})
    };
  } catch {
    return null;
  }
}

export function readAgentSessionCookie(
  cookieHeader: string,
  nowMs = Date.now()
): AgentSessionCookiePayload | null {
  for (const part of cookieHeader.split(";")) {
    const separator = part.indexOf("=");
    if (separator < 0) {
      continue;
    }
    const name = part.slice(0, separator).trim();
    if (name !== AGENT_SESSION_COOKIE) {
      continue;
    }
    return decodeAgentSessionCookie(part.slice(separator + 1).trim(), nowMs);
  }
  return null;
}

export function buildAgentSessionSetCookie(
  payload: AgentSessionCookiePayload,
  secure: boolean,
  nowMs = Date.now()
): string {
  let maxAge = AGENT_WEB_SESSION_TTL_SECONDS;
  if (payload.expiresAt) {
    const remaining = Math.floor((Date.parse(payload.expiresAt) - nowMs) / 1000);
    if (Number.isFinite(remaining) && remaining > 0) {
      maxAge = remaining;
    }
  }
  const parts = [
    `${AGENT_SESSION_COOKIE}=${encodeAgentSessionCookie(payload)}`,
    "Path=/",
    "HttpOnly",
    "SameSite=Lax",
    `Max-Age=${maxAge}`
  ];
  if (secure) {
    parts.push("Secure");
  }
  return parts.join("; ");
}

export function clearAgentSessionSetCookie(secure: boolean): string {
  const parts = [
    `${AGENT_SESSION_COOKIE}=`,
    "Path=/",
    "HttpOnly",
    "SameSite=Lax",
    "Max-Age=0"
  ];
  if (secure) {
    parts.push("Secure");
  }
  return parts.join("; ");
}

export function isSafeReturnPath(raw: string | null | undefined): raw is string {
  if (!raw || !raw.startsWith("/") || raw.startsWith("//")) {
    return false;
  }
  if (/[\r\n\0]/.test(raw)) {
    return false;
  }
  return !/^\/(?:login|signup|agent-login)(?:[/?#]|$)/.test(raw);
}
