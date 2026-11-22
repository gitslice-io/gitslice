import { useInfiniteQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useId, useRef } from "react";

import type { ListCommitsResponse, SliceRef } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { shortChangesetId, shortHash } from "../../lib/objectId";
import { SliceNotice } from "../../components/slices/SlicePageParts";
import { GLOBAL_REF_NAME } from "../../lib/globalRef";
import { AuthorLabel } from "../../components/AccountAvatar";
import { Timestamp } from "../../components/Timestamp";

const HISTORY_PAGE_SIZE = 50;

interface HistoryDrawerProps {
  api: ApiClient;
  commitId: string;
  onClose(): void;
  open: boolean;
  selectedPath: string;
  sliceId: string;
  sliceLabel: string;
  sliceRef: SliceRef | undefined;
}

export function HistoryDrawer({
  api,
  commitId,
  onClose,
  open,
  selectedPath,
  sliceId,
  sliceLabel,
  sliceRef
}: HistoryDrawerProps) {
  const commitsQuery = useInfiniteQuery({
    enabled: Boolean(open && commitId && sliceRef?.account && sliceRef?.slice),
    getNextPageParam: (last: ListCommitsResponse) => last.nextPageToken || undefined,
    initialPageParam: "",
    queryKey: [
      "sliceCommits",
      sliceId,
      commitId,
      selectedPath,
      sliceRef?.account,
      sliceRef?.slice
    ],
    queryFn: ({ pageParam }) =>
      api.listCommits({
        refName: GLOBAL_REF_NAME,
        slice: sliceRef,
        path: selectedPath || undefined,
        limit: HISTORY_PAGE_SIZE,
        ...(pageParam ? { pageToken: pageParam } : {})
      })
  });

  const commits = (commitsQuery.data?.pages ?? []).flatMap((page) => page.commits ?? []);
  const historyIsPending = commitsQuery.isPending;
  const { fetchNextPage, hasNextPage, isFetchingNextPage } = commitsQuery;

  // Older commits load when the end of the list scrolls into view, and from
  // the button for browsers without IntersectionObserver.
  const moreRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const sentinel = moreRef.current;
    if (!open || !sentinel || !hasNextPage || typeof IntersectionObserver === "undefined") {
      return;
    }
    const observer = new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting) && !isFetchingNextPage) {
        void fetchNextPage();
      }
    });
    observer.observe(sentinel);
    return () => observer.disconnect();
  }, [open, hasNextPage, isFetchingNextPage, fetchNextPage]);
  // Opening a commit's changeset from a file's history lands on that file's
  // diff. The diff ignores a path it does not show (a folder).
  const headingId = useId();
  const drawerRef = useRef<HTMLElement>(null);

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

  const focusFile = selectedPath.replace(/^\/+/, "");

  return (
    <>
      {open ? (
        <button
          aria-label="Close history"
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
          open ? "visible translate-x-0" : "invisible pointer-events-none translate-x-full"
        ].join(" ")}
        ref={drawerRef}
        role={open ? "dialog" : undefined}
        tabIndex={open ? -1 : undefined}
      >
        <div className="border-b border-slate-200 px-4 py-3 dark:border-zinc-800">
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0">
              <h2
                className="text-base font-semibold tracking-tight text-zinc-950 dark:text-zinc-50"
                id={headingId}
              >
                History
              </h2>
              <p className="truncate text-xs text-slate-500 dark:text-zinc-400">{sliceLabel}</p>
              <p className="mt-0.5 break-all font-mono text-xs text-slate-500 dark:text-zinc-400">
                {selectedPath || "Slice root"}
              </p>
            </div>
            <button
              aria-label="Close history"
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

        <div className="flex-1 overflow-y-auto px-4 pt-1 pb-[max(1rem,env(safe-area-inset-bottom,0px))]">
          {historyIsPending ? (
            <div className="mt-3 grid gap-2">
              <div className="h-12 animate-pulse rounded-md bg-slate-100 dark:bg-zinc-800" />
              <div className="h-12 animate-pulse rounded-md bg-slate-100 dark:bg-zinc-800" />
              <div className="h-12 animate-pulse rounded-md bg-slate-100 dark:bg-zinc-800" />
            </div>
          ) : commitsQuery.isError && !commitsQuery.data ? (
            <div className="mt-3">
              <SliceNotice title="Could not load history" tone="error">
                {commitsQuery.error?.message}
              </SliceNotice>
            </div>
          ) : commits.length === 0 ? (
            <div className="mt-3">
              <SliceNotice title="No history for this path yet.">
                No commits touch this path.
              </SliceNotice>
            </div>
          ) : (
            <div>
              <ul className="divide-y divide-slate-100 dark:divide-zinc-800/80">
                {commits.map((commit) => {
                  // The changeset that landed the commit, whichever slice
                  // authored it: the server says, so every commit that has a
                  // diff links to it.
                  const handle = shortChangesetId(commit.changesetId ?? "");
                  const summary =
                    commit.message?.split("\n")[0] || "(no message)";
                  const shortCommitId = shortHash(commit.id);
                  return (
                    <li
                      className="py-2.5"
                      key={commit.id ?? `${summary}-${commit.createdAt ?? ""}`}
                    >
                      {handle ? (
                        <Link
                          className="text-sm font-medium leading-snug text-zinc-950 underline-offset-2 transition hover:text-sky-700 hover:underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-600 dark:text-zinc-50 dark:hover:text-sky-300"
                          params={{ id: handle }}
                          search={(focusFile ? { file: focusFile } : {}) as never}
                          title={`Open changeset ${handle}`}
                          to="/cs/$id"
                        >
                          {summary}
                        </Link>
                      ) : (
                        <p className="text-sm font-medium leading-snug text-zinc-950 dark:text-zinc-50">
                          {summary}
                        </p>
                      )}
                      <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-slate-500 dark:text-zinc-400">
                        {shortCommitId ? (
                          <span
                            className="font-mono text-xs text-slate-500 dark:text-zinc-400"
                            title={commit.id}
                          >
                            {shortCommitId}
                          </span>
                        ) : null}
                        {commit.author ? <AuthorLabel name={commit.author} /> : null}
                        <Timestamp value={commit.createdAt} />
                      </div>
                    </li>
                  );
                })}
              </ul>
              {commitsQuery.isFetchNextPageError ? (
                <div className="mt-3">
                  <SliceNotice title="Could not load older commits" tone="error">
                    {commitsQuery.error?.message}
                  </SliceNotice>
                </div>
              ) : null}
              {hasNextPage ? (
                <div className="flex justify-center pt-3" ref={moreRef}>
                  <button
                    className="rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-1.5 text-xs font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98] disabled:cursor-not-allowed disabled:opacity-60"
                    disabled={isFetchingNextPage}
                    onClick={() => void fetchNextPage()}
                    type="button"
                  >
                    {isFetchingNextPage ? "Loading older commits…" : "Load older commits"}
                  </button>
                </div>
              ) : commits.length > HISTORY_PAGE_SIZE ? (
                <p className="pt-3 text-center text-xs text-slate-500 dark:text-zinc-400">
                  That's the whole history.
                </p>
              ) : null}
            </div>
          )}
        </div>
      </aside>
    </>
  );
}
