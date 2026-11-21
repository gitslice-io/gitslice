import { queryOptions, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";

import type { SliceRef, Tag } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { AuthorLabel } from "../../components/AccountAvatar";
import { SliceNotice } from "../../components/slices/SlicePageParts";
import { Timestamp } from "../../components/Timestamp";
import { shortHash } from "../../lib/objectId";

// Release tags of Gitslice itself; their files are served under /releases.
const RELEASE_TAG = /^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/;

// releaseHrefForTag is the release page of a tag, for the gs CLI releases
// that gitslice/gitslice tags start. Other slices have no release files.
export function releaseHrefForTag(
  sliceRef: SliceRef | undefined,
  name: string,
): string | undefined {
  if (
    sliceRef?.account !== "gitslice" ||
    sliceRef?.slice !== "gitslice" ||
    !RELEASE_TAG.test(name)
  ) {
    return undefined;
  }
  return `/releases/tag/${name}`;
}

// The slice's tags, shared by the drawer and the pinned-commit banner.
export function sliceTagsQuery(
  api: ApiClient,
  sliceId: string,
  sliceRef: SliceRef | undefined,
) {
  return queryOptions({
    queryKey: ["sliceTags", sliceId, sliceRef?.account, sliceRef?.slice],
    queryFn: () => api.listTags({ slice: sliceRef }),
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

export function TagsDrawer({
  api,
  onClose,
  open,
  sliceId,
  sliceLabel,
  sliceRef,
}: TagsDrawerProps) {
  const tagsQuery = useQuery({
    ...sliceTagsQuery(api, sliceId, sliceRef),
    enabled: Boolean(open && sliceRef?.account && sliceRef?.slice),
  });
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
          open ? "translate-x-0" : "translate-x-full",
        ].join(" ")}
      >
        <div className="border-b border-slate-200 dark:border-zinc-800 px-4 py-3">
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0">
              <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">
                Tags
              </h2>
              <p className="text-xs text-slate-500 dark:text-zinc-400">
                {sliceLabel}
              </p>
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
              Create one with{" "}
              <code className="font-mono">gs tag create &lt;name&gt;</code>.
            </SliceNotice>
          ) : (
            <ul className="-my-2 divide-y divide-slate-200 dark:divide-zinc-800">
              {tags.map((tag) => (
                <TagRow
                  key={`${tag.slice?.slice ?? ""}/${tag.name ?? ""}`}
                  sliceRef={sliceRef}
                  tag={tag}
                />
              ))}
            </ul>
          )}
        </div>
      </aside>
    </>
  );
}

// tagSummary drops a leading "v1.2.3:" (or "gs v1.2.3:") from a tag's message,
// since the row already shows the name.
export function tagSummary(name: string, message: string): string {
  const text = message.trim();
  for (const prefix of [`${name}:`, `gs ${name}:`]) {
    if (name && text.startsWith(prefix)) {
      return text.slice(prefix.length).trim();
    }
  }
  return text;
}

function TagRow({
  sliceRef,
  tag,
}: {
  sliceRef: SliceRef | undefined;
  tag: Tag;
}) {
  const [expanded, setExpanded] = useState(false);
  const name = tag.name ?? "";
  const author = tag.attribution?.actingUsername || tag.createdBy;
  const summary = tagSummary(name, tag.message ?? "");
  const sliceRouteParams =
    sliceRef?.account && sliceRef?.slice
      ? { account: sliceRef.account, slice: sliceRef.slice }
      : undefined;
  const tagSlice =
    tag.slice?.account && tag.slice?.slice
      ? { account: tag.slice.account, slice: tag.slice.slice }
      : undefined;
  const showOriginSlice = Boolean(
    tagSlice &&
    (sliceRef?.slice === "home" ||
      tagSlice.account !== sliceRef?.account ||
      tagSlice.slice !== sliceRef?.slice),
  );
  const originSliceLabel = tagSlice
    ? tagSlice.account === sliceRef?.account
      ? tagSlice.slice
      : `${tagSlice.account}/${tagSlice.slice}`
    : "";
  const release = releaseHrefForTag(tagSlice ?? sliceRef, name);
  return (
    <li className="py-2.5">
      <div className="flex items-baseline justify-between gap-3">
        <div className="flex min-w-0 items-baseline gap-2">
          <span
            className="truncate font-mono text-sm font-semibold text-zinc-950 dark:text-zinc-50"
            title={name}
          >
            {name}
          </span>
          {showOriginSlice && tagSlice ? (
            <Link
              className="shrink-0 rounded bg-slate-100 px-1 font-mono text-[11px] text-slate-600 transition hover:bg-slate-200 hover:text-zinc-950 dark:bg-zinc-800 dark:text-zinc-300 dark:hover:bg-zinc-700 dark:hover:text-zinc-50"
              params={{ account: tagSlice.account, slice: tagSlice.slice }}
              search={tag.commitId ? { at: tag.commitId } : {}}
              title={`Open ${tagSlice.account}/${tagSlice.slice}${tag.commitId ? ` at ${name}` : ""}`}
              to="/slices/$account/$slice"
            >
              {originSliceLabel}
            </Link>
          ) : null}
        </div>
        <Timestamp
          className="shrink-0 text-xs text-slate-500 dark:text-zinc-400"
          dateOnly
          value={tag.createdAt}
        />
      </div>
      {summary ? (
        // One line until tapped; long release notes otherwise fill the drawer.
        <button
          aria-expanded={expanded}
          className={[
            "mt-0.5 block w-full text-left text-[13px] leading-5 text-slate-600 dark:text-zinc-400",
            expanded ? "whitespace-pre-line" : "truncate",
          ].join(" ")}
          onClick={() => setExpanded((value) => !value)}
          title={expanded ? undefined : summary}
          type="button"
        >
          {summary}
        </button>
      ) : null}
      <div className="mt-0.5 flex min-w-0 items-center gap-x-1.5 text-xs text-slate-500 dark:text-zinc-400">
        {sliceRouteParams && tag.commitId ? (
          // Browse the slice as it was at the tag; the drawer closes because
          // the new address has no ?tags.
          <Link
            className="shrink-0 font-mono text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
            params={sliceRouteParams}
            search={{ at: tag.commitId }}
            title={`Browse ${name} (${tag.commitId})`}
            to="/slices/$account/$slice"
          >
            {shortHash(tag.commitId)}
          </Link>
        ) : (
          <span className="shrink-0 font-mono" title={tag.commitId}>
            {shortHash(tag.commitId)}
          </span>
        )}
        {author ? (
          <>
            <span aria-hidden="true">·</span>
            <AuthorLabel className="min-w-0" name={author} />
          </>
        ) : null}
        {release ? (
          <>
            <span aria-hidden="true">·</span>
            <a
              className="shrink-0 font-medium text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
              href={release}
            >
              Release files
            </a>
          </>
        ) : null}
      </div>
    </li>
  );
}
