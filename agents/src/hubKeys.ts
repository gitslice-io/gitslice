// Canonical Durable Object storage keys for Hub.

export const HUB_KEYS = {
  baseline: "baseline",
  baselineFor: "baselineFor",
  baselineGen: "baselineGen",
  counters: "counters",
  events: "events",
  floor: "floor",
  ident: "ident",
  nativeSince: "nativeSince",
  retired: "retired",
  sessionPrefix: "s:",
} as const;

export function sessionStorageKey(id: string): string {
  return `${HUB_KEYS.sessionPrefix}${id}`;
}
