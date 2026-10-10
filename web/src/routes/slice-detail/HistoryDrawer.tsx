import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

import type { SliceRef } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { shortChangesetId, shortHash } from "../../lib/objectId";
import { SliceNotice } from "../../components/slices/SlicePageParts";
import { GLOBAL_REF_NAME } from "../../lib/globalRef";
import { Timestamp } from "../../components/Timestamp";

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
  const commitsQuery = useQuery({
    enabled: Boolean(open && commitId && sliceRef?.account && sliceRef?.slice),
    queryKey: [
      "sliceCommits",
      sliceId,
      commitId,
      selectedPath,
      sliceRef?.account,
      sliceRef?.slice
    ],
    queryFn: () =>
      api.listCommits({
        refName: GLOBAL_REF_NAME,
        slice: sliceRef,
        path: selectedPath || undefined,
        limit: 50
      })
  });

  const commits = commitsQuery.data?.commits ?? [];
  const historyIsPending = commitsQuery.isPending;
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
          ) : commitsQuery.isError ? (
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
                const body = (
                  <>
                    <p className="text-sm font-medium leading-snug text-zinc-950 dark:text-zinc-50">
                      {summary}
                    </p>
                    <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-slate-500 dark:text-zinc-400">
                      {shortCommitId ? (
                        <span
                          className="font-mono text-xs text-slate-500 dark:text-zinc-400"
                          title={commit.id}
                        >
                          {shortCommitId}
                        </span>
                      ) : null}
                      {commit.author ? <span>{commit.author}</span> : null}
                      <Timestamp value={commit.createdAt} />
                      {handle ? (
                        <span className="ml-auto inline-flex items-center gap-1 rounded-md border border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-2 py-0.5 font-mono text-xs font-semibold text-slate-700 dark:text-zinc-300">
                          {handle}
                          <span aria-hidden="true">→</span>
                        </span>
                      ) : null}
                    </div>
                  </>
                );
                const cardClass =
                  "block rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-2.5 py-2";

                return handle ? (
                  <Link
                    aria-label={`${summary}: open changeset ${handle}`}
                    className={`${cardClass} transition hover:border-slate-300 hover:bg-slate-50 dark:hover:border-zinc-700 dark:hover:bg-zinc-950`}
                    key={commit.id ?? summary}
                    params={{ id: handle }}
                    search={(focusFile ? { file: focusFile } : {}) as never}
                    to="/cs/$id"
                  >
                    {body}
                  </Link>
                ) : (
                  <div className={cardClass} key={commit.id ?? `${summary}-${commit.createdAt ?? ""}`}>
                    {body}
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
