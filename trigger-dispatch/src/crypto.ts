// Signatures shared with the Go server (internal/triggers/engine.go) and the
// sealing of secret placeholders (design/34_triggers.md).

const encoder = new TextEncoder();
const decoder = new TextDecoder();

async function hmacKey(secret: string): Promise<CryptoKey> {
  return crypto.subtle.importKey("raw", encoder.encode(secret), { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
}

function hex(bytes: ArrayBuffer): string {
  return [...new Uint8Array(bytes)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

// sign is the server's Engine.sign: HMAC-SHA256 of the purpose, then each
// part after a zero byte, in hex.
export async function sign(secret: string, purpose: string, ...parts: string[]): Promise<string> {
  const chunks: Uint8Array[] = [encoder.encode(purpose)];
  for (const part of parts) {
    chunks.push(new Uint8Array([0]), encoder.encode(part));
  }
  const data = new Uint8Array(chunks.reduce((n, c) => n + c.length, 0));
  let offset = 0;
  for (const c of chunks) {
    data.set(c, offset);
    offset += c.length;
  }
  return hex(await crypto.subtle.sign("HMAC", await hmacKey(secret), data));
}

// sameString compares in constant time for equal lengths.
export function sameString(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}

// verifyDispatch checks the server's signature on a POST /run body.
export async function verifyDispatch(secret: string, body: string, signature: string | null): Promise<boolean> {
  if (!secret || !signature) return false;
  return sameString(await sign(secret, "dispatch", body), signature);
}

// A placeholder holds a secret sealed with AES-GCM under a key derived from
// the dispatch secret, with the code hash of the only script whose gate may
// open it, and an expiry.
export interface Sealed {
  v: string; // the value
  code: string; // the script's code hash
  exp: number; // unix ms
}

export const PLACEHOLDER = /gs-secret:v1:[A-Za-z0-9_-]+/g;

export async function sealKey(secret: string): Promise<CryptoKey> {
  const raw = await crypto.subtle.sign("HMAC", await hmacKey(secret), encoder.encode("seal"));
  return crypto.subtle.importKey("raw", raw, { name: "AES-GCM" }, false, ["encrypt", "decrypt"]);
}

function base64url(bytes: Uint8Array): string {
  let s = "";
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function fromBase64url(text: string): Uint8Array {
  const s = atob(text.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((text.length + 3) % 4));
  return Uint8Array.from(s, (c) => c.charCodeAt(0));
}

export async function seal(key: CryptoKey, sealed: Sealed): Promise<string> {
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const ct = new Uint8Array(await crypto.subtle.encrypt({ name: "AES-GCM", iv }, key, encoder.encode(JSON.stringify(sealed))));
  const out = new Uint8Array(iv.length + ct.length);
  out.set(iv);
  out.set(ct, iv.length);
  return "gs-secret:v1:" + base64url(out);
}

export async function unseal(key: CryptoKey, placeholder: string): Promise<Sealed | null> {
  try {
    const bytes = fromBase64url(placeholder.slice("gs-secret:v1:".length));
    const plain = await crypto.subtle.decrypt({ name: "AES-GCM", iv: bytes.slice(0, 12) }, key, bytes.slice(12));
    return JSON.parse(decoder.decode(plain)) as Sealed;
  } catch {
    return null;
  }
}

export class PlaceholderError extends Error {}

// openPlaceholders replaces every placeholder in text with its value. A
// placeholder that does not open, belongs to another script or has expired
// is an error: the request must not go out with it.
export async function openPlaceholders(key: CryptoKey, text: string, codeHash: string, now = Date.now()): Promise<string> {
  const found = text.match(PLACEHOLDER);
  if (!found) return text;
  const values = new Map<string, string>();
  for (const placeholder of new Set(found)) {
    const sealed = await unseal(key, placeholder);
    if (!sealed) throw new PlaceholderError("a secret placeholder does not open");
    if (sealed.code !== codeHash) throw new PlaceholderError("a secret placeholder belongs to another trigger");
    if (sealed.exp < now) throw new PlaceholderError("a secret placeholder has expired");
    values.set(placeholder, sealed.v);
  }
  return text.replace(PLACEHOLDER, (p) => values.get(p) ?? p);
}
