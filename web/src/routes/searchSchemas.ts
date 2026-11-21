import { pathSearchValue } from "./slice-detail/sourceTree";
import { parseStatusTab, type StatusTab } from "./changesets-list/filters";

// Validated search (query string) shapes of the routes whose addresses carry
// state. Each validator is a pure function from whatever the address held
// (the router has already JSON-parsed it, so "1" may arrive as 1 or "1") to
// the typed object the pages and the SSR loaders read, so the two cannot
// disagree about what an address means and navigation is checked by the
// compiler. Unknown or malformed values are dropped, never thrown on: a broken
// deep link opens the page as if the part were missing.

function text(value: unknown): string | undefined {
  if (typeof value === "string") {
    const trimmed = value.trim();
    return trimmed === "" ? undefined : trimmed;
  }
  return undefined;
}

// Typed text, kept as typed (a trailing space is part of what is being typed),
// unless it is blank. The address may have parsed it as a number (?q=2024).
function typedText(value: unknown): string | undefined {
  if (typeof value === "number" && Number.isFinite(value)) {
    return String(value);
  }
  return typeof value === "string" && value.trim() !== "" ? value : undefined;
}

// An on/off switch in the address: `?history`, `?history=1`, `?history=true`.
function flag(value: unknown): true | undefined {
  if (value === true || value === 1) {
    return true;
  }
  if (typeof value === "string") {
    const normalized = value.trim().toLowerCase();
    return normalized !== "" && normalized !== "0" && normalized !== "false" ? true : undefined;
  }
  return undefined;
}

export interface SliceDetailSearch {
  // The file or folder shown.
  path?: string;
  // The history, tags, Folder Policy (and legacy ?triggers=1 alias), and (for
  // a file) blame views are on, in the address so the back gesture closes them instead
  // of leaving the slice.
  history?: true;
  tags?: true;
  policy?: true;
  triggers?: true;
  blame?: true;
  // The change selected in the blame, a short commit id.
  blameAt?: string;
  // The commit the slice is browsed at (from a tag); unset is the latest.
  at?: string;
}

export function validateSliceDetailSearch(search: Record<string, unknown>): SliceDetailSearch {
  const path = pathSearchValue(search.path);
  const blameAt = text(search.blameAt);
  const at = text(search.at);
  return {
    ...(path ? { path } : {}),
    ...(flag(search.history) ? { history: true as const } : {}),
    ...(flag(search.tags) ? { tags: true as const } : {}),
    ...(flag(search.policy) ? { policy: true as const } : {}),
    ...(flag(search.triggers) ? { triggers: true as const } : {}),
    ...(flag(search.blame) ? { blame: true as const } : {}),
    ...(blameAt ? { blameAt } : {}),
    ...(at ? { at } : {})
  };
}

export type DiffSide = "original" | "changed";

export interface ChangesetDetailSearch {
  // The patchsets compared: the base and the one shown.
  from?: string;
  to?: string;
  // The file the diff scrolls to.
  file?: string;
  // The conversation drawer is open.
  conversation?: true;
  // A changed file read in full, and the side of it.
  view?: string;
  side?: DiffSide;
  // The diff's mode; unified is the default and stays out of the address.
  diff?: "split";
}

export function validateChangesetDetailSearch(search: Record<string, unknown>): ChangesetDetailSearch {
  const from = text(search.from);
  const to = text(search.to);
  const file = text(search.file);
  const view = text(search.view);
  const side = search.side === "original" || search.side === "changed" ? search.side : undefined;
  return {
    ...(from ? { from } : {}),
    ...(to ? { to } : {}),
    ...(file ? { file } : {}),
    ...(flag(search.conversation) ? { conversation: true as const } : {}),
    ...(view ? { view } : {}),
    ...(side ? { side } : {}),
    ...(search.diff === "split" ? { diff: "split" as const } : {})
  };
}

export interface ChangesetsSearch {
  // The slice, as account:slice (account/slice also opens).
  slice?: string;
  // The status group; open is the default and stays out of the address.
  status?: StatusTab;
  // The typed filter text.
  q?: string;
}

export function validateChangesetsSearch(search: Record<string, unknown>): ChangesetsSearch {
  const slice = text(search.slice);
  const status = text(search.status) ? parseStatusTab(search.status) : undefined;
  const q = typedText(search.q);
  return {
    ...(slice ? { slice } : {}),
    ...(status ? { status } : {}),
    ...(q ? { q } : {})
  };
}

export interface SliceCreateSearch {
  // The account the new slice starts in.
  account?: string;
}

export function validateSliceCreateSearch(search: Record<string, unknown>): SliceCreateSearch {
  const account = text(search.account);
  return account ? { account } : {};
}
