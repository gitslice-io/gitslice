import type { TreeEntry } from "../api/types";
import { entryDisplayName, normalizeRepositoryPath } from "./repoPaths";

// README_MAX_BYTES is the largest README shown inline on a folder page; a bigger
// one is linked instead, so a huge file cannot slow the page down.
export const README_MAX_BYTES = 256 * 1024;

// How many single-folder levels a folder page looks down for a README.
export const NESTED_README_MAX_DEPTH = 8;

const README_NAMES = ["readme.md", "readme.markdown"];

// findReadmeEntry picks the README to show for a folder from its entries:
// README.md, then readme.md (any other casing), then README.markdown. Only files
// directly in the folder count.
export function findReadmeEntry(entries: TreeEntry[]): TreeEntry | undefined {
  let best: TreeEntry | undefined;
  let bestRank = Number.POSITIVE_INFINITY;
  for (const entry of entries) {
    if (entry.kind !== "ENTRY_KIND_FILE") {
      continue;
    }
    const name = entryDisplayName(entry);
    const family = README_NAMES.indexOf(name.toLowerCase());
    if (family < 0) {
      continue;
    }
    const exactCase = name === "README.md" || name === "README.markdown";
    const rank = family * 2 + (exactCase ? 0 : 1);
    if (rank < bestRank) {
      best = entry;
      bestRank = rank;
    }
  }
  return best;
}

// onlyDirectory is the one entry of a folder that holds a single folder and
// nothing else.
export function onlyDirectory(entries: TreeEntry[]): TreeEntry | undefined {
  return entries.length === 1 && entries[0].kind === "ENTRY_KIND_DIRECTORY" ? entries[0] : undefined;
}

// findNestedReadme is the README a folder page shows: the folder's own, or,
// when the folder holds only one folder, that folder's, and so on down, as
// long as each level is a single folder. It stops where the tree branches.
export async function findNestedReadme(
  entries: TreeEntry[],
  list: (path: string) => Promise<TreeEntry[]>
): Promise<TreeEntry | undefined> {
  let current = entries;
  for (let depth = 0; ; depth++) {
    const readme = findReadmeEntry(current);
    if (readme) {
      return readme;
    }
    const only = onlyDirectory(current);
    if (!only || depth >= NESTED_README_MAX_DEPTH) {
      return undefined;
    }
    current = await list(normalizeRepositoryPath(only.path ?? ""));
  }
}
