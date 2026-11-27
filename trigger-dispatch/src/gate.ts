// What an automation script may reach (design/34_automations.md): https, to the hosts
// its trigger lists in network, by public DNS name.

import { openPlaceholders } from "./crypto.ts";

const BLOCKED_HOST_SUFFIXES = ["localhost", "internal", "local", "home.arpa"];

function reservedHost(host: string): boolean {
  if (!host || !host.includes(".") || host.startsWith(".") || host.includes("..") || /\s/.test(host)) {
    return true;
  }
  for (const suffix of BLOCKED_HOST_SUFFIXES) {
    if (host === suffix || host.endsWith("." + suffix)) return true;
  }
  return false;
}

// hostAllowed matches a host against network entries: an exact name, or
// "*.example.com" for any name under example.com (not example.com itself).
// Loopback, internal/mDNS suffixes, single-label hosts, and IP literals are
// never allowed even if listed in network.
export function hostAllowed(host: string, network: readonly string[]): boolean {
  host = host.toLowerCase();
  if (host.endsWith(".")) host = host.slice(0, -1);
  if (reservedHost(host) || isAddress(host)) return false;
  for (const entry of network) {
    let e = entry.trim().toLowerCase();
    if (e.endsWith(".")) e = e.slice(0, -1);
    if (!e) continue;
    if (e.startsWith("*.")) {
      const base = e.slice(2);
      if (!base.includes(".") || reservedHost(base) || isAddress(base)) continue;
      if (host.endsWith("." + base) && host.length > base.length + 1) return true;
    } else if (host === e) {
      return true;
    }
  }
  return false;
}

// isAddress reports an IPv4/IPv6 literal (including shorthand, decimal, hex,
// or octal forms), which network never allows.
export function isAddress(host: string): boolean {
  if (!host) return false;
  if (host.startsWith("[") || host.endsWith("]") || host.includes(":")) return true;
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(host)) return true;
  const lastDot = host.lastIndexOf(".");
  const lastLabel = lastDot >= 0 ? host.slice(lastDot + 1) : host;
  if (/^\d+$/.test(lastLabel) || /^0x[0-9a-f]+$/i.test(lastLabel)) return true;
  return false;
}

// textual says whether a body is worth searching for placeholders.
export function textual(contentType: string | null): boolean {
  if (!contentType) return true;
  const t = contentType.toLowerCase();
  return t.startsWith("text/") || t.includes("json") || t.includes("x-www-form-urlencoded") || t.includes("xml");
}

export class GatePolicyError extends Error {}

export interface PreparedGateRequest {
  target: string;
  method: string;
  headers: Headers;
  body: BodyInit | null;
}

export type GateRequestLike = Pick<Request, "url" | "method" | "headers" | "body" | "text">;

// prepareGateRequest validates outbound request URL policy both before and
// after expanding secret placeholders so secrets cannot rewrite URL scheme,
// credentials, or origin/authority.
export async function prepareGateRequest(
  request: GateRequestLike,
  key: CryptoKey,
  scope: string,
  network: readonly string[],
): Promise<PreparedGateRequest> {
  let url: URL;
  try {
    url = new URL(request.url);
  } catch {
    throw new GatePolicyError("invalid outbound URL");
  }
  if (url.protocol !== "https:") {
    throw new GatePolicyError(`trigger scripts may only use https (${url.protocol})`);
  }
  if (url.username !== "" || url.password !== "") {
    throw new GatePolicyError("trigger scripts may not send URL credentials");
  }
  if (!hostAllowed(url.hostname, network)) {
    throw new GatePolicyError(`${url.hostname} is not in the trigger's network`);
  }

  const target = await openPlaceholders(key, request.url, scope);
  let finalUrl: URL;
  try {
    finalUrl = new URL(target);
  } catch {
    throw new GatePolicyError("secret placeholder produced an invalid URL");
  }
  if (finalUrl.protocol !== "https:") {
    throw new GatePolicyError(`trigger scripts may only use https (${finalUrl.protocol})`);
  }
  if (finalUrl.username !== "" || finalUrl.password !== "") {
    throw new GatePolicyError("trigger scripts may not send URL credentials");
  }
  if (finalUrl.origin !== url.origin) {
    throw new GatePolicyError("secret placeholders may not change the request origin");
  }
  if (!hostAllowed(finalUrl.hostname, network)) {
    throw new GatePolicyError(`${finalUrl.hostname} is not in the trigger's network`);
  }

  const headers = new Headers();
  for (const [name, value] of request.headers) {
    const expanded = await openPlaceholders(key, value, scope);
    if (/[\r\n]/.test(expanded)) {
      throw new GatePolicyError(`invalid header value for ${name}`);
    }
    if (name.toLowerCase() === "host") {
      if (expanded.toLowerCase() !== finalUrl.host.toLowerCase()) {
        throw new GatePolicyError("host header does not match target host");
      }
      continue;
    }
    headers.set(name, expanded);
  }

  let body: BodyInit | null = null;
  if (request.body && !["GET", "HEAD"].includes(request.method.toUpperCase())) {
    if (textual(request.headers.get("content-type"))) {
      body = await openPlaceholders(key, await request.text(), scope);
    } else {
      body = request.body;
    }
  }

  return {
    target: finalUrl.toString(),
    method: request.method,
    headers,
    body,
  };
}
