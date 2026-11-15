// Signatures shared with the Go server (service/doorbell.go).

const encoder = new TextEncoder();
const MAX_CACHED_KEYS = 8;
const hmacKeys = new Map<string, Promise<CryptoKey>>();

export const MAX_RING_SKEW_S = 60;

function hmacKey(secret: string): Promise<CryptoKey> {
  let cached = hmacKeys.get(secret);
  if (!cached) {
    if (hmacKeys.size >= MAX_CACHED_KEYS) {
      const oldest = hmacKeys.keys().next().value;
      if (oldest !== undefined) hmacKeys.delete(oldest);
    }
    cached = crypto.subtle
      .importKey("raw", encoder.encode(secret), { name: "HMAC", hash: "SHA-256" }, false, ["sign"])
      .catch((err) => {
        hmacKeys.delete(secret);
        throw err;
      });
    hmacKeys.set(secret, cached);
  }
  return cached;
}

function hex(bytes: ArrayBuffer): string {
  return [...new Uint8Array(bytes)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

// sign is HMAC-SHA256 of the purpose, then each part after a zero byte, in
// hex, as the server's Doorbell.mac.
export async function sign(secret: string, purpose: string, ...parts: string[]): Promise<string> {
  const key = await hmacKey(secret);
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

// validRingBody checks that a verified /ring JSON payload names a daemon and
// carries a timestamp within MAX_RING_SKEW_S of nowMs.
export function validRingBody(body: string, nowMs = Date.now()): { daemonId: string; queued: number } | null {
  let msg: { daemon_id?: unknown; ts?: unknown; queued?: unknown };
  try {
    msg = JSON.parse(body) as { daemon_id?: unknown; ts?: unknown; queued?: unknown };
  } catch {
    return null;
  }
  if (typeof msg.daemon_id !== "string" || !msg.daemon_id) return null;
  if (typeof msg.ts !== "number" || !Number.isFinite(msg.ts) || msg.ts <= 0) return null;
  if (Math.abs(nowMs / 1000 - msg.ts) > MAX_RING_SKEW_S) return null;
  return { daemonId: msg.daemon_id, queued: Math.max(0, Number(msg.queued) || 0) };
}
