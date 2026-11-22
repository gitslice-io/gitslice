import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useId, useMemo, useRef, useState } from "react";

import type { SliceRef, Tag } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { AuthorLabel } from "../../components/AccountAvatar";
import { SliceNotice } from "../../components/slices/SlicePageParts";
import { Timestamp } from "../../components/Timestamp";
import { sliceTagsQuery } from "../../queries/tags";

export { sliceTagsQuery };

// Release tags of Gitslice itself; their files are served under /releases.
const RELEASE_TAG = /^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/;
const FILTER_INPUT_THRESHOLD = 5;

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

function shortCommitHash(commitId: string | undefined): string {
  if (!commitId) {
    return "";
  }
  return commitId.replace(/^sha256:/, "").slice(0, 7);
}

function originSliceForTag(
  tag: Tag,
  sliceRef: SliceRef | undefined,
): { account: string; slice: string; label: string } | undefined {
  const account = tag.slice?.account;
  const slice = tag.slice?.slice;
  if (!account || !slice) {
    return undefined;
  }
  const label = account === sliceRef?.account ? slice : `${account}/${slice}`;
  return { account, slice, label };
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
  const headingId = useId();
  const filterId = useId();
  const drawerRef = useRef<HTMLElement>(null);
  const [query, setQuery] = useState("");
  const [selectedSlice, setSelectedSlice] = useState("");

  const tagsQuery = useQuery({
    ...sliceTagsQuery(api, sliceId, sliceRef),
    enabled: Boolean(open && sliceRef?.account && sliceRef?.slice),
  });
  // The server lists them newest first.
  const tags = tagsQuery.data?.tags ?? [];

  // Reset filters when switching slices.
  useEffect(() => {
    setQuery("");
    setSelectedSlice("");
  }, [sliceId, sliceRef?.account, sliceRef?.slice]);

  // Keep closed drawer inert (non-focusable and hidden from AT) instead of
  // applying aria-hidden over focusable controls, and move focus in on open.
  useEffect(() => {
    const drawer = drawerRef.current;
    if (!drawer) {
      return;
    }
    drawer.inert = !open;
    if (open && !drawer.contains(document.activeElement)) {
      drawer.focus({ preventScroll: true });
    }
  }, [open]);

  // Dismiss with Escape while open.
  useEffect(() => {
    if (!open) {
      return;
    }
    function handleKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") {
        onClose();
      }
    }
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [open, onClose]);

  const sliceOptions = useMemo(() => {
    if (sliceRef?.slice !== "home") {
      return [];
    }
    const counts = new Map<string, { label: string; count: number }>();
    for (const tag of tags) {
      const origin = originSliceForTag(tag, sliceRef);
      if (!origin) {
        continue;
      }
      const key = `${origin.account}/${origin.slice}`;
      const existing = counts.get(key);
      if (existing) {
        existing.count += 1;
      } else {
        counts.set(key, { label: origin.label, count: 1 });
      }
    }
    return Array.from(counts.entries()).map(([key, value]) => ({
      key,
      label: value.label,
      count: value.count,
    }));
  }, [sliceRef, tags]);

  const activeSliceFilter = sliceOptions.some((o) => o.key === selectedSlice)
    ? selectedSlice
    : "";

  const normalizedQuery = query.trim().toLowerCase();
  const filteredTags = useMemo(() => {
    if (!normalizedQuery && !activeSliceFilter) {
      return tags;
    }
    return tags.filter((tag) => {
      if (activeSliceFilter) {
        const key =
          tag.slice?.account && tag.slice?.slice
            ? `${tag.slice.account}/${tag.slice.slice}`
            : "";
        if (key !== activeSliceFilter) {
          return false;
        }
      }
      if (!normalizedQuery) {
        return true;
      }
      const name = (tag.name ?? "").toLowerCase();
      const summary = tagSummary(tag.name ?? "", tag.message ?? "").toLowerCase();
      const sliceSlug = (tag.slice?.slice ?? "").toLowerCase();
      return (
        name.includes(normalizedQuery) ||
        summary.includes(normalizedQuery) ||
        sliceSlug.includes(normalizedQuery)
      );
    });
  }, [activeSliceFilter, normalizedQuery, tags]);

  const showFilterInput = tags.length > FILTER_INPUT_THRESHOLD;
  const showSliceFilters = sliceOptions.length > 1;
  const hasFilters = Boolean(normalizedQuery || activeSliceFilter);

  return (
    <>
      {open ? (
        <button
          aria-label="Close tags"
          className="fixed inset-0 z-40 bg-black/30"
          onClick={onClose}
          tabIndex={-1}
          type="button"
        />
      ) : null}
      <aside
        aria-hidden={!open}
        aria-labelledby={headingId}
        aria-modal={open ? true : undefined}
        className={[
          "fixed inset-y-0 right-0 z-50 flex w-full max-w-md transform flex-col bg-white outline-none shadow-xl transition-[transform,visibility] duration-200 dark:bg-zinc-900",
          open ? "visible translate-x-0" : "invisible pointer-events-none translate-x-full",
        ].join(" ")}
        ref={drawerRef}
        role={open ? "dialog" : undefined}
        tabIndex={open ? -1 : undefined}
      >
        <div className="border-b border-slate-200 px-4 py-3 dark:border-zinc-800">
          <div className="flex items-center justify-between gap-3">
            <div className="min-w-0">
              <div className="flex items-center gap-2">
                <h2
                  className="text-base font-semibold tracking-tight text-zinc-950 dark:text-zinc-50"
                  id={headingId}
                >
                  Tags
                </h2>
                {tags.length > 0 ? (
                  <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs font-medium tabular-nums text-slate-600 dark:bg-zinc-800 dark:text-zinc-400">
                    {tags.length}
                  </span>
                ) : null}
              </div>
              <p className="truncate text-xs text-slate-500 dark:text-zinc-400">
                {sliceLabel}
              </p>
            </div>
            <button
              aria-label="Close tags"
              className="inline-flex min-h-9 min-w-9 shrink-0 items-center justify-center rounded-md text-slate-500 transition hover:bg-slate-100 hover:text-zinc-950 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-600 active:scale-[0.98] dark:text-zinc-400 dark:hover:bg-zinc-800 dark:hover:text-zinc-50 [@media(pointer:coarse)]:min-h-11 [@media(pointer:coarse)]:min-w-11"
              onClick={onClose}
              type="button"
            >
              <svg
                aria-hidden="true"
                className="h-4 w-4"
                fill="none"
                stroke="currentColor"
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth="2"
                viewBox="0 0 24 24"
              >
                <path d="M18 6 6 18" />
                <path d="m6 6 12 12" />
              </svg>
            </button>
          </div>
        </div>

        {showFilterInput || showSliceFilters ? (
          <div className="space-y-2 border-b border-slate-200 bg-slate-50/70 px-4 py-2.5 dark:border-zinc-800 dark:bg-zinc-950/40">
            {showFilterInput ? (
              <div className="relative">
                <label className="sr-only" htmlFor={filterId}>
                  Filter tags
                </label>
                <svg
                  aria-hidden="true"
                  className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-slate-400 dark:text-zinc-500"
                  fill="none"
                  stroke="currentColor"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth="2"
                  viewBox="0 0 24 24"
                >
                  <circle cx="11" cy="11" r="8" />
                  <path d="m21 21-4.3-4.3" />
                </svg>
                <input
                  className="min-h-9 w-full rounded-md border border-slate-200 bg-white py-1.5 pl-8 pr-8 text-sm text-zinc-950 placeholder:text-slate-400 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-0 focus-visible:outline-sky-600 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-50 dark:placeholder:text-zinc-500 [@media(pointer:coarse)]:min-h-11"
                  id={filterId}
                  onChange={(event) => setQuery(event.target.value)}
                  placeholder="Filter tags…"
                  type="search"
                  value={query}
                />
                {query ? (
                  <button
                    aria-label="Clear tag filter"
                    className="absolute right-1 top-1/2 inline-flex min-h-7 min-w-7 -translate-y-1/2 items-center justify-center rounded text-slate-400 hover:text-zinc-700 focus-visible:outline focus-visible:outline-2 focus-visible:outline-sky-600 dark:text-zinc-500 dark:hover:text-zinc-200"
                    onClick={() => setQuery("")}
                    type="button"
                  >
                    <svg
                      aria-hidden="true"
                      className="h-3.5 w-3.5"
                      fill="none"
                      stroke="currentColor"
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      strokeWidth="2"
                      viewBox="0 0 24 24"
                    >
                      <path d="M18 6 6 18" />
                      <path d="m6 6 12 12" />
                    </svg>
                  </button>
                ) : null}
              </div>
            ) : null}

            {showSliceFilters ? (
              <div
                aria-label="Filter by slice"
                className="flex flex-wrap gap-1.5"
                role="group"
              >
                <button
                  aria-pressed={activeSliceFilter === ""}
                  className={[
                    "min-h-7 rounded-full px-2.5 py-0.5 text-xs font-medium transition focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-600 [@media(pointer:coarse)]:min-h-9",
                    activeSliceFilter === ""
                      ? "bg-zinc-900 text-white dark:bg-zinc-100 dark:text-zinc-950"
                      : "bg-white text-slate-600 ring-1 ring-inset ring-slate-200 hover:text-zinc-950 dark:bg-zinc-900 dark:text-zinc-300 dark:ring-zinc-700 dark:hover:text-zinc-50",
                  ].join(" ")}
                  onClick={() => setSelectedSlice("")}
                  type="button"
                >
                  All
                </button>
                {sliceOptions.map((option) => {
                  const active = activeSliceFilter === option.key;
                  return (
                    <button
                      aria-pressed={active}
                      className={[
                        "min-h-7 rounded-full px-2.5 py-0.5 font-mono text-xs transition focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-600 [@media(pointer:coarse)]:min-h-9",
                        active
                          ? "bg-zinc-900 text-white dark:bg-zinc-100 dark:text-zinc-950"
                          : "bg-white text-slate-600 ring-1 ring-inset ring-slate-200 hover:text-zinc-950 dark:bg-zinc-900 dark:text-zinc-300 dark:ring-zinc-700 dark:hover:text-zinc-50",
                      ].join(" ")}
                      key={option.key}
                      onClick={() =>
                        setSelectedSlice((prev) =>
                          prev === option.key ? "" : option.key,
                        )
                      }
                      type="button"
                    >
                      {option.label}{" "}
                      <span className="opacity-65 tabular-nums">
                        {option.count}
                      </span>
                    </button>
                  );
                })}
              </div>
            ) : null}
          </div>
        ) : null}

        <div className="flex-1 overflow-y-auto px-4 pt-1 pb-[max(1rem,env(safe-area-inset-bottom,0px))]">
          {open && tagsQuery.isPending ? (
            <div className="mt-3 grid gap-2">
              <div className="h-12 animate-pulse rounded-md bg-slate-100 dark:bg-zinc-800" />
              <div className="h-12 animate-pulse rounded-md bg-slate-100 dark:bg-zinc-800" />
              <div className="h-12 animate-pulse rounded-md bg-slate-100 dark:bg-zinc-800" />
            </div>
          ) : tagsQuery.isError ? (
            <div className="mt-3">
              <SliceNotice title="Could not load tags" tone="error">
                {tagsQuery.error?.message}
              </SliceNotice>
            </div>
          ) : tags.length === 0 ? (
            <div className="mt-3">
              <SliceNotice title="No tags yet.">
                Create one with{" "}
                <code className="font-mono">gs tag create &lt;name&gt;</code>.
              </SliceNotice>
            </div>
          ) : filteredTags.length === 0 ? (
            <div className="mt-6 text-center">
              <p className="text-sm text-slate-600 dark:text-zinc-400">
                No matching tags.
              </p>
              {hasFilters ? (
                <button
                  className="mt-2 rounded-md border border-slate-300 bg-white px-3 py-1.5 text-xs font-medium text-slate-700 transition hover:bg-slate-50 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-300 dark:hover:bg-zinc-800"
                  onClick={() => {
                    setQuery("");
                    setSelectedSlice("");
                  }}
                  type="button"
                >
                  Clear filters
                </button>
              ) : null}
            </div>
          ) : (
            <ul className="divide-y divide-slate-100 dark:divide-zinc-800/80">
              {filteredTags.map((tag) => (
                <TagRow
                  key={`${tag.slice?.slice ?? ""}/${tag.name ?? ""}`}
                  sliceLabel={sliceLabel}
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
