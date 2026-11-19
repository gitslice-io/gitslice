// Secret masking for bash container logs and error messages (defense-in-depth
// alongside the Go server's internal/secretmask).

const MIN_SECRET_LEN = 4;

function base64Encode(value: string): string {
  const bytes = new TextEncoder().encode(value);
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin);
}

// expandVariants collects raw values, multi-line segments, base64, and
// URL-encoded variants of secrets (>= 4 chars), sorted longest-first so longer
// tokens mask before substrings.
export function expandSecretVariants(secretValues: readonly string[]): string[] {
  const seen = new Set<string>();
  const add = (s: string) => {
    if (s.length >= MIN_SECRET_LEN) {
      seen.add(s);
    }
  };
  for (const raw of secretValues) {
    if (!raw) continue;
    add(raw);
    for (const line of raw.split(/\r?\n/)) {
      const trimmed = line.trim();
      if (trimmed.length >= MIN_SECRET_LEN) {
        add(trimmed);
      }
    }
    if (raw.length >= MIN_SECRET_LEN) {
      add(base64Encode(raw));
      const encoded = encodeURIComponent(raw);
      if (encoded !== raw) {
        add(encoded);
      }
    }
  }
  return [...seen].sort((a, b) => b.length - a.length);
}

export function maskSecrets(text: string, variants: readonly string[]): string {
  if (!text || variants.length === 0) return text;
  let out = text;
  for (const variant of variants) {
    if (out.includes(variant)) {
      out = out.split(variant).join("***");
    }
  }
  return out;
}
