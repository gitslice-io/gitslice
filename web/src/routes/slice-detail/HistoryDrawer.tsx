import { useInfiniteQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useRef } from "react";

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
  const focusFile = selectedPath.replace(/^\/+/, "");

  return (
    <>
      {open ? (
        <button
          aria-label="Close history"
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
              <h2 className="text-base font-semibold text-zinc-950 dark:text-zinc-50">
                History
              </h2>
              <p className="mt-1 text-sm text-slate-600 dark:text-zinc-400">{sliceLabel}</p>
              <p className="mt-1 break-all font-mono text-xs text-slate-500 dark:text-zinc-400">
                {selectedPath || "Slice root"}
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
          {historyIsPending ? (
            <div className="grid gap-1.5">
              <div className="h-11 animate-pulse rounded-md bg-slate-100 dark:bg-zinc-800" />
              <div className="h-11 animate-pulse rounded-md bg-slate-100 dark:bg-zinc-800" />
              <div className="h-11 animate-pulse rounded-md bg-slate-100 dark:bg-zinc-800" />
            </div>
          ) : commitsQuery.isError && !commitsQuery.data ? (
            <SliceNotice title="Could not load history" tone="error">
              {commitsQuery.error?.message}
            </SliceNotice>
          ) : commits.length === 0 ? (
            <SliceNotice title="No history for this path yet.">
              No commits touch this path.
            </SliceNotice>
          ) : (
            <div className="grid grid-cols-1 gap-1.5">
              {commits.map((commit) => {
                // The changeset that landed the commit, whichever slice
                // authored it: the server says, so every commit that has a
                // diff links to it.
                const handle = shortChangesetId(commit.changesetId ?? "");
                const summary =
                  commit.message?.split("\n")[0] || "(no message)";
                const shortCommitId = shortHash(commit.id);
                return (
                  <div
                    className="rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-2.5 py-2"
                    key={commit.id ?? `${summary}-${commit.createdAt ?? ""}`}
                  >
                    {handle ? (
                      <Link
                        className="text-sm font-medium leading-snug text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
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
                  </div>
                );
              })}
              {commitsQuery.isFetchNextPageError ? (
                <SliceNotice title="Could not load older commits" tone="error">
                  {commitsQuery.error?.message}
                </SliceNotice>
              ) : null}
              {hasNextPage ? (
                <div className="flex justify-center pt-1" ref={moreRef}>
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
                <p className="pt-1 text-center text-xs text-slate-500 dark:text-zinc-400">
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
