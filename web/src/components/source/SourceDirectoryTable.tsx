import { Link } from "@tanstack/react-router";

import type { TreeEntry } from "../../api/types";
import {
  entryDisplayName,
  entryKindLabel,
  formatMode,
  formatSize,
  repositoryPathToRoutePath,
  sortEntries,
  trimMiddle
} from "./sourceUtils";

interface SourceDirectoryTableProps {
  account: string;
  entries: TreeEntry[];
  search: Record<string, string | undefined>;
}

export function SourceDirectoryTable({
  account,
  entries,
  search
}: SourceDirectoryTableProps) {
  const sortedEntries = sortEntries(entries);
  const hasMode = sortedEntries.some((entry) => entry.mode !== undefined);
  const hasSize = sortedEntries.some((entry) => Boolean(entry.size));
  const hasObjectId = sortedEntries.some(
    (entry) => entry.contentHash || entry.blobId || entry.treeId
  );
  const hasSymlinkTarget = sortedEntries.some((entry) => entry.symlinkTarget);

  if (sortedEntries.length === 0) {
    return (
      <div className="rounded-lg border border-dashed border-slate-300 bg-white p-8 text-sm text-slate-600">
        This directory is empty.
      </div>
    );
  }

  return (
    <div className="overflow-hidden rounded-lg border border-slate-200 bg-white">
      <div className="overflow-x-auto">
        <table className="min-w-full divide-y divide-slate-200 text-left text-sm">
          <thead className="bg-slate-50 text-xs font-semibold uppercase tracking-normal text-slate-500">
            <tr>
              <th className="px-4 py-3">Name</th>
              <th className="px-4 py-3">Kind</th>
              {hasMode ? <th className="px-4 py-3">Mode</th> : null}
              {hasSize ? <th className="px-4 py-3">Size</th> : null}
              {hasObjectId ? <th className="px-4 py-3">Content</th> : null}
              {hasSymlinkTarget ? <th className="px-4 py-3">Symlink target</th> : null}
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100">
            {sortedEntries.map((entry) => {
              const routePath = repositoryPathToRoutePath(account, entry.path);
              const isDirectory = entry.kind === "ENTRY_KIND_DIRECTORY";
              const name = entryDisplayName(entry);
              const objectRows = [
                entry.contentHash ? { label: "hash", value: entry.contentHash } : null,
                entry.blobId ? { label: "blob", value: entry.blobId } : null,
                entry.treeId ? { label: "tree", value: entry.treeId } : null
              ].filter(Boolean) as Array<{ label: string; value: string }>;

              return (
                <tr className="align-top transition hover:bg-slate-50" key={entry.path ?? name}>
                  <td className="min-w-56 px-4 py-3">
                    <Link
                      className="font-medium text-zinc-950 underline-offset-4 hover:underline"
                      params={
                        routePath
                          ? ({ account, _splat: routePath } as never)
                          : ({ account } as never)
                      }
                      search={search as never}
                      to={routePath ? "/source/$account/$" : "/source/$account"}
                    >
                      {name}
                      {isDirectory ? "/" : ""}
                    </Link>
                    {entry.path ? (
                      <div className="mt-1 max-w-96 truncate font-mono text-xs text-slate-400">
                        {entry.path}
                      </div>
                    ) : null}
                  </td>
                  <td className="px-4 py-3 text-slate-600">
                    {entryKindLabel(entry.kind)}
                  </td>
                  {hasMode ? (
                    <td className="px-4 py-3 font-mono text-xs text-slate-600">
                      {formatMode(entry.mode)}
                    </td>
                  ) : null}
                  {hasSize ? (
                    <td className="whitespace-nowrap px-4 py-3 font-mono text-xs text-slate-600">
                      {formatSize(entry.size)}
                    </td>
                  ) : null}
                  {hasObjectId ? (
                    <td className="px-4 py-3">
                      <div className="grid gap-1 font-mono text-xs text-slate-600">
                        {objectRows.map((row) => (
                          <div className="flex min-w-52 gap-2" key={row.label}>
                            <span className="w-8 shrink-0 text-slate-400">
                              {row.label}
                            </span>
                            <span title={row.value}>{trimMiddle(row.value)}</span>
                          </div>
                        ))}
                      </div>
                    </td>
                  ) : null}
                  {hasSymlinkTarget ? (
                    <td className="px-4 py-3 font-mono text-xs text-slate-600">
                      {entry.symlinkTarget}
                    </td>
                  ) : null}
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}

