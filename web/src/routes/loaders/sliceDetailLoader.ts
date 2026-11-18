import { isNotFound } from "@tanstack/react-router";
import type { QueryClient } from "@tanstack/react-query";

import type { Slice } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import {
  isSliceProjectionDirectoryPath,
  normalizeRepositoryPath
} from "../../components/source/sourceUtils";
import type { SliceSocial } from "../../lib/socialMeta";
import { authStatusQuery } from "../../queries/auth";
import {
  globalRefQuery,
  sliceDirectoryQuery,
  sliceFileQuery,
  slicePathQuery,
  sliceRefKey,
  sliceRefQuery,
  sliceRenderedFileQuery,
  sliceRenderedFileQueryKey
} from "../../queries/slices";
import { lookupOrNotFound } from "../layouts";
import { findNestedReadme, nestedReadmeQueryKey, onlyDirectory } from "../slice-detail/nestedReadme";
import { findReadmeEntry, README_MAX_BYTES } from "../slice-detail/readme";
import { initialTreeExpansion } from "../slice-detail/sourceTree";

// Upper bound on how long SSR waits for path-specific prefetches. Anything that
// finishes in time is hydrated; the rest loads client-side as before, so a slow
// API can delay the first byte by at most this much.
export const SLICE_PATH_PREFETCH_BUDGET_MS = 1200;

export function sliceSocial(
  slice: Slice,
  params: { account: string; slice: string },
  path: string,
  origin: string
): SliceSocial {
  return {
    account: slice.ref?.account || params.account,
    slice: slice.ref?.slice || params.slice,
    description: slice.description ?? "",
    path,
    origin
  };
}

export interface PrefetchSliceDetailOptions {
  queryClient: QueryClient;
  api: ApiClient;
  signedIn: boolean;
  account: string;
  slice: string;
  selectedPath: string;
  origin: string;
  lookup?: <T>(fn: () => Promise<T>) => Promise<T>;
  budgetMs?: number;
}

export async function prefetchSliceDetailSSR({
  queryClient,
  api,
  signedIn,
  account,
  slice: sliceName,
  selectedPath,
  origin,
  lookup = lookupOrNotFound,
  budgetMs = SLICE_PATH_PREFETCH_BUDGET_MS
}: PrefetchSliceDetailOptions): Promise<SliceSocial | undefined> {
  const ref = { account, slice: sliceName };
  // A signed-in viewer's memberships decide what the page offers them
  // (editing, settings). Known in the server render, those parts are in
  // the page from the start instead of appearing after it loads and
  // pushing the README down.
  const memberships = signedIn
    ? queryClient.prefetchQuery(authStatusQuery(api))
    : Promise.resolve(undefined);

  // The default view resolves the slice and the latest global ref (which
  // yields the commit the file tree renders from); both fire on first
  // paint and have no derived inputs, so prefetch them together.
  const [slice, latest] = await Promise.all([
    lookup(() => queryClient.ensureQueryData(sliceRefQuery(api, account, sliceName))),
    queryClient.ensureQueryData(globalRefQuery(api))
  ]);
  await memberships.catch(() => undefined);

  const social = sliceSocial(slice, { account, slice: sliceName }, selectedPath, origin);
  const commitId = latest.commitId ?? "";
  const sliceRef = slice.ref ?? ref;
  const includedPaths = slice.definition?.includedPaths ?? [];

  if (!commitId) {
    return social;
  }

  // Without this, the browser discovers the path's kind, then its
  // listing, then the navigator's ancestor listings, one round trip at
  // a time. Prefetch them here with the page's own query keys.
  const listing = (path: string) =>
    queryClient.ensureQueryData(
      sliceDirectoryQuery(api, sliceRef, commitId, path, {
        allowMissingDirectory: isSliceProjectionDirectoryPath(path, includedPaths)
      })
    );

  // A slice of one folder nearly always shows that folder's README.md, so
  // ask for it alongside the listings; they still decide, and a wrong guess is dropped.
  const guessedReadmePath =
    !selectedPath && includedPaths.length === 1
      ? `${normalizeRepositoryPath(includedPaths[0]).replace(/\/$/, "")}/README.md`
      : "";
  const guessedReadmeKey = sliceRenderedFileQueryKey(sliceRef, commitId, guessedReadmePath);
  if (guessedReadmePath) {
    void queryClient.prefetchQuery(
      sliceRenderedFileQuery(api, sliceRef, commitId, guessedReadmePath)
    );
  }
  const dropGuessedReadme = () => {
    if (guessedReadmePath) {
      queryClient.removeQueries({ exact: true, queryKey: guessedReadmeKey });
    }
  };

  // A folder's README is part of its page: find it as the page does
  // (through single-folder levels), and ask the server for it
  // rendered, so the page is served with it.
  const readmeFor = async (path: string) => {
    const entries = await listing(path);
    const readme = await findNestedReadme(entries, listing);
    const only = findReadmeEntry(entries) ? undefined : onlyDirectory(entries);
    if (only) {
      queryClient.setQueryData(
        nestedReadmeQueryKey(sliceRef, commitId, normalizeRepositoryPath(only.path ?? "")),
        readme ?? null
      );
    }
    if (!readme || Number(readme.size) > README_MAX_BYTES) {
      dropGuessedReadme();
      return;
    }
    const readmePath = normalizeRepositoryPath(readme.path ?? "");
    if (readmePath !== guessedReadmePath) {
      dropGuessedReadme();
    }
    await queryClient.ensureQueryData(
      sliceRenderedFileQuery(api, sliceRef, commitId, readmePath)
    );
  };

  const navigatorPaths = new Set(initialTreeExpansion(selectedPath, includedPaths, false));
  const work: Promise<unknown>[] = Array.from(navigatorPaths, listing);

  if (selectedPath && !isSliceProjectionDirectoryPath(selectedPath, includedPaths)) {
    if (/[^/]\.[^/]+$/.test(selectedPath)) {
      void queryClient.prefetchQuery(sliceFileQuery(api, sliceRef, commitId, selectedPath));
    } else {
      void listing(selectedPath).catch(() => undefined);
    }
    work.push(
      queryClient
        .ensureQueryData(slicePathQuery(api, sliceRef, commitId, selectedPath))
        .then((resolved): Promise<unknown> | undefined => {
          const kind = resolved.entry?.kind;
          if (kind === "ENTRY_KIND_DIRECTORY") {
            return readmeFor(selectedPath);
          }
          if (kind === "ENTRY_KIND_FILE") {
            return queryClient.ensureQueryData(
              sliceFileQuery(api, sliceRef, commitId, selectedPath)
            );
          }
          return undefined;
        })
    );
  } else {
    work.push(readmeFor(selectedPath));
  }

  await Promise.race([
    Promise.allSettled(work),
    new Promise((resolve) => setTimeout(resolve, budgetMs))
  ]);

  return social;
}

export async function loadSliceDetail({
  context,
  deps,
  params
}: {
  context: { queryClient: QueryClient };
  deps: { path: string };
  params: { account: string; slice: string };
}): Promise<{ social: SliceSocial | undefined }> {
  let social: SliceSocial | undefined;
  if (!import.meta.env.SSR && params.account && params.slice) {
    const known = context.queryClient.getQueryData(sliceRefKey(params.account, params.slice));
    if (known) {
      social = sliceSocial(known, params, deps.path, window.location.origin);
    }
  }
  if (import.meta.env.SSR && params.account && params.slice) {
    try {
      const { createServerApiClient, isServerSignedIn } = await import("../../api/serverApi");
      const { getRequestUrl } = await import("@tanstack/react-start/server");
      const [api, signedIn] = await Promise.all([
        createServerApiClient(),
        isServerSignedIn()
      ]);
      social = await prefetchSliceDetailSSR({
        queryClient: context.queryClient,
        api,
        signedIn,
        account: params.account,
        slice: params.slice,
        selectedPath: deps.path,
        origin: getRequestUrl().origin
      });
    } catch (error) {
      // A 404 is the answer; any other failure leaves it to the component.
      if (isNotFound(error)) {
        throw error;
      }
    }
  }
  return { social };
}
