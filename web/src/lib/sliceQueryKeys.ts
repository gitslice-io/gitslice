import type { SliceRef } from "../api/types";

// Shared react-query keys for slice source data. The slice page, the folder
// navigator, and the SSR route loader must use identical keys so a directory
// listed by one is never fetched again by another, and SSR-prefetched data is
// picked up on hydration.
function refParts(ref: SliceRef | undefined) {
  return [ref?.account ?? "", ref?.slice ?? ""] as const;
}

export function slicePathQueryKey(ref: SliceRef | undefined, commitId: string, path: string) {
  return ["slicePath", ...refParts(ref), commitId, path] as const;
}

export function sliceDirectoryQueryKey(ref: SliceRef | undefined, commitId: string, path: string) {
  return ["sliceDirectory", ...refParts(ref), commitId, path] as const;
}

export function sliceFileQueryKey(ref: SliceRef | undefined, commitId: string, path: string) {
  return ["sliceFile", ...refParts(ref), commitId, path] as const;
}
