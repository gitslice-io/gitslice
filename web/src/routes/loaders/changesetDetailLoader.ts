import { isNotFound } from "@tanstack/react-router";
import type { QueryClient } from "@tanstack/react-query";

import type { Changeset } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { shortChangesetId } from "../../lib/objectId";
import type { ChangesetSocial } from "../../lib/socialMeta";
import {
  changesetDiffQuery,
  changesetKey,
  changesetQuery,
  dependentChangesetsQuery
} from "../../queries/changesets";
import { checkRunsQuery, triggerRunsQuery } from "../../queries/checks";
import { isTerminalCheckStatus } from "../changeset-detail/checkStatus";
import { sortedPatchsets } from "../changeset-detail/patchsetUtils";
import { lookupOrNotFound } from "../layouts";

export function changesetSocial(
  changeset: Changeset,
  requestedId: string,
  origin: string
): ChangesetSocial {
  return {
    handle: shortChangesetId(changeset.id ?? "") || requestedId,
    title: changeset.title ?? "",
    description: changeset.description ?? "",
    status: changeset.status ?? "",
    author: changeset.author ?? "",
    account: changeset.authoringSlice?.account ?? "",
    slice: changeset.authoringSlice?.slice ?? "",
    fileCount: changeset.affectedPaths?.length ?? 0,
    origin
  };
}

export interface PrefetchChangesetDetailOptions {
  queryClient: QueryClient;
  api: ApiClient;
  signedIn: boolean;
  changesetId: string;
  from?: string;
  to?: string;
  origin: string;
  lookup?: <T>(fn: () => Promise<T>) => Promise<T>;
}

export async function prefetchChangesetDetailSSR({
  queryClient,
  api,
  signedIn,
  changesetId,
  from,
  to,
  origin,
  lookup = lookupOrNotFound
}: PrefetchChangesetDetailOptions): Promise<ChangesetSocial> {
  const changeset = await lookup(() =>
    queryClient.ensureQueryData(changesetQuery(api, changesetId))
  );
  const social = changesetSocial(changeset, changesetId, origin);

  const canonicalChangesetId = changeset.id || changesetId;
  const patchsets = sortedPatchsets(changeset);
  const ids = new Set(
    patchsets
      .map((patchset) => patchset.id)
      .filter((id): id is string => Boolean(id))
  );
  const fromPatchset = from && ids.has(from) ? from : "";
  const toPatchset =
    to && ids.has(to)
      ? to
      : changeset.currentPatchsetId || patchsets[patchsets.length - 1]?.id || "";
  const authoringSlice = changeset.authoringSlice;

  const panels: Promise<unknown>[] = [
    queryClient.prefetchQuery(triggerRunsQuery(api, canonicalChangesetId))
  ];
  if (signedIn && toPatchset) {
    const checkRuns = checkRunsQuery(api, canonicalChangesetId, toPatchset);
    const queryKey = checkRuns.queryKey;
    panels.push(
      queryClient.prefetchQuery(checkRuns).then(() => {
        // A run still going shows times that move ("last seen 5s ago");
        // leave non-terminal runs to the browser, which polls them anyway.
        const runs = queryClient.getQueryData(queryKey);
        if (runs?.some((run) => !isTerminalCheckStatus(run.status))) {
          queryClient.removeQueries({ exact: true, queryKey });
        }
      })
    );
  }

  await Promise.all([
    Promise.allSettled(panels),
    queryClient.ensureQueryData(
      changesetDiffQuery(api, canonicalChangesetId, fromPatchset, toPatchset)
    ),
    authoringSlice?.account && authoringSlice?.slice
      ? queryClient.ensureQueryData(
          dependentChangesetsQuery(api, authoringSlice, changeset.id ?? "")
        )
      : Promise.resolve(undefined)
  ]);

  return social;
}

export async function loadChangesetDetail({
  context,
  deps,
  params
}: {
  context: { queryClient: QueryClient };
  deps: { from?: string; to?: string };
  params: { id: string };
}): Promise<{ social: ChangesetSocial | undefined }> {
  let social: ChangesetSocial | undefined;
  if (!import.meta.env.SSR) {
    const known = context.queryClient.getQueryData(changesetKey(params.id));
    if (known) {
      social = changesetSocial(known, params.id, window.location.origin);
    }
  }
  if (import.meta.env.SSR) {
    try {
      const { createServerApiClient, isServerSignedIn } = await import("../../api/serverApi");
      const { getRequestUrl } = await import("@tanstack/react-start/server");
      const [api, signedIn] = await Promise.all([
        createServerApiClient(),
        isServerSignedIn()
      ]);
      social = await prefetchChangesetDetailSSR({
        queryClient: context.queryClient,
        api,
        signedIn,
        changesetId: params.id,
        from: deps.from,
        to: deps.to,
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
