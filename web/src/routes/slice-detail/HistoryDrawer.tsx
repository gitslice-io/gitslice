import { useInfiniteQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useRef } from "react";

import type { SliceRef } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { shortChangesetId, shortHash } from "../../lib/objectId";
import { SliceNotice } from "../../components/slices/SlicePageParts";
import { AuthorLabel } from "../../components/AccountAvatar";
import { Timestamp } from "../../components/Timestamp";
import { Button } from "../../components/ui/Button";
import { Drawer } from "../../components/ui/Drawer";
import { sliceCommitsInfiniteQuery } from "../../queries/repository";

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
    ...sliceCommitsInfiniteQuery(api, sliceId, commitId, selectedPath, sliceRef, HISTORY_PAGE_SIZE),
    enabled: Boolean(open && commitId && sliceRef?.account && sliceRef?.slice)
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
    <Drawer
      closeLabel="Close history"
      onClose={onClose}
      open={open}
      size="md"
      subtitle={
        <>
          <p className="truncate text-xs text-slate-500 dark:text-zinc-400">{sliceLabel}</p>
          <p className="mt-0.5 break-all font-mono text-xs text-slate-500 dark:text-zinc-400">
            {selectedPath || "Slice root"}
          </p>
        </>
      }
      title="History"
    >
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
                  <Button
                    className="px-3 py-1.5 font-semibold"
                    disabled={isFetchingNextPage}
                    onClick={() => void fetchNextPage()}
                    size="sm"
                    variant="secondary"
                  >
                    {isFetchingNextPage ? "Loading older commits…" : "Load older commits"}
                  </Button>
                </div>
              ) : commits.length > HISTORY_PAGE_SIZE ? (
                <p className="pt-3 text-center text-xs text-slate-500 dark:text-zinc-400">
                  That's the whole history.
                </p>
              ) : null}
            </div>
          )}
        </div>
    </Drawer>
  );
}
