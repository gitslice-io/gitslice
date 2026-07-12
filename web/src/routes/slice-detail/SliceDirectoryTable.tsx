import { useState } from "react";

import { ActionMenu } from "../../components/source/ActionMenu";
import {
  InlineRenameForm,
  joinRepositoryPath,
  parentRepositoryPath,
  type PendingEdit
} from "../../components/source/SliceEditing";
import {
  entryDisplayName,
  entryKindLabel,
  formatSize,
  normalizeRepositoryPath,
  sortEntries
} from "../../components/source/sourceUtils";
import { shortHash } from "../../lib/objectId";
import { canModifyPath } from "./DirectoryHeader";

const TOP_LEVEL_SLICE_FOLDER_TITLE =
  "Top-level slice folders are managed in the slice definition (Settings).";

interface SliceDirectoryTableProps {
  entries: import("../../api/types").TreeEntry[];
  includedPaths: string[];
  onSelectPath(path: string): void;
  onStageEdit?: (edit: PendingEdit) => void;
}

export function SliceDirectoryTable({
  entries,
  includedPaths,
  onSelectPath,
  onStageEdit
}: SliceDirectoryTableProps) {
  const [renamingPath, setRenamingPath] = useState("");
  const sortedEntries = sortEntries(entries);
  const showActions = Boolean(onStageEdit);

  if (!sortedEntries.length) {
    return (
      <div className="p-8 text-sm text-slate-600 dark:text-zinc-400">
        This slice-projected directory is empty.
      </div>
    );
  }

  return (
    <div className="overflow-x-auto">
      <table className="min-w-full divide-y divide-slate-200 dark:divide-zinc-800 text-left text-sm">
        <thead className="bg-slate-50 dark:bg-zinc-950 text-xs font-semibold uppercase tracking-normal text-slate-500 dark:text-zinc-400">
          <tr>
            <th className="w-full px-3 py-3 sm:px-4">Name</th>
            <th className="hidden px-4 py-3 md:table-cell">Kind</th>
            <th className="hidden px-4 py-3 md:table-cell">Size</th>
            <th className="hidden px-4 py-3 md:table-cell">Content hash</th>
            {showActions ? (
              <th className="px-4 py-3 text-right sm:px-5">Actions</th>
            ) : null}
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-zinc-800">
          {sortedEntries.map((entry) => {
            const path = normalizeRepositoryPath(entry.path ?? "");
            const isDirectory = entry.kind === "ENTRY_KIND_DIRECTORY";
            const isRenaming = renamingPath === path;
            const displayName = entryDisplayName(entry);
            const entryHash =
              entry.contentHash || entry.blobId || entry.treeId || "";
            const canModifyEntryPath = canModifyPath(includedPaths, path);
            const modifyDisabledTitle = canModifyEntryPath
              ? undefined
              : TOP_LEVEL_SLICE_FOLDER_TITLE;

            return (
              <tr className="align-top transition hover:bg-slate-50 dark:hover:bg-zinc-950" key={entry.path ?? entryDisplayName(entry)}>
                <td className="min-w-0 px-3 py-3 sm:min-w-56 sm:px-4">
                  <button
                    className="break-words text-left font-medium text-zinc-950 dark:text-zinc-50 underline-offset-4 hover:underline"
                    onClick={() => onSelectPath(path)}
                    type="button"
                  >
                    {displayName}
                    {isDirectory ? "/" : ""}
                  </button>
                  {entry.path ? (
                    <div className="mt-1 max-w-96 break-all font-mono text-xs text-slate-400 dark:text-zinc-500 sm:truncate">
                      {entry.path}
                    </div>
                  ) : null}
                </td>
                <td className="hidden px-4 py-3 text-slate-600 dark:text-zinc-400 md:table-cell">
                  {entryKindLabel(entry.kind)}
                </td>
                <td className="hidden whitespace-nowrap px-4 py-3 font-mono text-xs text-slate-600 dark:text-zinc-400 md:table-cell">
                  {formatSize(entry.size)}
                </td>
                <td className="hidden whitespace-nowrap px-4 py-3 font-mono text-xs text-slate-600 dark:text-zinc-400 md:table-cell">
                  {entryHash ? (
                    <span title={entryHash}>{shortHash(entryHash)}</span>
                  ) : (
                    ""
                  )}
                </td>
                {showActions ? (
                  <td className="min-w-40 px-4 py-3 text-right sm:min-w-48 sm:px-5">
                    {isRenaming && canModifyEntryPath ? (
                      <InlineRenameForm
                        directoryPath={parentRepositoryPath(path)}
                        onCancel={() => setRenamingPath("")}
                        onSave={(name) => {
                          onStageEdit?.({
                            kind: "rename",
                            oldPath: path,
                            path: joinRepositoryPath(
                              parentRepositoryPath(path),
                              name
                            )
                          });
                          setRenamingPath("");
                        }}
                        originalName={displayName}
                      />
                    ) : (
                      <ActionMenu
                        items={[
                          {
                            label: "Rename",
                            disabled: !canModifyEntryPath,
                            onSelect: () => setRenamingPath(path),
                            title: modifyDisabledTitle
                          },
                          {
                            label: "Delete",
                            disabled: !canModifyEntryPath,
                            onSelect: () =>
                              onStageEdit?.({ kind: "delete", path }),
                            title: modifyDisabledTitle,
                            tone: "danger"
                          }
                        ]}
                        label={`Actions for ${displayName}`}
                      />
                    )}
                  </td>
                ) : null}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}