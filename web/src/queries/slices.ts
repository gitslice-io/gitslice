import { queryOptions } from "@tanstack/react-query";

import { isNotFoundError } from "../api/rpcError";
import type { SliceGitMirror, SliceRef } from "../api/types";
import type { ApiClient } from "../api/useApi";
import { listDirectoryAll } from "../components/source/sourceUtils";
import { GLOBAL_REF_NAME } from "../lib/globalRef";

// Slice source and settings data. The slice page, the folder navigator, the
// README/blame views, settings panels, and SSR route loaders read these through
// the same options so cache keys and shapes stay consistent.

function refParts(ref: SliceRef | undefined) {
  return [ref?.account ?? "", ref?.slice ?? ""] as const;
}

export const slicesKeyPrefix = ["slices"] as const;
export const slicesListKey = (account: string) => [...slicesKeyPrefix, account] as const;
export const sliceRefKeyPrefix = ["sliceRef"] as const;
export const sliceRefKey = (account: string, slice: string) => [...sliceRefKeyPrefix, account, slice] as const;
export const globalRefKey = ["globalRef", GLOBAL_REF_NAME] as const;
export const sliceGitMirrorKey = (ref: SliceRef) => ["sliceGitMirror", ...refParts(ref)] as const;

export function slicePathQueryKey(ref: SliceRef | undefined, commitId: string, path: string) {
  return ["slicePath", ...refParts(ref), commitId, path] as const;
}

export function sliceDirectoryQueryKey(ref: SliceRef | undefined, commitId: string, path: string) {
  return ["sliceDirectory", ...refParts(ref), commitId, path] as const;
}

export function sliceFileQueryKey(ref: SliceRef | undefined, commitId: string, path: string) {
  return ["sliceFile", ...refParts(ref), commitId, path] as const;
}

export function sliceRenderedFileQueryKey(ref: SliceRef | undefined, commitId: string, path: string) {
  return ["sliceRenderedFile", ...refParts(ref), commitId, path] as const;
}

export function sliceBlameQueryKey(ref: SliceRef | undefined, commitId: string, path: string) {
  return ["blame", ...refParts(ref), commitId, path] as const;
}

// A slice, by the name it is opened under (a renamed slice opens under its
// old name too).
export function sliceRefQuery(api: Pick<ApiClient, "resolveSlice">, account: string, slice: string) {
  return queryOptions({
    queryKey: sliceRefKey(account, slice),
    queryFn: () => api.resolveSlice({ ref: { account, slice } })
  });
}

// The latest global state, which names the commit every file view reads.
export function globalRefQuery(api: Pick<ApiClient, "getRef">) {
  return queryOptions({
    queryKey: globalRefKey,
    queryFn: async () => {
      const ref = await api.getRef({ refName: GLOBAL_REF_NAME });
      if (!ref.commitId) {
        throw new Error("Latest global state did not return a commit id.");
      }
      return ref;
    }
  });
}

export function slicePathQuery(
  api: Pick<ApiClient, "resolvePath">,
  ref: SliceRef | undefined,
  commitId: string,
  path: string
) {
  return queryOptions({
    queryKey: slicePathQueryKey(ref, commitId, path),
    queryFn: () => api.resolvePath({ commitId, path, slice: ref })
  });
}

export function sliceDirectoryQuery(
  api: ApiClient,
  ref: SliceRef | undefined,
  commitId: string,
  path: string,
  options: { allowMissingDirectory?: boolean } = {}
) {
  return queryOptions({
    queryKey: sliceDirectoryQueryKey(ref, commitId, path),
    queryFn: () =>
      listDirectoryAll(api, {
        allowMissingDirectory: options.allowMissingDirectory,
        commitId,
        path,
        slice: ref
      })
  });
}

export function sliceFileQuery(
  api: Pick<ApiClient, "readFile">,
  ref: SliceRef | undefined,
  commitId: string,
  path: string
) {
  return queryOptions({
    queryKey: sliceFileQueryKey(ref, commitId, path),
    queryFn: () => api.readFile({ commitId, path, slice: ref })
  });
}

// A Markdown file as the server renders it, safe to show as HTML.
export function sliceRenderedFileQuery(
  api: Pick<ApiClient, "renderFile">,
  ref: SliceRef | undefined,
  commitId: string,
  path: string
) {
  return queryOptions({
    queryKey: sliceRenderedFileQueryKey(ref, commitId, path),
    queryFn: () => api.renderFile({ commitId, path, slice: ref })
  });
}

export function sliceBlameQuery(
  api: Pick<ApiClient, "blameFile">,
  ref: SliceRef,
  commitId: string,
  path: string
) {
  return queryOptions({
    queryKey: sliceBlameQueryKey(ref, commitId, path),
    queryFn: () => api.blameFile({ path, slice: ref }),
    staleTime: 60_000
  });
}

export function sliceGitMirrorQuery(
  api: Pick<ApiClient, "getSliceGitMirror">,
  slice: SliceRef
) {
  return queryOptions({
    queryKey: sliceGitMirrorKey(slice),
    queryFn: async (): Promise<SliceGitMirror | null> => {
      try {
        return await api.getSliceGitMirror({ slice });
      } catch (error) {
        if (isNotFoundError(error)) {
          return null;
        }
        throw error;
      }
    }
  });
}
