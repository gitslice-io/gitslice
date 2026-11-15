// Signatures shared with the Go server (service/doorbell.go).

const encoder = new TextEncoder();

function hex(bytes: ArrayBuffer): string {
  return [...new Uint8Array(bytes)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

// sign is HMAC-SHA256 of the purpose, then each part after a zero byte, in
// hex, as the server's Doorbell.mac.
export async function sign(secret: string, purpose: string, ...parts: string[]): Promise<string> {
  const key = await crypto.subtle.importKey("raw", encoder.encode(secret), { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  const data = encoder.encode([purpose, ...parts].join("\0"));
  return hex(await crypto.subtle.sign("HMAC", key, data));
}

// sameString compares in constant time for equal lengths.
export function sameString(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}

// verifyToken checks a daemon's doorbell token
// (gsdb1.<daemon>.<expiry unix>.<mac>) and returns its daemon id, or null.
export async function verifyToken(secret: string, token: string | null, nowMs = Date.now()): Promise<string | null> {
  if (!secret || !token) return null;
  const parts = token.split(".");
  if (parts.length !== 4 || parts[0] !== "gsdb1" || !/^[A-Za-z0-9_-]+$/.test(parts[1]) || !/^\d+$/.test(parts[2])) {
    return null;
  }
  const [, daemon, exp, mac] = parts;
  if (Number(exp) * 1000 < nowMs) return null;
  return sameString(await sign(secret, "doorbell-token", daemon, exp), mac) ? daemon : null;
}

// verifyRing checks the server's signature on a POST /ring body.
export async function verifyRing(secret: string, body: string, signature: string | null): Promise<boolean> {
  if (!secret || !signature) return false;
  return sameString(await sign(secret, "ring", body), signature);
}
