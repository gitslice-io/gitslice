import { useState } from "react";

import type { SliceRef } from "../../api/types";
import { ActionMenu } from "../../components/source/ActionMenu";
import {
  InlineRenameForm,
  NewFileForm,
  joinRepositoryPath,
  parentRepositoryPath,
  type PendingEdit
} from "../../components/source/SliceEditing";
import {
  canCreateInPath,
  entryDisplayName,
  entryKindLabel,
  formatSize,
  normalizeRepositoryPath,
  sortEntries
} from "../../components/source/sourceUtils";
import { Button } from "../../components/ui/Button";
import { CopyButton } from "../../components/ui/CopyButton";
import { shortHash } from "../../lib/objectId";
import { copyMoveMenuItems, useCopyMove } from "./CopyMove";
import { canModifyPath } from "./DirectoryHeader";

const TOP_LEVEL_SLICE_FOLDER_TITLE =
  "Top-level slice folders are managed in the slice definition (Settings).";

interface SliceDirectoryTableProps {
  createDirectory?: string;
  entries: import("../../api/types").TreeEntry[];
  includedPaths: string[];
  onSelectPath(path: string): void;
  onStageEdit?: (edit: PendingEdit) => void;
  sliceRef?: SliceRef;
  // compact is a names-only list for a narrow column: no heading row, kind,
  // size, hash or path line.
  compact?: boolean;
}

export function SliceDirectoryTable({
  createDirectory,
  entries,
  includedPaths,
  onSelectPath,
  onStageEdit,
  sliceRef,
  compact = false
}: SliceDirectoryTableProps) {
  const [renamingPath, setRenamingPath] = useState("");
  const [creatingFile, setCreatingFile] = useState(false);
  const copyMove = useCopyMove();
  const sortedEntries = sortEntries(entries);
  const showActions = Boolean(onStageEdit);
  const targetCreateDir = createDirectory || includedPaths[0] || "";
  const canCreateHere = Boolean(
    onStageEdit && targetCreateDir && canCreateInPath(includedPaths, targetCreateDir)
  );

  if (!sortedEntries.length) {
    const initCmd =
      sliceRef?.account && sliceRef?.slice
        ? `gs init ${sliceRef.account}:${sliceRef.slice} .`
        : "";
    return (
      <div className="p-6 sm:p-8">
        <p className="text-sm font-medium text-zinc-950 dark:text-zinc-50">
          This slice-projected directory is empty.
        </p>
        <p className="mt-1 text-sm text-slate-600 dark:text-zinc-400">
          Add your first file directly here or check out this slice with the CLI.
        </p>
        <div className="mt-4 flex flex-wrap items-center gap-3">
          {canCreateHere && !creatingFile ? (
            <Button
              className="px-3 py-1.5"
              onClick={() => setCreatingFile(true)}
              size="sm"
              variant="primary"
            >
              New file
            </Button>
          ) : null}
          {initCmd ? (
            <div className="inline-flex max-w-full items-center gap-2 rounded-md border border-slate-200 bg-slate-50 px-3 py-1.5 font-mono text-xs text-slate-700 dark:border-zinc-800 dark:bg-zinc-950 dark:text-zinc-300">
              <code className="truncate">{initCmd}</code>
              <CopyButton
                ariaLabel="Copy checkout command"
                className="rounded px-1.5 py-0.5 text-[11px] font-sans font-medium text-slate-600 transition hover:bg-slate-200/70 hover:text-zinc-950 dark:text-zinc-400 dark:hover:bg-zinc-800 dark:hover:text-zinc-50"
                value={initCmd}
                variant="unstyled"
              />
            </div>
          ) : null}
        </div>
        {creatingFile && canCreateHere ? (
          <div className="mt-4">
            <NewFileForm
              directoryPath={targetCreateDir}
              onCancel={() => setCreatingFile(false)}
              onCreate={(edit) => {
                onStageEdit?.(edit);
                setCreatingFile(false);
              }}
            />
          </div>
        ) : null}
      </div>
    );
  }

  return (
    <div className="overflow-x-auto">
      <table className="min-w-full divide-y divide-slate-200 dark:divide-zinc-800 text-left text-sm">
        {compact ? null : (
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
        )}
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
                <td className={compact ? "min-w-0 px-3 py-2" : "min-w-0 px-3 py-3 sm:min-w-56 sm:px-4"}>
                  <button
                    className="break-words text-left font-medium text-zinc-950 dark:text-zinc-50 underline-offset-4 hover:underline"
                    onClick={() => onSelectPath(path)}
                    type="button"
                  >
                    {displayName}
                    {isDirectory ? "/" : ""}
                  </button>
                  {entry.path && !compact ? (
                    <div className="mt-1 max-w-96 break-all font-mono text-xs text-slate-400 dark:text-zinc-500 sm:truncate">
                      {entry.path}
                    </div>
                  ) : null}
                </td>
                {compact ? null : (
                  <>
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
                  </>
                )}
                {showActions ? (
                  <td className={compact ? "px-3 py-2 text-right" : "min-w-40 px-4 py-3 text-right sm:min-w-48 sm:px-5"}>
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
                          ...copyMoveMenuItems(
                            copyMove,
                            { path, isDirectory },
                            {
                              canModifySource: canModifyEntryPath,
                              disabledTitle: TOP_LEVEL_SLICE_FOLDER_TITLE
                            }
                          ),
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