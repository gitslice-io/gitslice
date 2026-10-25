import type { SliceRef } from "../../api/types";
import type { ApiClient } from "../../api/useApi";

// Every file of a slice at a commit, for the file finder. The server lists
// everything under a path in a few requests (a thousand files take one), so
// the finder can match in the browser as the person types.

const PAGE_SIZE = 10_000;
// A slice this large is searched in part: the finder says so.
export const MAX_FINDER_FILES = 100_000;

export interface SliceFiles {
  // Canonical paths, sorted.
  paths: string[];
  // Not every file is here: the slice has more than MAX_FINDER_FILES, or the
  // server could not list recursively.
  partial: boolean;
}

export async function listSliceFiles(
  api: Pick<ApiClient, "listDirectory">,
  slice: SliceRef,
  commitId: string,
  includedPaths: readonly string[]
): Promise<SliceFiles> {
  const found = new Set<string>();
  let partial = false;
  for (const root of includedPaths) {
    let cursor = "";
    do {
      const page = await api.listDirectory({
        commitId,
        cursor: cursor || undefined,
        pageSize: PAGE_SIZE,
        path: root,
        recursive: true,
        slice
      });
      if (!page.recursive) {
        // An older server lists one level: not the whole slice.
        partial = true;
        break;
      }
      for (const entry of page.entries ?? []) {
        if (entry.kind === "ENTRY_KIND_FILE" && entry.path) {
          found.add(entry.path);
        }
      }
      cursor = page.nextCursor ?? "";
      if (found.size >= MAX_FINDER_FILES) {
        partial = partial || cursor !== "";
        break;
      }
    } while (cursor);
  }
  return { paths: [...found].sort(), partial };
}

// The part of a path the finder leaves out: the folder a slice with one
// included path sits in, which every file shares. With several, none.
export function sharedRoot(includedPaths: readonly string[]): string {
  if (includedPaths.length !== 1) {
    return "";
  }
  return includedPaths[0].replace(/\/+$/, "");
}

// A path as the finder shows it: relative to the shared root.
export function displayPath(path: string, root: string): string {
  if (root && path.startsWith(`${root}/`)) {
    return path.slice(root.length + 1);
  }
  return path.replace(/^\/+/, "");
}
