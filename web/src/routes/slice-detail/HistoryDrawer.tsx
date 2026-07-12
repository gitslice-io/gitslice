import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

import type { Changeset, SliceRef } from "../../api/types";
import type { ApiClient } from "../../api/useApi";
import { shortChangesetId, shortHash } from "../../lib/objectId";
import { SliceNotice } from "../../components/slices/SlicePageParts";
import { GLOBAL_REF_NAME } from "../../lib/globalRef";

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

  const changesetsQuery = useQuery({
    enabled: Boolean(open && sliceRef?.account && sliceRef?.slice),
    queryKey: ["sliceHistoryChangesets", sliceRef?.account, sliceRef?.slice],
    queryFn: () =>
      api.listChangesets({
        authoringSlice: sliceRef,
        limit: 200
      })
  });

  const changesetByCommit = (() => {
    const next = new Map<string, Changeset>();
    if (changesetsQuery.isError) {
      return next;
    }
    for (const changeset of changesetsQuery.data?.changesets ?? []) {
      if (changeset.commitId) {
        next.set(changeset.commitId, changeset);
      }
    }
    return next;
  })();

  const commits = commitsQuery.data?.commits ?? [];
  const historyIsPending = commitsQuery.isPending || changesetsQuery.isPending;

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
                const changeset = commit.id
                  ? changesetByCommit.get(commit.id)
                  : undefined;
                const summary =
                  commit.message?.split("\n")[0] || "(no message)";
                const shortCommitId = shortHash(commit.id);

                return (
                  <div
                    className="rounded-md border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 px-2.5 py-2"
                    key={commit.id ?? `${summary}-${commit.createdAt ?? ""}`}
                  >
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
                      {commit.createdAt ? <span>{commit.createdAt}</span> : null}
                      {changeset ? (
                        <Link
                          className="inline-flex rounded-md border border-slate-200 dark:border-zinc-800 bg-slate-50 dark:bg-zinc-950 px-2 py-0.5 text-xs font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-white dark:hover:bg-zinc-900 hover:text-zinc-950 dark:hover:text-zinc-50"
                          params={{
                            id: shortChangesetId(changeset.id ?? "") || ""
                          }}
                          to="/cs/$id"
                        >
                          {shortChangesetId(changeset.id ?? "") ||
                            (changeset.number
                              ? `#${changeset.number}`
                              : changeset.id)}
                        </Link>
                      ) : null}
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
