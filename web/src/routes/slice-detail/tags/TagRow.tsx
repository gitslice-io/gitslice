import { Link } from "@tanstack/react-router";
import { useId, useState } from "react";

import type { SliceRef, Tag } from "../../../api/types";
import { AuthorLabel } from "../../../components/AccountAvatar";
import { Timestamp } from "../../../components/Timestamp";
import {
  originSliceForTag,
  releaseHrefForTag,
  shortCommitHash,
  tagSummary,
} from "./tagUtils";

export function TagRow({
  sliceLabel,
  sliceRef,
  tag,
}: {
  sliceLabel: string;
  sliceRef: SliceRef | undefined;
  tag: Tag;
}) {
  const [expanded, setExpanded] = useState(false);
  const detailsId = useId();
  const name = tag.name ?? "";
  const author = tag.attribution?.actingUsername || tag.createdBy;
  const summary = tagSummary(name, tag.message ?? "");
  const sliceRouteParams =
    sliceRef?.account && sliceRef?.slice
      ? { account: sliceRef.account, slice: sliceRef.slice }
      : undefined;
  const origin = originSliceForTag(tag, sliceRef);
  const isDifferentSlice = Boolean(
    origin &&
      (origin.account !== sliceRef?.account || origin.slice !== sliceRef?.slice),
  );
  const showOriginBadge = Boolean(
    origin && (sliceRef?.slice === "home" || isDifferentSlice),
  );
  const release = releaseHrefForTag(
    origin ? { account: origin.account, slice: origin.slice } : sliceRef,
    name,
  );
  const commitShort = shortCommitHash(tag.commitId);
  const hasDetails = Boolean(
    summary || commitShort || author || isDifferentSlice || release,
  );

  return (
    <li className="py-2.5">
      <div className="flex items-center justify-between gap-2">
        <div className="flex min-w-0 items-baseline gap-2">
          {sliceRouteParams && tag.commitId ? (
            // Primary action: clicking the tag name opens the slice at that tag's commit.
            <Link
              className="truncate font-mono text-sm font-semibold text-zinc-950 underline-offset-2 transition hover:text-sky-700 hover:underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-600 dark:text-zinc-50 dark:hover:text-sky-300"
              params={sliceRouteParams}
              search={{ at: tag.commitId }}
              title={`Browse ${sliceLabel} at ${name}`}
              to="/slices/$account/$slice"
            >
              {name}
            </Link>
          ) : (
            <span
              className="truncate font-mono text-sm font-semibold text-zinc-950 dark:text-zinc-50"
              title={name}
            >
              {name}
            </span>
          )}
          {showOriginBadge && origin ? (
            <span className="shrink-0 rounded-full bg-slate-100 px-1.5 py-0.5 text-[11px] font-medium text-slate-500 dark:bg-zinc-800 dark:text-zinc-400">
              {origin.label}
            </span>
          ) : null}
        </div>

        <div className="flex shrink-0 items-center gap-0.5">
          <Timestamp
            className="mr-1 text-xs tabular-nums text-slate-400 dark:text-zinc-500"
            dateOnly
            value={tag.createdAt}
          />
          {release ? (
            <a
              aria-label={`Release files for ${name}`}
              className="inline-flex min-h-8 min-w-8 items-center justify-center rounded-md text-slate-400 transition hover:bg-slate-100 hover:text-sky-700 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-600 dark:text-zinc-500 dark:hover:bg-zinc-800 dark:hover:text-sky-300 [@media(pointer:coarse)]:min-h-11 [@media(pointer:coarse)]:min-w-11"
              href={release}
              title={`Release files for ${name}`}
            >
              <svg
                aria-hidden="true"
                className="h-4 w-4"
                fill="none"
                stroke="currentColor"
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth="1.75"
                viewBox="0 0 24 24"
              >
                <path d="M12 3v12" />
                <path d="m7 10 5 5 5-5" />
                <path d="M5 21h14" />
              </svg>
            </a>
          ) : null}
          {hasDetails ? (
            <button
              aria-controls={detailsId}
              aria-expanded={expanded}
              aria-label={`Details for ${name}`}
              className="inline-flex min-h-8 min-w-8 items-center justify-center rounded-md text-slate-400 transition hover:bg-slate-100 hover:text-zinc-700 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-600 dark:text-zinc-500 dark:hover:bg-zinc-800 dark:hover:text-zinc-200 [@media(pointer:coarse)]:min-h-11 [@media(pointer:coarse)]:min-w-11"
              onClick={() => setExpanded((value) => !value)}
              type="button"
            >
              <svg
                aria-hidden="true"
                className={[
                  "h-4 w-4 transition-transform duration-150",
                  expanded ? "rotate-180" : "",
                ].join(" ")}
                fill="none"
                stroke="currentColor"
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth="1.75"
                viewBox="0 0 24 24"
              >
                <path d="m6 9 6 6 6-6" />
              </svg>
            </button>
          ) : null}
        </div>
      </div>

      {summary && !expanded ? (
        <button
          aria-controls={detailsId}
          aria-expanded={false}
          className="mt-0.5 block w-full truncate text-left text-[13px] leading-5 text-slate-600 transition hover:text-zinc-950 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-600 dark:text-zinc-400 dark:hover:text-zinc-200"
          onClick={() => setExpanded(true)}
          type="button"
        >
          {summary}
        </button>
      ) : null}

      {expanded ? (
        <div className="mt-2 space-y-2" id={detailsId}>
          {summary ? (
            <p className="whitespace-pre-line text-pretty text-[13px] leading-5 text-slate-700 dark:text-zinc-300">
              {summary}
            </p>
          ) : null}
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 rounded-md bg-slate-50 px-2.5 py-2 text-xs text-slate-500 dark:bg-zinc-800/60 dark:text-zinc-400">
            {commitShort ? (
              sliceRouteParams && tag.commitId ? (
                <Link
                  className="font-mono text-slate-700 underline-offset-2 hover:text-sky-700 hover:underline dark:text-zinc-300 dark:hover:text-sky-300"
                  params={sliceRouteParams}
                  search={{ at: tag.commitId }}
                  title={tag.commitId}
                  to="/slices/$account/$slice"
                >
                  {commitShort}
                </Link>
              ) : (
                <span className="font-mono" title={tag.commitId}>
                  {commitShort}
                </span>
              )
            ) : null}
            {author ? <AuthorLabel className="min-w-0" name={author} /> : null}
            {isDifferentSlice && origin ? (
              <Link
                className="font-medium text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
                params={{ account: origin.account, slice: origin.slice }}
                search={tag.commitId ? { at: tag.commitId } : {}}
                to="/slices/$account/$slice"
              >
                Open {origin.label}
              </Link>
            ) : null}
            {release ? (
              <a
                className="font-medium text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
                href={release}
              >
                Release files
              </a>
            ) : null}
          </div>
        </div>
      ) : null}
    </li>
  );
}
