// Object ids and content hashes are sha256 values, usually carrying an algorithm
// label (e.g. "sha256:ab12cd…"). Git and GitHub abbreviate hashes by their
// leading hex prefix — never a suffix — because objects resolve by that prefix,
// and they keep the full value available on demand. Mirror that here: strip the
// algorithm label, show a short leading prefix, and surface the full value via a
// title tooltip at each call site.

const ALGORITHM_PREFIX = /^[a-z0-9]+:/i;

// Default abbreviation length. Git's own default display is 7 hex chars; we use
// a slightly longer prefix for sha256 to keep collisions unlikely while staying
// scannable.
export const SHORT_HASH_LENGTH = 12;

export function stripHashAlgorithm(id: string): string {
  return id.replace(ALGORITHM_PREFIX, "");
}

// Abbreviated hash for display: the leading hex characters with the algorithm
// label removed. Returns "" for empty input so callers can render conditionally.
export function shortHash(
  id: string | undefined | null,
  length = SHORT_HASH_LENGTH
): string {
  if (!id) {
    return "";
  }
  return stripHashAlgorithm(id).slice(0, length);
}
