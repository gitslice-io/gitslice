import { queryOptions, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

import type { SliceRef } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { AuthorLabel } from "../../components/AccountAvatar";
import { SliceNotice } from "../../components/slices/SlicePageParts";
import { Timestamp } from "../../components/Timestamp";
import { shortHash } from "../../lib/objectId";

// Release tags of Gitslice itself; their files are served under /releases.
const RELEASE_TAG = /^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/;

// releaseHrefForTag is the release page of a tag, for the gs CLI releases
// that gitslice/gitslice tags start. Other slices have no release files.
export function releaseHrefForTag(sliceRef: SliceRef | undefined, name: string): string | undefined {
  if (sliceRef?.account !== "gitslice" || sliceRef?.slice !== "gitslice" || !RELEASE_TAG.test(name)) {
    return undefined;
  }
  return `/releases/tag/${name}`;
}

// The slice's tags, shared by the drawer and the pinned-commit banner.
export function sliceTagsQuery(api: ApiClient, sliceId: string, sliceRef: SliceRef | undefined) {
  return queryOptions({
    queryKey: ["sliceTags", sliceId, sliceRef?.account, sliceRef?.slice],
    queryFn: () => api.listTags({ slice: sliceRef })
  });
}

interface TagsDrawerProps {
  api: ApiClient;
  onClose(): void;
  open: boolean;
  sliceId: string;
  sliceLabel: string;
  sliceRef: SliceRef | undefined;
}

export function TagsDrawer({ api, onClose, open, sliceId, sliceLabel, sliceRef }: TagsDrawerProps) {
  const tagsQuery = useQuery({
    ...sliceTagsQuery(api, sliceId, sliceRef),
    enabled: Boolean(open && sliceRef?.account && sliceRef?.slice)
  });
  const sliceRouteParams =
    sliceRef?.account && sliceRef?.slice ? { account: sliceRef.account, slice: sliceRef.slice } : undefined;
  // The server lists them newest first.
  const tags = tagsQuery.data?.tags ?? [];

  return (
    <>
      {open ? (
        <button
          aria-label="Close tags"
          className="fixed inset-0 z-40 bg-black/30"
          onClick={onClose}
          type="button"
        />
      ) : null}
      <aside
        aria-hidden={!open}
        className={[
          "fixed inset-y-0 right-0 z-50 flex w-full max-w-md transform flex-col bg-white dark:bg-zinc-900 shadow-xl transition-transform duration-200",
          open ? "translate-x-0" : "translate-x-full"
        ].join(" ")}
      >
        <div className="border-b border-slate-200 dark:border-zinc-800 px-4 py-4">
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0">
              <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">Tags</h2>
              <p className="mt-1 text-sm text-slate-600 dark:text-zinc-400">{sliceLabel}</p>
            </div>
            <button
              className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-2.5 py-1.5 text-xs font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
              onClick={onClose}
              type="button"
            >
              Close
            </button>
          </div>
        </div>

        <div className="flex-1 overflow-y-auto px-4 py-4">
          {open && tagsQuery.isPending ? (
            <div className="grid gap-1.5">
              <div className="h-11 animate-pulse rounded-md bg-slate-100 dark:bg-zinc-800" />
              <div className="h-11 animate-pulse rounded-md bg-slate-100 dark:bg-zinc-800" />
              <div className="h-11 animate-pulse rounded-md bg-slate-100 dark:bg-zinc-800" />
            </div>
          ) : tagsQuery.isError ? (
            <SliceNotice title="Could not load tags" tone="error">
              {tagsQuery.error?.message}
            </SliceNotice>
          ) : tags.length === 0 ? (
            <SliceNotice title="No tags yet.">
              Create one with <code className="font-mono">gs tag create &lt;name&gt;</code>.
            </SliceNotice>
          ) : (
            <div className="grid grid-cols-1 gap-1.5">
              {tags.map((tag) => {
                const name = tag.name ?? "";
                const author = tag.attribution?.actingUsername || tag.createdBy;
                const release = releaseHrefForTag(tag.slice ?? sliceRef, name);
                return (
                  <div
                    className="rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-2.5 py-2"
                    key={name}
                  >
                    <div className="flex items-baseline justify-between gap-2">
                      <p className="break-all font-mono text-sm font-semibold text-zinc-950 dark:text-zinc-50">
                        {name}
                      </p>
                      {release ? (
                        <a
                          className="shrink-0 text-xs font-medium text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
                          href={release}
                        >
                          Release files
                        </a>
                      ) : null}
                    </div>
                    {tag.message ? (
                      <p className="mt-0.5 whitespace-pre-line text-sm text-slate-700 dark:text-zinc-300">
                        {tag.message}
                      </p>
                    ) : null}
                    <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-slate-500 dark:text-zinc-400">
                      {sliceRouteParams && tag.commitId ? (
                        // Browse the slice as it was at the tag; the drawer
                        // closes because the new address has no ?tags.
                        <Link
                          className="font-mono text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
                          params={sliceRouteParams}
                          search={{ at: tag.commitId }}
                          title={`Browse ${name} (${tag.commitId})`}
                          to="/slices/$account/$slice"
                        >
                          {shortHash(tag.commitId)}
                        </Link>
                      ) : (
                        <span className="font-mono" title={tag.commitId}>
                          {shortHash(tag.commitId)}
                        </span>
                      )}
                      {author ? <AuthorLabel name={author} /> : null}
                      <Timestamp value={tag.createdAt} />
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </div>
      </aside>
    </>
  );
}
