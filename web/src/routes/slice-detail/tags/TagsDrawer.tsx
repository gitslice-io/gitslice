import { useQuery } from "@tanstack/react-query";
import { useEffect, useId, useMemo, useState } from "react";

import type { SliceRef } from "../../../api/types";
import type { ApiClient } from "../../../api/useApi";
import { SliceNotice } from "../../../components/slices/SlicePageParts";
import { Button } from "../../../components/ui/Button";
import { Drawer } from "../../../components/ui/Drawer";
import { sliceTagsQuery } from "../../../queries/tags";
import { TagRow } from "./TagRow";
import {
  FILTER_INPUT_THRESHOLD,
  originSliceForTag,
  tagSummary,
} from "./tagUtils";

export interface TagsDrawerProps {
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
  const filterId = useId();
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
    <Drawer
      badge={
        tags.length > 0 ? (
          <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs font-medium tabular-nums text-slate-600 dark:bg-zinc-800 dark:text-zinc-400">
            {tags.length}
          </span>
        ) : null
      }
      closeLabel="Close tags"
      onClose={onClose}
      open={open}
      size="md"
      subtitle={
        <p className="truncate text-xs text-slate-500 dark:text-zinc-400">
          {sliceLabel}
        </p>
      }
      title="Tags"
    >
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
              <Button
                className="mt-2 px-3 py-1.5"
                onClick={() => {
                  setQuery("");
                  setSelectedSlice("");
                }}
                size="sm"
                variant="secondary"
              >
                Clear filters
              </Button>
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
    </Drawer>
  );
}
