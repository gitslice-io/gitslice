import type { ReactNode } from "react";
import { useState } from "react";

import {
  InlineRenameForm,
  parentRepositoryPath,
  repositoryPathName,
  validateEntryName,
  type PendingEdit
} from "../../components/source/SliceEditing";
import { ActionMenu } from "../../components/source/ActionMenu";
import { shortHash } from "../../lib/objectId";

interface DirectoryHeaderProps {
  commitId: string;
  includedPaths: string[];
  onOpenHistory?: () => void;
  onStageEdit?: (edit: PendingEdit) => void;
  selectedPath: string;
  actions?: ReactNode;
  children?: ReactNode;
}

const TOP_LEVEL_SLICE_FOLDER_TITLE =
  "Top-level slice folders are managed in the slice definition (Settings).";

export function DirectoryHeader({
  commitId,
  includedPaths,
  onOpenHistory,
  onStageEdit,
  selectedPath,
  actions,
  children
}: DirectoryHeaderProps) {
  const [isRenaming, setIsRenaming] = useState(false);
  const canRename = Boolean(selectedPath && onStageEdit);
  const canModifySelectedPath = canModifyPath(includedPaths, selectedPath);
  const modifyDisabledTitle = canModifySelectedPath
    ? undefined
    : TOP_LEVEL_SLICE_FOLDER_TITLE;

  function stageRename(name: string) {
    if (!onStageEdit) {
      return;
    }
    onStageEdit({
      kind: "rename",
      oldPath: selectedPath,
      path: joinRepositoryPath(parentRepositoryPath(selectedPath), name)
    });
    setIsRenaming(false);
  }

  const ownMenu = canRename ? (
    <ActionMenu
      items={[
        {
          label: "Rename",
          disabled: !canModifySelectedPath,
          onSelect: () => setIsRenaming(true),
          title: modifyDisabledTitle
        },
        {
          label: "Delete",
          disabled: !canModifySelectedPath,
          onSelect: () => onStageEdit?.({ kind: "delete", path: selectedPath }),
          title: modifyDisabledTitle,
          tone: "danger"
        }
      ]}
      label="Directory actions"
    />
  ) : null;
  const rightActions = actions !== undefined ? actions : ownMenu;
  const historyButton = onOpenHistory ? (
    <button
      className="rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 active:scale-[0.98]"
      onClick={onOpenHistory}
      type="button"
    >
      History
    </button>
  ) : null;

  return (
    <div className="border-b border-slate-200 px-4 py-4 sm:px-5">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 flex-1">
          <h2 className="break-all text-base font-semibold text-zinc-950">
            {selectedPath || "Slice root"}
          </h2>
          <p className="mt-1 hidden text-xs text-slate-500 sm:block">
            Commit{" "}
            <span className="font-mono text-slate-700" title={commitId}>
              {shortHash(commitId)}
            </span>
          </p>
        </div>
        {historyButton || rightActions ? (
          <div className="flex shrink-0 items-center gap-2">
            {historyButton}
            {rightActions}
          </div>
        ) : null}
      </div>
      {children}
      {actions === undefined && isRenaming && canModifySelectedPath ? (
        <div className="mt-3">
          <InlineRenameForm
            directoryPath={parentRepositoryPath(selectedPath)}
            onCancel={() => setIsRenaming(false)}
            onSave={stageRename}
            originalName={repositoryPathName(selectedPath)}
          />
        </div>
      ) : null}
    </div>
  );
}

export function canModifyPath(includedPaths: string[], path: string): boolean {
  const normalizedPath = path.replace(/^\/+|\/+$/g, "");
  return includedPaths.some((includedPath) =>
    pathCoversCurrentPath(includedPath, normalizedPath)
  ) && !isIncludedRoot(includedPaths, path);
}

function pathCoversCurrentPath(includedPath: string, currentPath: string) {
  const normalizedIncluded = includedPath.replace(/^\/+|\/+$/g, "");
  if (normalizedIncluded === "") {
    return true;
  }
  return currentPath === normalizedIncluded || currentPath.startsWith(`${normalizedIncluded}/`);
}

function isIncludedRoot(includedPaths: string[], path: string): boolean {
  const normalizedPath = path.replace(/^\/+|\/+$/g, "");
  return includedPaths.some(
    (includedPath) => includedPath.replace(/^\/+|\/+$/g, "") === normalizedPath
  );
}

export function joinRepositoryPath(...parts: string[]) {
  return "/" + parts.filter(Boolean).join("/");
}