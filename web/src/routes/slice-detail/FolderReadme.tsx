import { useQuery } from "@tanstack/react-query";

import { useApi } from "../../api/useApi";
import type { SliceRef, TreeEntry } from "../../api/types";
import { MarkdownViewer } from "../../components/source/MarkdownViewer";
import { useRepoImageLoader } from "../../components/source/useRepoImageLoader";
import {
  entryDisplayName,
  formatSize,
  normalizeRepositoryPath
} from "../../components/source/sourceUtils";
import { projectRoot } from "../../lib/repoLinks";
import { sliceRenderedFileQueryKey } from "../../lib/sliceQueryKeys";
import { README_MAX_BYTES } from "./readme";

interface FolderReadmeProps {
  canEdit: boolean;
  commitId: string;
  entry: TreeEntry;
  includedPaths?: string[];
  // The heading, when the README is not the folder's own: its path from the
  // folder.
  label?: string;
  onSelectPath(path: string): void;
  sliceRef: SliceRef | undefined;
}

// FolderReadme is a folder's README, rendered as the folder's introduction.
export function FolderReadme({ canEdit, commitId, entry, includedPaths, label, onSelectPath, sliceRef }: FolderReadmeProps) {
  const api = useApi();
  const path = normalizeRepositoryPath(entry.path ?? "");
  const directory = path.slice(0, path.lastIndexOf("/"));
  const name = entryDisplayName(entry);
  const tooLarge = Number(entry.size) > README_MAX_BYTES;
  // Rendered and sanitized by the server, which the page's own server render
  // also asks for, so a README arrives with the page.
  const query = useQuery({
    enabled: Boolean(commitId && sliceRef?.account && sliceRef?.slice) && !tooLarge,
    queryKey: sliceRenderedFileQueryKey(sliceRef, commitId, path),
    queryFn: () => api.renderFile({ commitId, path, slice: sliceRef }),
    // A commit's file never changes.
    staleTime: Number.POSITIVE_INFINITY
  });
  const html = query.data?.html ?? "";
  const loadImage = useRepoImageLoader(commitId, sliceRef);

  let body;
  if (tooLarge) {
    body = (
      <p className="p-4 text-sm text-slate-600 dark:text-zinc-400">
        {name} is {formatSize(entry.size)}, too large to show here. Open it to read it.
      </p>
    );
  } else if (query.isError) {
    body = (
      <p className="p-4 text-sm text-rose-700 dark:text-rose-300" role="alert">
        Could not load {name}: {query.error instanceof Error ? query.error.message : String(query.error)}
      </p>
    );
  } else if (query.isPending) {
    body = (
      <div aria-label={`Loading ${name}`} className="grid gap-3 p-4">
        <div className="h-5 w-1/3 animate-pulse rounded bg-slate-200 dark:bg-zinc-700" />
        <div className="h-4 animate-pulse rounded bg-slate-100 dark:bg-zinc-800" />
        <div className="h-4 w-5/6 animate-pulse rounded bg-slate-100 dark:bg-zinc-800" />
        <div className="h-4 w-2/3 animate-pulse rounded bg-slate-100 dark:bg-zinc-800" />
      </div>
    );
  } else if (!html.trim()) {
    body = <p className="p-4 text-sm text-slate-600 dark:text-zinc-400">{name} is empty.</p>;
  } else {
    body = <MarkdownViewer html={html} links={{ directory, loadImage, onOpenPath: onSelectPath, root: projectRoot(includedPaths, path) }} />;
  }

  return (
    <section aria-label={label ?? name} className="min-w-0">
      <div className="flex items-center justify-between gap-3 border-b border-slate-200 dark:border-zinc-800 px-4 py-2.5 sm:px-5">
        <h3 className="min-w-0 truncate text-sm font-semibold text-zinc-950 dark:text-zinc-50" title={label ?? name}>{label ?? name}</h3>
        <button
          className="shrink-0 rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-2.5 py-1.5 text-xs font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
          onClick={() => onSelectPath(path)}
          type="button"
        >
          {canEdit ? "Edit" : "Open"}
        </button>
      </div>
      {body}
    </section>
  );
}
