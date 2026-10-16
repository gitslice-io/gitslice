import type { TreeEntry } from "../../api/types";
import { entryDisplayName } from "../../components/source/sourceUtils";

// README_MAX_BYTES is the largest README shown inline on a folder page; a bigger
// one is linked instead, so a huge file cannot slow the page down.
export const README_MAX_BYTES = 256 * 1024;

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
